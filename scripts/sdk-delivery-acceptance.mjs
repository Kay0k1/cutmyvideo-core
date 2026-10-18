import assert from 'node:assert/strict';
import { join } from 'node:path';
import { mkdir } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';
const { CutmyClient, readyArtifacts, exportPortableProject, importPortableProject } = await import(pathToFileURL(process.env.CUTMY_SDK_JS_ENTRY).href);
const { LocalCutmy, uploadFile, downloadArtifactToFile, downloadImageArtifactToFile } = await import(pathToFileURL(process.env.CUTMY_SDK_LOCAL_JS_ENTRY).href);
const { runCollection, resumeCollection } = await import(process.env.CUTMY_SDK_COLLECTION_JS_ENTRY ? pathToFileURL(process.env.CUTMY_SDK_COLLECTION_JS_ENTRY).href : new URL('./collections.js', pathToFileURL(process.env.CUTMY_SDK_JS_ENTRY)).href);
const [mode, input, _replacement, output, baseUrl] = process.argv.slice(2), local = mode === 'local';
const client = local ? new LocalCutmy({ binary: process.env.CUTMY_SDK_BINARY, threads: 1 }) : new CutmyClient({ baseUrl, requestTimeoutMs: 30000 });
const sourceId = local ? input : (await uploadFile(client, input)).id;
const speed = { numerator: 5, denominator: 4 }, target = 262144;
const outputRanges = [{ start_ms: 2000, end_ms: 4000, label: 'Blue' }, { start_ms: 0, end_ms: 1000, label: 'Red' }, { start_ms: 2000, end_ms: 2500, label: 'Blue again' }];
const outputs = [], canonical = [];
for (const name of ['wav','flac','m4a','speed-target']) {
  const format = name === 'speed-target' ? 'mp4' : name;
  const options = { format, quality: 'best', cut_mode: name === 'm4a' ? 'copy' : 'accurate', ...(name === 'speed-target' ? { speed, target_size_bytes: target } : {}) };
  const ranges = name === 'speed-target' ? outputRanges : [{ start_ms: 0, end_ms: 4000, label: 'Audio' }];
  const path = join(output, `${name}.${format}`), original = structuredClone(options);
  let validation, artifact;
  if (local) {
    const plan = { version: 1, input, ranges, output_mode: 'joined', options };
    validation = await client.preflight(plan, path);
    artifact = (await client.executePlan(plan, path, { timeoutMs: 90000 })).outputs[0];
  } else {
    const request = { source_id: sourceId, ranges, output_mode: 'joined', ...options };
    validation = await client.preflight(request);
    const job = await client.createJob(request, { idempotencyKey: `delivery-typescript-${name}` });
    assert.equal((await client.createJob(request, { idempotencyKey: `delivery-typescript-${name}` })).id, job.id);
    artifact = readyArtifacts(await client.waitForJob(job.id, { timeoutMs: 90000 }))[0];
    await downloadArtifactToFile(client, artifact.id, path, { maxBytes: 4 << 20 });
  }
  assert.deepEqual(options, original);
  if (name === 'speed-target') { assert.deepEqual(validation.request.speed, speed); assert.equal(validation.request.target_size_bytes, target); }
  canonical.push(validation.request); outputs.push({ name, path, artifact });
}
const finished = outputs.at(-1);
const sidecar = await client.retimeSubtitles({ version: 2, format: 'srt', text: '1\n00:00:00,250 --> 00:00:00,750\nRed cue\n\n', outputs: [{ clip_id: 'speed', segments: finished.artifact.segments }] });
assert.equal(sidecar.version, 1); assert.equal(sidecar.outputs[0].text, '1\n00:00:01,800 --> 00:00:02,200\nRed cue\n\n');
const posterOptions = { timestamp_ms: 2000, format: 'png' }, posterPath = join(output, 'poster.png');
let poster;
if (local) poster = await client.generatePoster(finished.path, posterPath, posterOptions);
else {
  const parent = finished.artifact.id; poster = await client.createPoster(parent, posterOptions);
  assert.equal((await client.createPoster(parent, posterOptions)).id, poster.id);
  assert.equal((await client.getImageArtifact(poster.id)).parent_artifact_id, parent);
  await downloadImageArtifactToFile(client, poster.id, posterPath, { maxBytes: 16 << 20 });
  await client.deleteImageArtifact(poster.id);
  await assert.rejects(client.getImageArtifact(poster.id), e => e.status === 404);
}
const chapters = await client.exportCutList({ format: 'ffmetadata', segments: finished.artifact.segments });
assert(chapters.text.includes('START=1600') && chapters.text.includes('START=2400') && chapters.text.includes('END=2800'));
const plan = { version: 1, options: { format: 'mp4', quality: 'best', cut_mode: 'accurate', speed, target_size_bytes: target }, clips: [{ id: 'inherit', ranges: [{ start_ms: 2000, end_ms: 4000 }] }, { id: 'reset', ranges: [{ start_ms: 0, end_ms: 1000 }], options: { clear_speed: true, clear_target_size: true } }] };
const request = { source_id: local ? 'owned' : sourceId, plan }, source = { id: request.source_id, duration_ms: 4000 };
assert.deepEqual(importPortableProject(exportPortableProject(request, source), source), request);
let collection;
if (local) { await mkdir(join(output,'collection'),{mode:0o700}); collection = (await client.executeCollection({ ...plan, input }, join(output,'collection'), { timeoutMs: 90000 })).outputs.map(row => ({ name: row.id, path: row.path, artifact: row })); }
else {
  const validation = await client.validateCollection(request), [inherited, reset] = validation.clips.map(row => row.preflight.request);
  assert.deepEqual(inherited.speed, speed); assert.equal(inherited.target_size_bytes, target); assert(!reset.speed && !reset.target_size_bytes);
  const report = await runCollection(client, request, join(output,'collection'), { idempotencyKey: 'delivery-typescript-collection', timeoutMs: 120000 });
  assert(report.complete); collection = report.outputs.map(row => ({ name: row.clip_id, path: row.path, artifact: { segments: row.segments } }));
  const audio = { source_id: sourceId, plan: { version: 1, options: { format: 'wav', quality: 'best', cut_mode: 'accurate' }, clips: [{ id: 'audio', ranges: [{ start_ms: 0, end_ms: 1000 }] }] } };
  const audioReport = await runCollection(client, audio, join(output,'audio-collection'), { idempotencyKey: 'delivery-typescript-wav', timeoutMs: 90000 });
  assert(audioReport.complete && audioReport.outputs[0].path.endsWith('.wav'));
  assert((await resumeCollection(client, join(output,'audio-collection'), { timeoutMs: 30000 })).complete);
}
process.stdout.write(JSON.stringify({ mode, outputs, poster: { path: posterPath, artifact: poster }, chapters: chapters.text, canonical, collection, portable_roundtrip: true, affine_sidecar: true }) + '\n');
