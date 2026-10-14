// Imports the installed package entries selected by CI, not the SDK source tree.
import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const { CutmyClient, LocalError, readyArtifacts } = await import(pathToFileURL(process.env.CUTMY_SDK_JS_ENTRY).href);
const { LocalCutmy, uploadFile, downloadArtifactToFile } = await import(pathToFileURL(process.env.CUTMY_SDK_LOCAL_JS_ENTRY).href);
const [mode, input, output, baseUrl] = process.argv.slice(2);
const ranges = [{ start_ms: 500, end_ms: 1500, label: "First scene" },
  { start_ms: 2000, end_ms: 3000, label: "Second scene" }];
const options = { format: "mp4", quality: "best", cut_mode: "accurate" };
let report;
if (mode === "local") {
  const client = new LocalCutmy({ binary: process.env.CUTMY_SDK_BINARY, threads: 1 });
  const inspected = await client.inspect(input, { timeoutMs: 20_000 });
  assert.equal(inspected.width, 160);
  assert.equal(inspected.height, 96);
  assert.equal(inspected.tracks.length, 3);
  const plan = { version: 1, input, ranges, options };
  const preflight = await client.preflight(plan, output, { timeoutMs: 20_000 });
  assert.deepEqual(await readdir(output), [], "Preflight wrote an export or workspace");
  const result = await client.executePlan(plan, output, { timeoutMs: 30_000 });
  assert.equal(result.outputs.length, 2);
  const first = result.outputs[0].path;
  const preserved = await readFile(first);
  await assert.rejects(client.preflight(plan, output, { timeoutMs: 20_000 }), error => error instanceof LocalError && error.code === "output_exists");
  assert.deepEqual(await readFile(first), preserved, "Preflight changed an existing output");
  report = { mode, preflight, predicted_paths: preflight.output_paths,
    outputs: result.outputs.map(item => ({ path: item.path, artifact: item })) };
} else if (mode === "http") {
  const client = new CutmyClient({ baseUrl, requestTimeoutMs: 20_000, transferTimeoutMs: 20_000 });
  const source = await uploadFile(client, input);
  const request = { source_id: source.id, ranges, ...options, output_mode: "joined" };
  const preflight = await client.preflight(request);
  const idempotencyKey = "installed-typescript-consumer";
  const job = await client.createJob(request, { idempotencyKey });
  const replay = await client.createJob(request, { idempotencyKey });
  assert.equal(job.id, replay.id, "Submission retry created a different job");
  const done = await client.waitForJob(job.id, { timeoutMs: 30_000, pollIntervalMs: 1000 });
  const artifacts = readyArtifacts(done);
  assert.equal(artifacts.length, 1);
  const outputs = [];
  for (const [index, artifact] of artifacts.entries()) {
    const path = await downloadArtifactToFile(client, artifact.id, join(output, `download-${index}.mp4`), { maxBytes: 16 << 20 });
    outputs.push({ path, artifact });
  }
  report = { mode, source_id: source.id, preflight, job_id: job.id, replay_id: replay.id, job: done, outputs };
} else { throw new TypeError("Use local or http"); }
process.stdout.write(JSON.stringify(report) + "\n");
