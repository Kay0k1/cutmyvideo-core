import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { mkdtemp, readFile, writeFile, rm, unlink, stat } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { pathToFileURL } from 'node:url';
import test from 'node:test';

const { CutmyClient, SourcePreparationError } = await import(process.env.CUTMY_SDK_JS_ENTRY ? pathToFileURL(process.env.CUTMY_SDK_JS_ENTRY).href : '../dist/index.js');
const { prepareSource, resumeSource } = await import(process.env.CUTMY_SDK_SOURCE_JS_ENTRY ? pathToFileURL(process.env.CUTMY_SDK_SOURCE_JS_ENTRY).href : '../dist/sources.js');
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
