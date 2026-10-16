import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { pathToFileURL } from 'node:url';
import test from 'node:test';

const { ApiError, CutmyClient, JobError, readyArtifacts } = await import(process.env.CUTMY_SDK_JS_ENTRY ? pathToFileURL(process.env.CUTMY_SDK_JS_ENTRY).href : '../dist/index.js');
const cookie = 'a'.repeat(43);
const limits = { max_source_bytes: 1000000, max_output_bytes: 1000000, max_fetch_bytes: 1000000, max_ranges: 128, max_range_ms: 100000, max_job_ms: 100000 };
const source = { id: 'source-1', duration_ms: 5000, title: 'Fixture', kind: 'upload', provider: 'upload', provider_video_id: null, source_url: null, preview_kind: 'native', preview_url: null, embed_url: null, thumbnail_url: null, expires_at: null, retention_blocked: false };
const artifact = { id: 'artifact-1', filename: 'one.mp4', size_bytes: 6, download_url: 'https://foreign.invalid/secret', actual_start_ms: 0, actual_end_ms: 1000, expires_at: null };
const request = { source_id: source.id, ranges: [{ start_ms: 0, end_ms: 1000 }], format: 'mp4', quality: 'best', cut_mode: 'accurate' };
function job(status = 'succeeded', extra = {}) { return { id: 'job-1', status, stage: status, message: '', items: [{ id: 'item-1', label: '', start_ms: 0, end_ms: 1000, status: 'succeeded', artifact }], storage_wait_until: null, ...extra }; }
function json(res, value, status = 200) { res.writeHead(status, { 'content-type': 'application/json' }); res.end(JSON.stringify(value)); }
async function fixture(t, route) {
  const calls = [];
  const server = createServer(async (req, res) => {
    calls.push({ method: req.method, url: req.url, cookie: req.headers.cookie, key: req.headers['idempotency-key'], origin: req.headers.origin });
    if (req.url === '/api/v1/session') { res.setHeader('Set-Cookie', [`cutmy_session=${cookie}; Path=/; HttpOnly`, 'ignored=private; Path=/']); json(res, { ok: true, limits }); return; }
    try { await route(req, res, calls); } catch (e) { json(res, { error: { code: 'fixture', message: e.message } }, 500); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => { server.closeAllConnections(); return new Promise(resolve => server.close(resolve)); });
  const baseUrl = `http://127.0.0.1:${server.address().port}`;
  return { client: new CutmyClient({ baseUrl }), calls, baseUrl };
}
async function body(req) { const chunks = []; for await (const chunk of req) chunks.push(chunk); return Buffer.concat(chunks).toString(); }
async function consume(stream) { const reader = stream.getReader(); const chunks = []; try { while (true) { const next = await reader.read(); if (next.done) break; chunks.push(Buffer.from(next.value)); } } finally { reader.releaseLock(); } return Buffer.concat(chunks); }

test('owner session initialization coalesces and all calls stay on the protected origin', async t => {
  const { client, calls, baseUrl } = await fixture(t, (req, res) => {
    assert.equal(req.headers.cookie, `cutmy_session=${cookie}`);
    assert.equal(req.headers.origin, undefined, 'Node must not forge Origin from an internal service address');
    if (req.url === '/api/v1/sources') json(res, { sources: [source], future: 1 });
    else if (req.url === '/api/v1/sources/source-1/tracks') json(res, { tracks: [{ index: 0, type: 'video', codec: 'h264', default: false }] });
    else json(res, source);
  });
  await Promise.all([client.getSource(source.id), client.getTracks(source.id), client.listSources()]);
  assert.equal(calls.filter(x => x.url === '/api/v1/session').length, 1);
  assert.equal((await client.initializeSession()).limits.max_ranges, 128);
  assert(!JSON.stringify(client).includes(cookie));
});

test('preflight preserves normalized defaults, warnings and future fields', async t => {
  const { client } = await fixture(t, async (req, res) => {
    assert.deepEqual(JSON.parse(await body(req)), request);
    json(res, { version: 1, valid: true, source: { duration_ms: 5000 }, request: { ...request, output_mode: '', subtitle_mode: '' }, requested_duration_ms: 1000, output_count: 1, track_manifest: [], checks: [{ name: 'future_check', status: 'future_status' }], warnings: [{ code: 'future_warning', message: 'Deferred' }], future: { retained: true } });
  });
  const report = await client.preflight(request);
  assert.equal(report.request.output_mode, '');
  assert.equal(report.checks[0].status, 'future_status');
  assert.equal(report.future.retained, true);
});

test('uncertain job submission is not retried and retains the explicit recovery key', async t => {
  const { client, calls } = await fixture(t, async (req, res) => {
    assert.deepEqual(JSON.parse(await body(req)), request);
    json(res, { error: { code: 'database_unavailable', message: 'Unavailable' } }, 503);
  });
  await assert.rejects(client.createJob(request, { idempotencyKey: 'saved-before-submission' }), e => e instanceof ApiError && e.code === 'database_unavailable' && e.status === 503 && e.idempotencyKey === 'saved-before-submission');
  assert.equal(calls.filter(x => x.url === '/api/v1/jobs').length, 1);
  assert.equal(calls.at(-1).key, 'saved-before-submission');
  assert.throws(() => client.createJob(request, {}), /idempotencyKey/);
  assert.throws(() => client.createJob(request, { idempotencyKey: 'é'.repeat(65) }), /128/);
});

test('connection failure during submission retains recovery metadata without repeating mutation', async t => {
  const { client, calls } = await fixture(t, (req, _res) => req.socket.destroy());
  await assert.rejects(client.createJob(request, { idempotencyKey: 'network-uncertain' }), e => e.code === 'network_error' && e.idempotencyKey === 'network-uncertain');
  assert.equal(calls.filter(x => x.url === '/api/v1/jobs').length, 1);
});

test('Retry-After is exposed without retrying rate-limited mutations', async t => {
  const { client, calls } = await fixture(t, (_req, res) => { res.setHeader('Retry-After', '45'); json(res, { error: { code: 'rate_limited', message: 'Wait' } }, 429); });
  await assert.rejects(client.createJob(request, { idempotencyKey: 'limited-export' }), e => e.code === 'rate_limited' && e.status === 429 && e.retryAfterSeconds === 45 && e.idempotencyKey === 'limited-export');
  assert.equal(calls.filter(x => x.url === '/api/v1/jobs').length, 1);
});

test('Unicode idempotency keys use UTF-8 wire bytes and controls are rejected', async t => {
  const key = 'монтаж-🎬';
  const { client, calls } = await fixture(t, (_req, res) => json(res, job(), 202));
  await client.createJob(request, { idempotencyKey: key });
  assert.equal(Buffer.from(calls.at(-1).key, 'latin1').toString('utf8'), key);
  assert.throws(() => client.createJob(request, { idempotencyKey: 'bad\tkey' }), /control/);
});

test('upload uses multipart and import errors are never automatically retried', async t => {
  const { client, calls } = await fixture(t, async (req, res) => {
    const data = await body(req);
    if (req.url === '/api/v1/uploads') { assert.match(req.headers['content-type'], /^multipart\/form-data; boundary=/); assert.match(data, /filename="clip.mov"/); assert.match(data, /streamed upload/); json(res, source, 201); }
    else { assert.deepEqual(JSON.parse(data), { url: 'https://example.test/video.mp4' }); json(res, { error: { code: 'source_timeout', message: 'Timeout' } }, 504); }
  });
  assert.equal((await client.upload(new Blob(['streamed upload']), 'clip.mov')).id, source.id);
  await assert.rejects(client.importSource('https://example.test/video.mp4'), e => e.code === 'source_timeout');
  assert.equal(calls.filter(x => x.url === '/api/v1/sources').length, 1);
});

test('failed and cancelled jobs retain partial ready artifacts and public item diagnostics', async t => {
  const { client } = await fixture(t, (_req, res) => json(res, job('failed', { items: [...job().items, { id: 'failed', label: '', start_ms: 1000, end_ms: 2000, status: 'failed', artifact: null, error_code: 'future_error' }] })));
  await assert.rejects(client.waitForJob('job-1'), e => e instanceof JobError && e.jobId === 'job-1' && e.artifacts[0].id === artifact.id && e.job.items[1].error_code === 'future_error');
  assert.deepEqual(readyArtifacts(job('cancelled')), [artifact]);
  assert.deepEqual(readyArtifacts(job('running', { items: [{ ...job().items[0], status: 'running' }] })), []);
});

test('waiting timeout and caller cancellation do not request server job cancellation', async t => {
  const { client, calls } = await fixture(t, (_req, res) => json(res, job('future_running_state')));
  await assert.rejects(client.waitForJob('job-1', { timeoutMs: 50 }), e => e.code === 'timeout' && e.jobId === 'job-1');
  const controller = new AbortController();
  await assert.rejects(client.waitForJob('job-1', { signal: controller.signal, onProgress: () => controller.abort() }), e => e.code === 'cancelled' && e.jobId === 'job-1');
  assert.equal(calls.filter(x => x.url?.endsWith('/cancel')).length, 0);
  await assert.rejects(client.waitForJob('job-1', { pollIntervalMs: 1 }), /at least 1000/);
});

test('successful wait returns the full job, progress callback and explicit cancel are supported', async t => {
  const { client, calls } = await fixture(t, (_req, res) => json(res, job()));
  let progress;
  const result = await client.waitForJob('job-1', { onProgress: value => { progress = value; } });
  assert.equal(result, progress);
  assert.equal((await client.cancelJob('job-1')).id, result.id);
  assert.equal(calls.at(-1).method, 'POST');
  assert.equal(calls.at(-1).url, '/api/v1/jobs/job-1/cancel');
});

test('redirects are refused before sending any owner cookie to the target', async t => {
  let leaked = 0;
  const foreign = createServer((_req, res) => { leaked++; json(res, source); });
  await new Promise(resolve => foreign.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => foreign.close(resolve)));
  const { client } = await fixture(t, (_req, res) => { res.writeHead(302, { location: `http://127.0.0.1:${foreign.address().port}/stolen` }); res.end(); });
  await assert.rejects(client.getSource('x'), e => e.code === 'redirect_refused');
  await assert.rejects(client.downloadArtifact('x'), e => e.code === 'redirect_refused');
  assert.equal(leaked, 0);
});

test('invalid JSON, oversized JSON, unexpected shapes and unknown codes are typed', async t => {
  let mode = 'json';
  const { client } = await fixture(t, (_req, res) => {
    if (mode === 'json') { res.writeHead(200, { 'content-type': 'application/json' }); res.end('private diagnostic is not JSON'); }
    else if (mode === 'size') { res.writeHead(200); res.end('x'.repeat((1 << 20) + 1)); }
    else if (mode === 'shape') json(res, { id: 42 });
    else json(res, { error: { code: 'future_code', message: 'Public message' } }, 499);
  });
  await assert.rejects(client.getSource('x'), e => e.code === 'invalid_response' && e.status === 200 && !e.message.includes('private'));
  mode = 'size'; await assert.rejects(client.getSource('x'), e => e.code === 'response_too_large');
  mode = 'shape'; await assert.rejects(client.getSource('x'), e => e.code === 'invalid_response');
  mode = 'future'; await assert.rejects(client.getJob('job-1'), e => e.code === 'future_code' && e.status === 499 && e.jobId === 'job-1');
});

test('stalled JSON request body is bounded by deadline and cancellation', async t => {
  const { client } = await fixture(t, (_req, res) => { res.writeHead(200, { 'content-type': 'application/json' }); res.write('{'); });
  await assert.rejects(client.getSource('x', { timeoutMs: 30 }), e => e.code === 'timeout');
  const signal = AbortSignal.timeout(30);
  await assert.rejects(client.getSource('x', { signal }), e => e.code === 'cancelled');
});

test('artifact downloads stream through the protected ID route and enforce actual byte limits', async t => {
  const { client, calls } = await fixture(t, (_req, res) => { res.writeHead(200, { 'content-type': 'video/mp4' }); res.write('abc'); res.end('def'); });
  const download = await client.downloadArtifact(artifact.id, { maxBytes: 6 });
  assert.equal(download.contentType, 'video/mp4');
  assert.equal((await consume(download.stream)).toString(), 'abcdef');
  assert.equal(calls.at(-1).url, '/api/v1/artifacts/artifact-1/download');
  const oversized = await client.downloadArtifact(artifact.id, { maxBytes: 5 });
  await assert.rejects(consume(oversized.stream), e => e.code === 'download_too_large');
});

test('download rejects advertised oversize, times out stalled bodies and cleans unconsumed streams', async t => {
  const { client } = await fixture(t, (_req, res) => {
    res.writeHead(200, { 'content-length': '10000' }); res.end('x');
  });
  await assert.rejects(client.downloadArtifact('x', { maxBytes: 10 }), e => e.code === 'download_too_large');

  // Keep the real 40ms total budget, without assuming TCP headers arrive first.
  // The controlled fetch resolves immediately; only its body remains unfinished.
  const transfers = [];
  const streaming = new CutmyClient({ baseUrl: 'https://example.test', fetch: async url => {
    if (String(url).endsWith('/session')) return Response.json({ ok: true, limits });
    assert.equal(new URL(url).pathname, '/api/v1/artifacts/x/download');
    let observeCancellation;
    const transfer = { cancelled: new Promise(resolve => { observeCancellation = resolve; }), cancelCalls: 0 };
    transfer.body = new ReadableStream({
      start(controller) { controller.enqueue(new TextEncoder().encode('x')); },
      cancel() { transfer.cancelCalls++; observeCancellation(); },
    });
    transfers.push(transfer);
    return new Response(transfer.body);
  } });
  async function cancellationComplete(transfer) {
    let guard;
    try {
      await Promise.race([transfer.cancelled, new Promise((_, reject) => {
        guard = setTimeout(() => reject(new Error('underlying stream was not cancelled')), 5000);
      })]);
      // Drain the cancellation/finally microtasks before inspecting reader cleanup.
      await new Promise(resolve => setImmediate(resolve));
      assert.equal(transfer.cancelCalls, 1);
      assert.equal(transfer.body.locked, false);
    } finally { clearTimeout(guard); }
  }
  const download = await streaming.downloadArtifact('x', { timeoutMs: 40 });
  await assert.rejects(consume(download.stream), e => e.code === 'timeout');
  await cancellationComplete(transfers[0]);
  const unconsumed = await streaming.downloadArtifact('x', { timeoutMs: 40 });
  await cancellationComplete(transfers[1]);
  await assert.rejects(consume(unconsumed.stream), e => e.code === 'timeout');
  const cancelled = await streaming.downloadArtifact('x');
  await cancelled.cancel();
  await cancellationComplete(transfers[2]);
  assert.equal(transfers.length, 3);
});

test('explicit Node capability resumption is validated and never serialized', async t => {
  const { baseUrl, client: initial, calls } = await fixture(t, (req, res) => { assert.equal(req.headers.cookie, `cutmy_session=${cookie}`); json(res, source); });
  assert.throws(() => initial.exportSessionCookie(), e => e.code === 'session_unavailable');
  await initial.initializeSession();
  assert.equal(initial.exportSessionCookie(), cookie);
  const client = new CutmyClient({ baseUrl, sessionCookie: initial.exportSessionCookie() });
  await client.getSource('source-1');
  assert.equal(calls.filter(x => x.url === '/api/v1/session').at(-1).cookie, `cutmy_session=${cookie}`);
  assert.equal(JSON.stringify(client), '{}');
  assert.throws(() => new CutmyClient({ baseUrl, sessionCookie: `${cookie}; evil=x` }), /sessionCookie/);
  assert.throws(() => new CutmyClient({ baseUrl: 'https://user:password@example.test' }), /credentials/);
});

test('downloads reject partial success and inconsistent advertised lengths', async () => {
  let partial = true;
  const client = new CutmyClient({ baseUrl: 'https://example.test', fetch: async url => {
    if (String(url).endsWith('/session')) return Response.json({ ok: true, limits });
    return new Response('abc', { status: partial ? 206 : 200, headers: { 'content-length': '4' } });
  } });
  await assert.rejects(client.downloadArtifact('x'), e => e.code === 'invalid_response' && e.status === 206);
  partial = false;
  const download = await client.downloadArtifact('x');
  await assert.rejects(consume(download.stream), e => e.code === 'invalid_response');
});
