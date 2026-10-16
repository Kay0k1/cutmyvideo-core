import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { fileURLToPath, pathToFileURL } from 'node:url';
import test from 'node:test';

const { CutmyClient, ApiError } = await import(process.env.CUTMY_SDK_JS_ENTRY ? pathToFileURL(process.env.CUTMY_SDK_JS_ENTRY).href : '../dist/index.js');
const { LocalCutmy, LocalError } = await import(process.env.CUTMY_SDK_LOCAL_JS_ENTRY ? pathToFileURL(process.env.CUTMY_SDK_LOCAL_JS_ENTRY).href : '../dist/local.js');
const request = (text = '1\n00:00:01,000 --> 00:00:02,000\nПривет $(literal)\n', count = 1) => ({ version: 1, format: 'srt', text, outputs: Array.from({ length: count }, (_, i) => ({ clip_id: 'clip' + i, segments: [{ index: 0, label: '', source_start_ms: 1000, source_end_ms: 2000, output_start_ms: 0, output_end_ms: 1000 }] })) });
const hash = text => createHash('sha256').update(text).digest('hex');

test('local subtitle wrapper passes a private literal request and handles results larger than the ordinary JSON cap', async () => {
  const local = new LocalCutmy({ binary: process.execPath, binaryArgs: [fileURLToPath(new URL('./fake-cli.mjs', import.meta.url))] });
  for (const body of [request(), request('x'.repeat(450000), 3)]) {
    const result = await local.retimeSubtitles(body, { timeoutMs: 5000 });
    assert.deepEqual(result.outputs.map(item => item.clip_id), body.outputs.map(item => item.clip_id));
    for (const item of result.outputs) { assert.equal(item.text, body.text); assert.equal(item.sha256, hash(item.text)); }
  }
  const bad = new LocalCutmy({ binary: process.execPath, binaryArgs: [fileURLToPath(new URL('./fake-cli.mjs', import.meta.url))], env: { SDK_BAD_SUBTITLE_HASH: '1' } });
  await assert.rejects(bad.retimeSubtitles(request()), error => error instanceof LocalError && error.code === 'invalid_response');
});

test('HTTP subtitle wrapper verifies hashes/identities and strips unknown fields from bounded large replies', async t => {
  const config = {}, received = [], cookie = 'c'.repeat(43);
  const limits = { max_source_bytes: 1, max_output_bytes: 1, max_fetch_bytes: 1, max_ranges: 128, max_range_ms: 1000, max_job_ms: 1000 };
  const server = createServer(async (req, res) => {
    res.setHeader('content-type', 'application/json');
    if (req.url === '/api/v1/session') { res.setHeader('set-cookie', 'cutmy_session=' + cookie); return res.end(JSON.stringify({ ok: true, limits })); }
    assert.equal(req.headers.cookie, 'cutmy_session=' + cookie);
    const chunks = []; for await (const chunk of req) chunks.push(chunk); const body = JSON.parse(Buffer.concat(chunks)); received.push(body);
    res.end(JSON.stringify({ version: 1, unknown_secret: 'private-do-not-return', outputs: body.outputs.map(item => ({ clip_id: item.clip_id, format: body.format, text: body.text, cue_count: 1, sha256: config.badHash ? '0'.repeat(64) : hash(body.text), unknown_secret: 'private-do-not-return' })) }));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => { server.closeAllConnections(); return new Promise(resolve => server.close(resolve)); });
  const client = new CutmyClient({ baseUrl: 'http://127.0.0.1:' + server.address().port }), body = request('x'.repeat(450000), 3);
  const result = await client.retimeSubtitles(body);
  assert.deepEqual(received[0], body); assert(result.outputs.reduce((sum, item) => sum + item.text.length, 0) > 1 << 20);
  assert(!JSON.stringify(result).includes('private-do-not-return'));
  config.badHash = true;
  await assert.rejects(client.retimeSubtitles(request()), error => error instanceof ApiError && error.code === 'invalid_response');
});

test('unsafe IDs and oversized subtitle text are refused before network', async () => {
  const client = new CutmyClient({ baseUrl: 'http://127.0.0.1:1' });
  for (const body of [request('x'.repeat((512 << 10) + 1)), { ...request(), outputs: [{ clip_id: '../outside', segments: [{}] }] }]) await assert.rejects(client.retimeSubtitles(body), TypeError);
});
