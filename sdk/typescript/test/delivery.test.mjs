import assert from 'node:assert/strict';
import { pathToFileURL } from 'node:url';
import test from 'node:test';
const { CutmyClient, exportPortableProject, importPortableProject } = await import(process.env.CUTMY_SDK_JS_ENTRY ? pathToFileURL(process.env.CUTMY_SDK_JS_ENTRY).href : '../dist/index.js');
const limits = { max_source_bytes: 1e6, max_output_bytes: 1e6, max_fetch_bytes: 1e6, max_ranges: 128, max_range_ms: 1e6, max_job_ms: 1e6 };
const image = { id: 'img_123', parent_artifact_id: 'art_123', filename: 'poster.png', size_bytes: 3, download_url: '/api/v1/image-artifacts/img_123/download', width: 160, height: 96, timestamp_ms: 2000, expires_at: null, format: 'png', future: true };
const request = { source_id: 'owned', ranges: [{ start_ms: 0, end_ms: 3000 }], format: 'mp4', quality: 'best', cut_mode: 'accurate' };

test('delivery options reject precision loss and conflicting resets before any network call', () => {
  let calls = 0; const client = new CutmyClient({ baseUrl: 'http://127.0.0.1', fetch: async () => { calls++; throw Error('unexpected'); } });
  for (const options of [{ speed: { numerator: 1.5, denominator: 1 } }, { speed: { numerator: true, denominator: 1 } }, { speed: { numerator: 1, denominator: 3 } }, { speed: { numerator: 5, denominator: 4, secret: 1 } }, { target_size_bytes: null }, { target_size_bytes: NaN }, { target_size_bytes: Number.MAX_SAFE_INTEGER + 1 }, { target_size_bytes: -1 }, { speed: { numerator: 5, denominator: 4 }, clear_speed: true }, { target_size_bytes: 500, clear_target_size: true }]) {
    assert.throws(() => client.createJob({ ...request, ...options }, { idempotencyKey: 'one-attempt' }), TypeError);
  }
  assert.equal(calls, 0);
});

test('portable delivery controls retain rational values and explicit inheritance resets', () => {
  const source = { id: 'owned', duration_ms: 4000 };
  const body = { source_id: source.id, plan: { version: 1, options: { format: 'mp4', speed: { numerator: 5, denominator: 4 }, target_size_bytes: 262144 }, clips: [{ id: 'inherit', ranges: [{ start_ms: 0, end_ms: 1000 }] }, { id: 'clear', ranges: [{ start_ms: 0, end_ms: 1000 }], options: { format: 'wav', clear_speed: true, clear_target_size: true } }] } };
  assert.deepEqual(importPortableProject(exportPortableProject(body, source), source), body);
  const bad = structuredClone(body); bad.plan.options.speed.denominator = 0;
  assert.throws(() => exportPortableProject(bad, source));
});

test('poster methods verify parent and output clock; image downloads use only the protected ID route', async () => {
  const calls = []; let reply = image;
  const client = new CutmyClient({ baseUrl: 'http://127.0.0.1', fetch: async (url, init) => {
    const path = new URL(url).pathname; calls.push({ path, method: init.method ?? 'GET', body: init.body });
    if (path.endsWith('/session')) return Response.json({ ok: true, limits });
    if (init.method === 'DELETE') return new Response(null, { status: 204 });
    if (path.endsWith('/download')) return new Response(new Uint8Array([1,2,3]), { headers: { 'content-length': '3', 'content-type': 'image/png' } });
    return Response.json(reply);
  } });
  assert.deepEqual(await client.createPoster('art_123', { timestamp_ms: 2000, format: 'png' }), image);
  assert.deepEqual(JSON.parse(calls.at(-1).body), { timestamp_ms: 2000, format: 'png' });
  assert.deepEqual(await client.getImageArtifact('img_123'), image);
  const download = await client.downloadImageArtifact('img_123', { maxBytes: 3 });
  assert.deepEqual([...new Uint8Array(await new Response(download.stream).arrayBuffer())], [1,2,3]);
  assert.equal(calls.at(-1).path, image.download_url);
  await client.deleteImageArtifact('img_123'); assert.equal(calls.at(-1).method, 'DELETE');
  for (const mutation of [{ parent_artifact_id: 'another' }, { timestamp_ms: true }, { timestamp_ms: 1999 }, { width: 0 }, { size_bytes: 16777217 }, { download_url: 'https://foreign.invalid/private' }, { filename: '../poster.png' }, { format: 'svg' }]) {
    reply = { ...image, ...mutation };
    await assert.rejects(client.createPoster('art_123', { timestamp_ms: 2000, format: 'png' }), e => e.code === 'invalid_response');
  }
  const before = calls.length;
  for (const options of [{}, { timestamp_ms: -1 }, { timestamp_ms: true }, { timestamp_ms: 0, format: 'svg' }]) await assert.rejects(client.createPoster('art_123', options), TypeError);
  assert.equal(calls.length, before);
});


test('normalized response delivery controls cannot be silently malformed', async () => {
  let returned = {};
  const client = new CutmyClient({ baseUrl: 'http://127.0.0.1', fetch: async (url) => new URL(url).pathname.endsWith('/session') ? Response.json({ ok: true, limits }) : Response.json({ version: 1, valid: true, source: { duration_ms: 3000 }, request: { ...request, ...returned }, output_count: 1, requested_duration_ms: 3000, track_manifest: [], checks: [], warnings: [] }) });
  for (const options of [{ speed: { numerator: 5, denominator: 0 } }, { speed: true }, { target_size_bytes: null }, { target_size_bytes: 9007199254740992 }]) {
    returned = options;
    await assert.rejects(client.preflight(request), e => e.code === 'invalid_response');
  }
  returned = { speed: { numerator: 5, denominator: 4 }, target_size_bytes: 262144 };
  assert.deepEqual((await client.preflight(request)).request, { ...request, ...returned });
});
