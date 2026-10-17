import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { mkdtemp, readFile, writeFile, rm, unlink, stat, link } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { pathToFileURL } from 'node:url';
import test from 'node:test';

const mainEntry = process.env.CUTMY_SDK_JS_ENTRY ? pathToFileURL(process.env.CUTMY_SDK_JS_ENTRY).href : new URL('../dist/index.js', import.meta.url).href;
const sourceEntry = process.env.CUTMY_SDK_SOURCE_JS_ENTRY ? pathToFileURL(process.env.CUTMY_SDK_SOURCE_JS_ENTRY).href : new URL('../dist/sources.js', import.meta.url).href;
const { CutmyClient, SourcePreparationError } = await import(mainEntry);
const { prepareSource, resumeSource } = await import(sourceEntry);
const cookie = 's'.repeat(43), hash = value => createHash('sha256').update(value).digest('hex');
const limits = { max_source_bytes: 10000000, max_output_bytes: 10000000, max_fetch_bytes: 10000000, max_ranges: 128, max_range_ms: 100000, max_job_ms: 100000 };
function json(response, value, status = 200) { response.writeHead(status, { 'content-type': 'application/json' }); response.end(JSON.stringify(value)); }
async function fixture(t) {
  const receipts = new Map(), posts = [], calls = [], config = {};
  const root = await mkdtemp(join(tmpdir(), 'cutmy-source-sdk-')); let server;
  t.after(async () => {
    // Cancelled file-backed uploads can finish releasing Windows handles after
    // their promise settles. Always close the owned server before removing files.
    try {
      if (server) await new Promise((resolve, reject) => {
        server.close(error => error && error.code !== 'ERR_SERVER_NOT_RUNNING' ? reject(error) : resolve());
        server.closeAllConnections();
      });
    } finally {
      await rm(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
      await assert.rejects(stat(root), { code: 'ENOENT' });
    }
  }, { timeout: 5000 });
  const path = join(root, 'interview.mp4'), payload = Buffer.from('source-fixture'.repeat(100)); await writeFile(path, payload);
  server = createServer(async (req, res) => {
    calls.push({ method: req.method, path: req.url });
    try {
      if (req.url === '/api/v1/session') { res.setHeader('Set-Cookie', 'cutmy_session=' + cookie); return json(res, { ok: true, limits }); }
      assert.equal(req.headers.cookie, 'cutmy_session=' + cookie);
      if (req.method === 'GET') { const key = decodeURIComponent(req.url.split('/').at(-1)); return receipts.has(key) ? json(res, receipts.get(key)) : json(res, { error: { code: 'not_found', message: 'No receipt' } }, 404); }
      const key = req.headers['idempotency-key']; posts.push({ key, path: req.url });
      if (config.blockUpload) { req.pause(); config.uploadStarted?.(); return; }
      const chunks = []; for await (const part of req) chunks.push(part); const body = Buffer.concat(chunks);
      let fingerprint;
      if (req.url === '/api/v1/uploads') {
        assert.equal(req.headers['upload-sha256'], hash(payload)); assert.equal(Number(req.headers['upload-size']), payload.length);
        assert(body.includes(payload)); fingerprint = { kind: 'content', sha256: hash(payload), size_bytes: payload.length };
      } else fingerprint = { kind: 'provider', provider: 'fixture', provider_id: 'recording', canonical_url_sha256: hash(JSON.parse(body).url) };
      if (!receipts.has(key)) {
        const source = { id: 'source-' + (receipts.size + 1), duration_ms: 9000, title: 'Fixture', fingerprint, unknown_secret: cookie };
        receipts.set(key, { version: 1, key, state: 'ready', source_id: source.id, source, fingerprint });
      }
      if (config.loseReply) { config.loseReply = false; req.socket.destroy(); return; }
      json(res, receipts.get(key).source, 201);
    } catch { if (!res.headersSent) json(res, { error: { code: 'fixture_error', message: 'Fixture failed' } }, 500); else res.destroy(); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const baseUrl = 'http://127.0.0.1:' + server.address().port;
  return { root, path, payload, project: join(root, 'project'), config, posts, calls, receipts, baseUrl, client: new CutmyClient({ baseUrl }) };
}

test('durable source upload recovers a lost committed reply with saved owner, key and fingerprint without reupload', async t => {
  const f = await fixture(t); f.config.loseReply = true;
  await assert.rejects(prepareSource(f.client, { kind: 'upload', path: f.path }, f.project, { idempotencyKey: 'private-source-key', timeoutMs: 10000,
    onProgress: value => { assert(!JSON.stringify(value).includes(cookie)); if (value.phase === 'uncertain') throw new Error('Stop after lost reply'); },
  }), error => error instanceof SourcePreparationError && error.code === 'callback_error');
  await unlink(f.path);
  const result = await resumeSource(new CutmyClient({ baseUrl: f.baseUrl }), f.project, { timeoutMs: 10000 });
  assert.equal(result.complete, true); assert.equal(result.source_id, 'source-1'); assert.equal(f.posts.length, 1);
  assert.deepEqual(result.fingerprint, { kind: 'content', sha256: hash(f.payload), size_bytes: f.payload.length });
  assert(!JSON.stringify(result).includes(cookie)); assert(!JSON.stringify(result).includes('private-source-key'));
});

test('space-bearing preparation and scoped retry keys fail before owner, networking or durable initialization', async t => {
  const f = await fixture(t);
  for (const key of ['agent key', ' leading', 'trailing ', '.', '..']) {
    await assert.rejects(f.client.getSourcePreparation(key), TypeError);
    assert.throws(() => f.client.importSource('https://example.test/interview', { idempotencyKey: key }), TypeError);
    assert.throws(() => f.client.upload(new Blob(['source']), 'source', { idempotencyKey: key }), TypeError);
    assert.throws(() => f.client.submitProjectCollection('project', { revision: 1, clip_ids: ['clip1'] }, { retryFailed: true, idempotencyKey: key }), TypeError);
    await assert.rejects(prepareSource(f.client, { kind: 'upload', path: f.path }, f.project, { idempotencyKey: key }), TypeError);
    await assert.rejects(stat(f.project), { code: 'ENOENT' });
    assert.deepEqual(f.calls, []);
    assert.throws(() => f.client.exportSessionCookie(), error => error.code === 'session_unavailable');
  }
  assert.equal((await prepareSource(f.client, { kind: 'import', url: 'https://example.test/interview' }, f.project,
    { idempotencyKey: '!source~', timeoutMs: 10000 })).complete, true);
});

test('changed input and conflicting trusted server/initialized owner never create a second source', async t => {
  const f = await fixture(t);
  await assert.rejects(prepareSource(f.client, { kind: 'upload', path: f.path }, f.project, { onProgress: value => { if (value.phase === 'created') throw new Error('Stop before upload'); } }), error => error.code === 'callback_error');
  const before = f.calls.length;
  for (const client of [new CutmyClient({ baseUrl: 'http://127.0.0.1:1' }), new CutmyClient({ baseUrl: f.baseUrl, sessionCookie: 'x'.repeat(43) })]) await assert.rejects(resumeSource(client, f.project, { timeoutMs: 1000 }), error => error instanceof SourcePreparationError);
  assert.equal(f.calls.length, before);
  await writeFile(f.path, 'changed-input');
  await assert.rejects(resumeSource(new CutmyClient({ baseUrl: f.baseUrl }), f.project, { timeoutMs: 10000 }), error => error.code === 'source_changed');
  assert.equal(f.posts.length, 0);
});

test('source preparation journal validates both primaries before completing an owner-only crash boundary', async t => {
  const f = await fixture(t);
  await assert.rejects(prepareSource(f.client, { kind: 'upload', path: f.path }, f.project, { idempotencyKey: 'original-source-key', onProgress: value => { if (value.phase === 'created') throw new Error('Stop before upload'); } }), error => error.code === 'callback_error');
  const state = JSON.parse(await readFile(join(f.project, 'state.json'))), owner = JSON.parse(await readFile(join(f.project, 'owner.json')));
  await writeFile(join(f.project, 'initialize-source.json'), JSON.stringify({ schema_version: 1, owner, state }), { mode: 0o600 });
  await unlink(join(f.project, 'state.json'));
  await writeFile(join(f.project, 'owner.json'), JSON.stringify({ ...owner, session_cookie: 'y'.repeat(43) }));
  const before = f.calls.length;
  await assert.rejects(resumeSource(new CutmyClient({ baseUrl: f.baseUrl }), f.project), error => error.code === 'initialization_conflict');
  await assert.rejects(readFile(join(f.project, 'state.json')), { code: 'ENOENT' }); assert.equal(f.calls.length, before);
  await writeFile(join(f.project, 'owner.json'), JSON.stringify(owner));
  const result = await resumeSource(new CutmyClient({ baseUrl: f.baseUrl }), f.project, { timeoutMs: 10000 });
  assert.equal(result.complete, true); assert.deepEqual(f.posts, [{ key: 'original-source-key', path: '/api/v1/uploads' }]);
});

test('fresh consumers recover source initialization interrupted between checkpoint link and unlink', async t => {
  for (const boundary of ['initialize-source.json', 'owner.json', 'state.json']) {
    await t.test(boundary, async t => {
      const f = await fixture(t);
      // Stop the actual source runner immediately after the selected hard-link
      // publication. process.exit leaves its filesystem state and lock receipt.
      const script = `
        import fs from 'node:fs/promises';
        import { syncBuiltinESMExports } from 'node:module';
        import { basename } from 'node:path';
        const [mainEntry, sourceEntry, baseUrl, path, directory, boundary] = process.argv.slice(1);
        const original = fs.link;
        fs.link = async (...args) => {
          await original(...args);
          if (basename(args[1]) === boundary) process.exit(99);
        };
        syncBuiltinESMExports();
        const { CutmyClient } = await import(mainEntry);
        const { prepareSource } = await import(sourceEntry);
        await prepareSource(new CutmyClient({ baseUrl }), { kind: 'upload', path }, directory,
          { idempotencyKey: 'crash-source-key', timeoutMs: 10000 });
        process.exit(1);
      `;
      const child = spawn(process.execPath, ['--input-type=module', '-e', script,
        mainEntry, sourceEntry, f.baseUrl, f.path, f.project, boundary], { stdio: 'ignore', windowsHide: true });
      const code = await new Promise((resolve, reject) => { child.once('error', reject); child.once('close', resolve); });
      assert.equal(code, 99);
      assert.equal((await stat(join(f.project, boundary))).nlink, 2);
      assert.deepEqual(f.posts, []);
      const result = await resumeSource(new CutmyClient({ baseUrl: f.baseUrl }), f.project, { timeoutMs: 10000 });
      assert.equal(result.complete, true);
      assert.deepEqual(f.posts, [{ key: 'crash-source-key', path: '/api/v1/uploads' }]);
      assert.equal((await stat(join(f.project, 'owner.json'))).nlink, 1);
      assert.equal((await stat(join(f.project, 'state.json'))).nlink, 1);
      await assert.rejects(stat(join(f.project, 'initialize-source.json')), { code: 'ENOENT' });
      const replay = await resumeSource(new CutmyClient({ baseUrl: f.baseUrl }), f.project, { timeoutMs: 10000 });
      assert.equal(replay.source_id, result.source_id); assert.equal(f.posts.length, 1);
    });
  }
});

test('source journal recovery rejects an unverified hard link before repairing primaries or contacting the server', async t => {
  const f = await fixture(t);
  await assert.rejects(prepareSource(f.client, { kind: 'upload', path: f.path }, f.project,
    { onProgress: value => { if (value.phase === 'created') throw new Error('Stop before upload'); } }),
    error => error.code === 'callback_error');
  const state = JSON.parse(await readFile(join(f.project, 'state.json'))), owner = JSON.parse(await readFile(join(f.project, 'owner.json')));
  const unrelated = join(f.project, 'unrelated.json');
  await writeFile(unrelated, JSON.stringify({ schema_version: 1, owner, state }), { mode: 0o600 });
  await link(unrelated, join(f.project, 'initialize-source.json'));
  await unlink(join(f.project, 'state.json')); await unlink(join(f.project, 'owner.json'));
  const before = f.calls.length, original = await readFile(unrelated);
  await assert.rejects(resumeSource(new CutmyClient({ baseUrl: f.baseUrl }), f.project), error => error.code === 'unverified_checkpoint');
  assert.equal(f.calls.length, before); assert.deepEqual(await readFile(unrelated), original);
  assert.equal((await stat(unrelated)).nlink, 2);
  for (const name of ['owner.json', 'state.json']) await assert.rejects(stat(join(f.project, name)), { code: 'ENOENT' });
});

test('provider preparation fingerprints do not claim a content digest and ready resume does no import', async t => {
  const f = await fixture(t);
  const first = await prepareSource(f.client, { kind: 'import', url: 'https://example.test/interview' }, f.project, { timeoutMs: 10000 });
  assert.equal(first.fingerprint.kind, 'provider'); assert.equal(first.fingerprint.sha256, undefined);
  const second = await resumeSource(new CutmyClient({ baseUrl: f.baseUrl }), f.project, { timeoutMs: 10000 });
  assert.equal(first.source_id, second.source_id); assert.equal(f.posts.length, 1);
});

test('cancelling a real stalled upload returns promptly and retains its uncertain intent', async t => {
  const f = await fixture(t); await writeFile(f.path, Buffer.alloc(8 << 20));
  const controller = new AbortController(); let stopped;
  f.config.blockUpload = true;
  f.config.uploadStarted = () => setTimeout(() => { stopped = performance.now(); controller.abort(); }, 150);
  await assert.rejects(prepareSource(f.client, { kind: 'upload', path: f.path }, f.project, { signal: controller.signal, timeoutMs: 5000 }), error => error.code === 'source_preparation_cancelled');
  assert.equal(typeof stopped, 'number'); assert(performance.now() - stopped < 1000);
  assert.equal(JSON.parse(await readFile(join(f.project, 'state.json'))).uncertain, true);
});
