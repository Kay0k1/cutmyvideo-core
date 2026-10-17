// Import only the installed distribution entries selected by the test producer.
import assert from 'node:assert/strict';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
const { CutmyClient, readyArtifacts } = await import(pathToFileURL(process.env.CUTMY_SDK_JS_ENTRY).href);
const { LocalCutmy, uploadFile, downloadArtifactToFile } = await import(pathToFileURL(process.env.CUTMY_SDK_LOCAL_JS_ENTRY).href);
const [mode, input, replacement, output, baseUrl] = process.argv.slice(2);
const client = mode === 'local' ? new LocalCutmy({binary: process.env.CUTMY_SDK_BINARY, threads: 1}) : new CutmyClient({baseUrl, requestTimeoutMs: 30000});
const sourceId = mode === 'local' ? input : (await uploadFile(client, input)).id;
const replacementId = mode === 'local' ? replacement : (await uploadFile(client, replacement)).id;
const silence = await client.analyzeSilence(sourceId, {threshold_db: -45, min_silence_ms: 200});
const video = await client.analyzeVideo(sourceId, {scene_threshold: .01, min_black_ms: 200});
const loudness = await client.analyzeLoudness(sourceId);
const cut = await client.importCutList({format: 'csv', text: '0,1,Привет\n2,,Ending\n'});
assert.equal(cut.entries[1].end_ms, null); assert.equal(cut.entries[0].label, 'Привет');
const serialized = await client.exportCutList({format: 'csv', cut_list: cut});
assert.deepEqual(await client.importCutList({format: 'csv', text: serialized.text}), cut);
const resolved = {version: 1, entries: [{start_ms: 0, end_ms: 1000, label: 'Привет'}, {start_ms: 2000, end_ms: 4000, label: 'Ending'}]};
const chapters = await client.exportCutList({format: 'ffmetadata', cut_list: resolved});
assert.deepEqual(await client.importCutList({format: 'ffmetadata', text: chapters.text}), resolved);
if (mode === 'local') {
  const plan = await client.planCutList({format: 'csv', text: serialized.text}, {media: input, durationMs: 4000});
  assert.equal(plan.ranges[1].end_ms, 4000);
  assert.ok((await client.silencePlan(input, {threshold_db: -45, min_silence_ms: 200})).ranges.length);
}
const settings = [
  ['normalize', {audio_normalize: {integrated_lufs: -16, true_peak_dbtp: -1, loudness_range_lu: 11}}],
  ['canvas', {canvas: {width: 96, height: 160, fit: 'contain', background: '#0000ff'}}],
  ['replace', {audio_replace: {...(mode === 'local' ? {input: replacementId} : {source_id: replacementId}), offset_ms: 1000}}],
];
const outputs = [];
for (const [name, edit] of settings) {
  const options = {format: 'mp4', quality: 'best', cut_mode: 'accurate', ...edit};
  const ranges = [{start_ms: 0, end_ms: 4000, label: 'Whole'}];
  let path = join(output, `${name}.mp4`), artifact;
  if (mode === 'local') {
    const plan = {version: 1, input, ranges, output_mode: 'joined', options};
    assert.equal((await client.preflight(plan, path, {timeoutMs: 30000})).valid, true);
    artifact = (await client.executePlan(plan, path, {timeoutMs: 45000})).outputs[0];
    path = artifact.path;
  } else {
    const request = {source_id: sourceId, ranges, output_mode: 'joined', ...options};
    assert.equal((await client.preflight(request)).valid, true);
    const job = await client.createJob(request, {idempotencyKey: `media-typescript-${name}`});
    artifact = readyArtifacts(await client.waitForJob(job.id, {timeoutMs: 45000, pollIntervalMs: 100}))[0];
    await downloadArtifactToFile(client, artifact.id, path, {maxBytes: 16 << 20});
  }
  outputs.push({name, path, artifact});
}
const segmentText = await client.exportCutList({format: 'csv', segments: outputs[0].artifact.segments});
assert.equal((await client.importCutList({format: 'csv', text: segmentText.text})).entries[0].end_ms, 4000);
process.stdout.write(JSON.stringify({mode, silence, video, loudness, outputs}) + '\n');
