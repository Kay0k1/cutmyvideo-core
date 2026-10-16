import { readFile, stat, writeFile } from 'node:fs/promises';
import { dirname, isAbsolute } from 'node:path';
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
const args = process.argv.slice(2);
const flag = name => args[args.indexOf(name) + 1];
const command = args[1];
const output = { path: '/published/one.mp4', size_bytes: 1, actual_start_ms: 0, actual_end_ms: 1000, segments: [], track_manifest: [] };
const collectionOutput = { ...output, id: 'answer', requested_ranges: [{ start_ms: 0, end_ms: 1000 }] };
const collectionResult = { version: 1, rendition: args.includes('--rendition') ? flag('--rendition') : 'final', outputs: [collectionOutput] };
if (args[0] === 'orphan-worker') {
  process.on('SIGTERM', () => {});
  setInterval(() => {}, 1000);
} else if (process.env.FAKE_MODE === 'orphan') {
  const worker = spawn(process.execPath, [process.argv[1], 'orphan-worker'], { stdio: 'ignore', detached: false });
  await writeFile(process.env.FAKE_ORPHAN_PID_PATH, String(worker.pid));
  process.on('SIGTERM', () => process.exit(130));
  setInterval(() => {}, 1000);
} else if (process.env.FAKE_MODE === 'stall' || process.env.FAKE_MODE === 'force') {
  if (process.env.FAKE_PID_PATH) await writeFile(process.env.FAKE_PID_PATH, String(process.pid));
  if (process.env.FAKE_MODE === 'stall') process.on('SIGTERM', () => {
    if (command === 'batch') console.log(JSON.stringify({ outputs: [output], incomplete: true }));
    if (command === 'collection') console.log(JSON.stringify({ ...collectionResult, incomplete: true }));
    console.error(JSON.stringify({ error: { code: 'cancelled', message: 'Cancelled' } }));
    process.exit(130);
  });
  else process.on('SIGTERM', () => {});
  setInterval(() => {}, 1000);
} else if (process.env.FAKE_MODE === 'oversize') {
  process.stdout.write('x'.repeat((1 << 20) + 1));
  setInterval(() => {}, 1000);
} else if (process.env.FAKE_MODE === 'invalid') {
  console.log('private invalid diagnostic');
} else if (process.env.FAKE_MODE === 'fail') {
  console.log(JSON.stringify(command === 'collection' ? { ...collectionResult, incomplete: true } : { outputs: [output], incomplete: true }));
  console.error(JSON.stringify({ error: { code: 'future_local_error', message: 'Public failure' } }));
  process.exitCode = 1;
} else if (command === 'subtitles') {
  const path = flag('--request'), request = JSON.parse(await readFile(path, 'utf8'));
  if (process.platform !== 'win32' && (((await stat(path)).mode & 0o777) !== 0o600 || ((await stat(dirname(path))).mode & 0o777) !== 0o700)) throw new Error('Unsafe subtitle request');
  const text = request.text;
  console.log(JSON.stringify({ version: 1, outputs: request.outputs.map(output => ({ clip_id: output.clip_id, format: request.output_format ?? request.format, text, cue_count: 1, sha256: process.env.SDK_BAD_SUBTITLE_HASH ? '0'.repeat(64) : createHash('sha256').update(text).digest('hex') })) }));
} else if (command === 'inspect') {
  console.log(JSON.stringify({ duration_ms: 5000, tracks: [{ index: 0, type: 'video', codec: 'h264', default: false }], input: flag('--input') }));
} else {
  const planPath = flag('--plan');
  const plan = JSON.parse(await readFile(planPath, 'utf8'));
  if (!isAbsolute(plan.input) || (process.platform !== 'win32' && (((await stat(planPath)).mode & 0o777) !== 0o600 || ((await stat(dirname(planPath))).mode & 0o777) !== 0o700))) throw new Error('Unsafe temporary plan');
  if (command === 'collection') {
    if (args[2] === 'export') console.log(JSON.stringify(collectionResult));
    else console.log(JSON.stringify({ version: 1, valid: true, rendition: collectionResult.rendition, source: { duration_ms: 5000 }, selected_clip_ids: flag('--clips').split(','), clips: plan.clips.map(clip => ({ id: clip.id, output_path: flag('--output'), request: { ranges: clip.ranges }, checks: [] })), input: plan.input, plan_path: planPath }));
  }
  else if (command === 'batch') console.log(JSON.stringify({ outputs: [output] }));
  else console.log(JSON.stringify({ version: 1, valid: true, source: { duration_ms: 5000 }, request: { ranges: plan.ranges }, requested_duration_ms: 1000, output_count: 1, track_manifest: [], checks: [{ name: 'request', status: 'passed' }], warnings: [], output_paths: [flag('--output')], input: plan.input, plan_path: planPath }));
}
