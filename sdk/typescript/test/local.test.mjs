import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdtemp, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath, pathToFileURL } from 'node:url';
import test, { after } from 'node:test';

const { CutmyClient } = await import(process.env.CUTMY_SDK_JS_ENTRY ? pathToFileURL(process.env.CUTMY_SDK_JS_ENTRY).href : '../dist/index.js');
const { LocalCutmy, LocalError, downloadArtifactToFile, uploadFile } = await import(process.env.CUTMY_SDK_LOCAL_JS_ENTRY ? pathToFileURL(process.env.CUTMY_SDK_LOCAL_JS_ENTRY).href : '../dist/local.js');
const fixturePath = fileURLToPath(new URL('./fake-cli.mjs', import.meta.url));
const plan = { version: 1, input: 'input with spaces ; $(unsafe).mp4', ranges: [{ start_ms: 0, end_ms: 1000 }], options: { format: 'mp4' } };
const local = (env = {}, extra = {}) => new LocalCutmy({ binary: process.execPath, binaryArgs: [fixturePath], env, ...extra });
async function directory(t) { const path = await mkdtemp(join(tmpdir(), 'cutmy-sdk-test-')); t.after(() => rm(path, { recursive: true, force: true })); return path; }

test('local wrapper uses literal arguments, private temporary plans and correct relative paths', async t => {
  const cwd = await directory(t);
  const engine = local({}, { cwd });
  const info = await engine.inspect(plan.input);
  assert.equal(info.input, resolve(cwd, plan.input));
  const report = await engine.preflight(plan, 'output path');
  assert.equal(report.input, resolve(cwd, plan.input));
  assert.equal(report.output_paths[0], resolve(cwd, 'output path'));
  await assert.rejects(stat(report.plan_path), e => e.code === 'ENOENT');
  assert.equal(plan.input, 'input with spaces ; $(unsafe).mp4');
  const result = await engine.executePlan(plan, 'output path');
  assert.equal(result.outputs[0].path, '/published/one.mp4');
  assert.deepEqual(await readdir(cwd), []);
});

test('local terminal errors preserve machine code and earlier published outputs', async () => {
  await assert.rejects(local({ FAKE_MODE: 'fail' }).executePlan(plan, '.'), e => e instanceof LocalError && e.code === 'future_local_error' && e.exitCode === 1 && e.result.incomplete && e.result.outputs[0].path === '/published/one.mp4');
  await assert.rejects(local({ FAKE_MODE: 'invalid' }).inspect('x'), e => e.code === 'invalid_response' && !e.message.includes('private'));
  await assert.rejects(new LocalCutmy({ binary: 'nonexistent-cutmy-sdk-binary' }).inspect('x'), e => e.code === 'tool_unavailable');
});

test('local timeout waits for graceful termination and retains partial result', async () => {
  await assert.rejects(local({ FAKE_MODE: 'stall' }).executePlan(plan, '.', { timeoutMs: 750 }), e => e.code === 'timeout' && (process.platform === 'win32' || e.result?.outputs.length === 1));
});

test('local caller cancellation stops execution, oversize output and stubborn processes are reaped', async t => {
  const controller = new AbortController();
  const cancellation = local({ FAKE_MODE: 'stall' }).inspect('x', { signal: controller.signal });
  setTimeout(() => controller.abort(), 200);
  await assert.rejects(cancellation, e => e.code === 'cancelled');
  await assert.rejects(local({ FAKE_MODE: 'oversize' }, { terminationGraceMs: 50 }).inspect('x'), e => e.code === 'response_too_large');
  const cwd = await directory(t);
  const marker = join(cwd, 'pid');
  await assert.rejects(local({ FAKE_MODE: 'force', FAKE_PID_PATH: marker }, { terminationGraceMs: 50 }).inspect('x', { timeoutMs: 750 }), e => e.code === 'timeout');
  const pid = Number(await readFile(marker, 'utf8'));
  assert.throws(() => process.kill(pid, 0), e => e.code === 'ESRCH');
});

test('local validation rejects invalid limits, missing paths and oversized plans before process start', async () => {
  assert.throws(() => local({}, { threads: 33 }), /threads/);
  assert.throws(() => local().inspect(''), /input/);
  await assert.rejects(local().preflight({ ...plan, input: '' }, '.'), /required/);
  await assert.rejects(local().preflight({ ...plan, ranges: [{ start_ms: 0, end_ms: 1000, label: 'x'.repeat(1 << 20) }] }, '.'), /1 MiB/);
  const controller = new AbortController(); controller.abort();
  await assert.rejects(local().executePlan(plan, '.', { signal: controller.signal }), e => e.code === 'cancelled');
});

test('POSIX cancellation kills an ignoring descendant after its launcher exits', { skip: process.platform === 'win32' }, async t => {
  const cwd = await directory(t);
  const marker = join(cwd, 'descendant-pid');
  await assert.rejects(local({ FAKE_MODE: 'orphan', FAKE_ORPHAN_PID_PATH: marker }).inspect('x', { timeoutMs: 750 }), e => e.code === 'timeout');
  const pid = Number(await readFile(marker, 'utf8'));
  // An orphan can briefly remain a zombie while its OS reaper handles SIGKILL.
  let terminated = false;
  t.after(() => { if (!terminated) { try { process.kill(pid, 'SIGKILL'); } catch { /* Owned fixture already exited. */ } } });
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      process.kill(pid, 0);
      if (process.platform === 'linux') {
        const state = await readFile(`/proc/${pid}/stat`, 'utf8');
        if (/^\d+ \(.*\) Z /.test(state)) { terminated = true; break; }
      }
    } catch (error) { if (error.code === 'ESRCH' || error.code === 'ENOENT') { terminated = true; break; } throw error; }
    await new Promise(resolve => setTimeout(resolve, 10));
  }
  assert.equal(terminated, true, 'An owned descendant must not remain running after cancellation');
});

test('Node file upload is disk-backed and download publishes exclusively with cleanup on failure', async t => {
  const cwd = await directory(t);
  const input = join(cwd, 'source.mov');
  await writeFile(input, 'native disk-backed upload');
  const session = { ok: true, limits: { max_source_bytes: 1000000, max_output_bytes: 1000000, max_fetch_bytes: 1000000, max_ranges: 128, max_range_ms: 100000, max_job_ms: 100000 } };
  const server = createServer(async (req, res) => {
    if (req.url === '/api/v1/session') { res.writeHead(200, { 'set-cookie': `cutmy_session=${'a'.repeat(43)}; Path=/`, 'content-type': 'application/json' }); res.end(JSON.stringify(session)); }
    else if (req.url === '/api/v1/uploads') {
      const chunks = []; for await (const data of req) chunks.push(data);
      assert.match(Buffer.concat(chunks).toString(), /native disk-backed upload/);
      res.writeHead(201, { 'content-type': 'application/json' }); res.end(JSON.stringify({ id: 'source-1', duration_ms: 5000 }));
    } else { res.writeHead(200, { 'content-type': 'video/mp4' }); res.end('media bytes'); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => { server.closeAllConnections(); return new Promise(resolve => server.close(resolve)); });
  const client = new CutmyClient({ baseUrl: `http://127.0.0.1:${server.address().port}` });
  assert.equal((await uploadFile(client, input)).id, 'source-1');
  const output = join(cwd, 'result.mp4');
  assert.equal(await downloadArtifactToFile(client, 'artifact-1', output), output);
  assert.equal(await readFile(output, 'utf8'), 'media bytes');
  await assert.rejects(downloadArtifactToFile(client, 'artifact-1', output), e => e.code === 'output_exists');
  assert.equal(await readFile(output, 'utf8'), 'media bytes');
  await assert.rejects(downloadArtifactToFile(client, 'artifact-1', join(cwd, 'oversize'), { maxBytes: 2 }), e => e.code === 'download_too_large');
  assert.deepEqual((await readdir(cwd)).sort(), ['result.mp4', 'source.mov']);
});

// Safe lifecycle diagnostics: never serialize reports, environment or socket data.
after(() => {
  const handles = process._getActiveHandles().map(handle => ({ type: handle.constructor.name, ...(typeof handle.pid === 'number' ? { pid: handle.pid } : {}), ...(typeof handle.destroyed === 'boolean' ? { destroyed: handle.destroyed } : {}), ...(typeof handle.listening === 'boolean' ? { listening: handle.listening } : {}) }));
  const timers = process.report.getReport().libuv.filter(handle => handle.type === 'timer').map(handle => ({ type: handle.type, active: handle.is_active, referenced: handle.is_referenced, fires_ms: handle.firesInMsFromNow, repeat: handle.repeat }));
  process.stderr.write(JSON.stringify({ event: 'local-fixture-resources', resources: process.getActiveResourcesInfo(), handles, requests: process._getActiveRequests().map(request => request.constructor.name), timers }) + '\n');
});
