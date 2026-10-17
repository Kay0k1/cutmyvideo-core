// Installed Node consumer for collection acceptance; never prints owner state.
import { createHash } from "node:crypto";
import { constants, createReadStream } from "node:fs";
import { link, lstat, mkdir, mkdtemp, open, readFile, readdir, realpath, rm, stat } from "node:fs/promises";
import { dirname, isAbsolute, join } from "node:path";
import { pathToFileURL } from "node:url";

const SDK = "typescript", EXPECTED_VERSION = "1.4.2", JSON_LIMIT = 1 << 20;
const ACTIONS = new Set(["local", "prepare", "validate", "run", "kill-eight", "kill-download", "resume", "replay", "revise-two", "draft-selected"]);
class AcceptanceFailure extends Error { constructor(code) { super(code); this.code = code; } }
function require(value, code) { if (!value) throw new AcceptanceFailure(code); }

async function regularPrivate(path) {
  const info = await lstat(path);
  require(info.isFile(), "acceptance_file_not_regular");
  if (process.platform !== "win32") require((info.mode & 0o077) === 0, "acceptance_file_not_private");
}
async function readJSON(path) {
  await regularPrivate(path);
  require((await stat(path)).size <= JSON_LIMIT, "acceptance_json_too_large");
  const value = JSON.parse(await readFile(path, "utf8"));
  require(value && typeof value === "object" && !Array.isArray(value), "acceptance_json_not_object");
  return value;
}
async function writeNew(path, value) {
  const raw = Buffer.from(JSON.stringify(value) + "\n");
  require(raw.byteLength <= JSON_LIMIT, "acceptance_json_too_large");
  const workspace = await mkdtemp(join(dirname(path), ".acceptance-"));
  try {
    const temporary = join(workspace, "new.json");
    const file = await open(temporary, "wx", 0o600);
    try { await file.writeFile(raw); await file.sync(); } finally { await file.close(); }
    await link(temporary, path);
    if (process.platform !== "win32") {
      const directory = await open(dirname(path), constants.O_RDONLY);
      try { await directory.sync(); } finally { await directory.close(); }
    }
  } finally { await rm(workspace, { recursive: true, force: true }); }
}
async function fileSHA256(path) {
  const value = createHash("sha256");
  for await (const chunk of createReadStream(path)) value.update(chunk);
  return value.digest("hex");
}

async function localWorkflow(LocalCutmy, LocalError, input, directory, timeout) {
  const finalDirectory = join(directory, "local-final"), draftDirectory = join(directory, "local-draft");
  await mkdir(finalDirectory, { mode: 0o700 }); await mkdir(draftDirectory, { mode: 0o700 });
  const plan = { version: 1, input: await realpath(input), options: { format: "mp4", quality: "best", cut_mode: "accurate" },
    clips: [{ id: "opening", label: "Начало", ranges: [{ start_ms: 500, end_ms: 1500 }] },
      { id: "answer", label: "Позже, потом раньше", ranges: [{ start_ms: 2200, end_ms: 3000 }, { start_ms: 200, end_ms: 1000 }] },
      { id: "vertical", label: "Крупный план", ranges: [{ start_ms: 1000, end_ms: 2000 }], options: { crop: { x: .25, y: 0, width: .5, height: 1 } } }] };
  const client = new LocalCutmy({ binary: process.env.CUTMY_SDK_BINARY, threads: 1 });
  const options = { timeoutMs: Math.min(timeout, 120) * 1000 };
  const preflight = await client.validateCollection(plan, finalDirectory, options);
  require(preflight.valid === true && (await readdir(finalDirectory)).length === 0, "acceptance_local_validation_wrote_files");
  const final = await client.executeCollection(plan, finalDirectory, options);
  require(final.outputs.length === 3, "acceptance_local_outputs");
  const preserved = await Promise.all(final.outputs.map(async output => [output.path, await fileSHA256(output.path)]));
  try { await client.validateCollection(plan, finalDirectory, options); throw new AcceptanceFailure("acceptance_local_collision_missing"); }
  catch (error) { require(error instanceof LocalError && error.code === "output_exists", "acceptance_local_collision_code"); }
  for (const [path, sha] of preserved) require(await fileSHA256(path) === sha, "acceptance_local_collision_changed_file");
  const selected = ["answer", "vertical"], draftOptions = { ...options, clipIds: selected, rendition: "draft" };
  const draftPreflight = await client.validateCollection(plan, draftDirectory, draftOptions);
  require(draftPreflight.valid === true && (await readdir(draftDirectory)).length === 0, "acceptance_local_draft_validation_wrote_files");
  const draft = await client.executeCollection(plan, draftDirectory, draftOptions);
  require(JSON.stringify(draft.outputs.map(item => item.id)) === JSON.stringify(selected), "acceptance_local_draft_selection");
  const outputs = [];
  for (const [rendition, result] of [["final", final], ["draft", draft]]) {
    for (const output of result.outputs) outputs.push({ ...output, clip_id: output.id, rendition, sha256: await fileSHA256(output.path) });
  }
  require((await readdir(finalDirectory)).length === 3 && (await readdir(draftDirectory)).length === 2, "acceptance_local_temporary_files");
  return { complete: true, phase: "local", outputs, pending: [], preflight, draft_preflight: draftPreflight };
}

async function main() {
  require(process.argv.length === 6, "acceptance_arguments");
  const [action, input, work, baseUrl] = process.argv.slice(2);
  require(ACTIONS.has(action), "acceptance_action");
  if (action !== "local") {
    const base = new URL(baseUrl);
    require(base.protocol === "http:" && ["localhost", "127.0.0.1", "[::1]"].includes(base.hostname)
      && !base.username && !base.password && !base.search && !base.hash, "acceptance_requires_loopback");
  }
  const entry = process.env.CUTMY_SDK_JS_ENTRY;
  require(entry && isAbsolute(entry), "acceptance_requires_installed_tarball");
  const mainEntry = await realpath(entry), packageDirectory = dirname(dirname(mainEntry));
  require(packageDirectory.endsWith(join("node_modules", "@cutmyvideo", "sdk")), "acceptance_requires_installed_tarball");
  const metadata = JSON.parse(await readFile(join(packageDirectory, "package.json"), "utf8"));
  require(metadata.name === "@cutmyvideo/sdk" && metadata.version === EXPECTED_VERSION, "acceptance_sdk_version");
  const localEntry = await realpath(process.env.CUTMY_SDK_LOCAL_JS_ENTRY ?? join(packageDirectory, "dist/local.js"));
  const collectionEntry = await realpath(process.env.CUTMY_SDK_COLLECTION_JS_ENTRY ?? join(packageDirectory, "dist/collections.js"));
  require(dirname(localEntry) === dirname(mainEntry) && dirname(collectionEntry) === dirname(mainEntry), "acceptance_installed_entry_scope");
  const { CutmyClient, LocalError } = await import(pathToFileURL(mainEntry).href);
  const { LocalCutmy, uploadFile } = await import(pathToFileURL(localEntry).href);
  const { runCollection, resumeCollection } = await import(pathToFileURL(collectionEntry).href);
  await mkdir(work, { recursive: true, mode: 0o700 });
  const directory = await realpath(work);
  if (process.platform !== "win32") require(((await stat(directory)).mode & 0o077) === 0, "acceptance_directory_not_private");
  const project = join(directory, "collection"), ownerPath = join(directory, "prepared-owner.json"), sourcePath = join(directory, "prepared-source.json");
  const timeout = Number(process.env.CUTMY_COLLECTION_ACCEPTANCE_TIMEOUT ?? "600");
  require(Number.isFinite(timeout) && timeout >= 1 && timeout <= 600, "acceptance_timeout");
  const secrets = [];
  function emit(value) {
    const raw = JSON.stringify(value);
    require(Buffer.byteLength(raw) <= JSON_LIMIT, "acceptance_report_too_large");
    require(!secrets.some(secret => raw.includes(secret)), "acceptance_secret_in_public_report");
    process.stdout.write(raw + "\n");
  }
  function finish(report) { emit({ ...report, consumer_sdk: SDK, consumer_sdk_version: metadata.version, consumer_action: action }); }
  async function exists(path) { try { await lstat(path); return true; } catch (error) { if (error.code === "ENOENT") return false; throw error; } }

  if (action === "local") { finish(await localWorkflow(LocalCutmy, LocalError, input, directory, timeout)); return; }

  if (action === "prepare") {
    require(!(await exists(ownerPath)) && !(await exists(sourcePath)), "acceptance_preparation_already_exists");
    require((await stat(input)).isFile(), "acceptance_source_missing");
    const client = new CutmyClient({ baseUrl, requestTimeoutMs: 30_000, transferTimeoutMs: 300_000 });
    await client.initializeSession();
    const secret = client.exportSessionCookie(); secrets.push(secret);
    await writeNew(ownerPath, { schema_version: 1, server_url: baseUrl, session_cookie: secret });
    const source = await uploadFile(client, input);
    await writeNew(sourcePath, { schema_version: 1, server_url: baseUrl, source });
    finish({ complete: true, phase: "prepared", source_id: source.id, source });
    return;
  }

  const prepared = await readJSON(ownerPath);
  require(prepared.schema_version === 1 && prepared.server_url === baseUrl, "acceptance_preparation_scope");
  require(typeof prepared.session_cookie === "string" && /^[A-Za-z0-9_-]{43}$/.test(prepared.session_cookie), "acceptance_preparation_owner");
  secrets.push(prepared.session_cookie);
  const source = await readJSON(sourcePath);
  require(source.server_url === baseUrl && source.source && typeof source.source === "object", "acceptance_source_scope");
  const onProgress = async report => {
    let checkpoint;
    if (action === "kill-eight" && report.phase === "output_ready" && report.outputs.length === 8) {
      await regularPrivate(join(project, "state.json")); await regularPrivate(join(project, "owner.json"));
      for (const output of report.outputs) {
        require((await stat(output.path)).isFile() && (await stat(output.path)).size === output.size_bytes
          && await fileSHA256(output.path) === output.sha256, "acceptance_ready_receipt");
      }
      checkpoint = "eight_ready";
    } else if (action === "kill-download" && report.phase === "download_interrupted") {
      require(Number.isSafeInteger(report.download?.offset) && report.download.offset > 0
        && report.download.offset < report.download.total_size, "acceptance_interrupted_prefix");
      await regularPrivate(join(project, "state.json")); await regularPrivate(join(project, "owner.json"));
      checkpoint = "download_interrupted";
    }
    if (checkpoint) {
      emit({ event: "checkpoint", checkpoint, report });
      // Keep the process alive until the harness's actual OS kill. Returning or
      // throwing here would not exercise lock recovery after a crashed process.
      await new Promise(() => { setInterval(() => {}, 1000); });
    }
  };
  const requestName = ({ "revise-two": "revised-request.json", "draft-selected": "draft-request.json" })[action] ?? "request.json";
  const request = await readJSON(join(directory, requestName));
  require(request.source_id === source.source.id, "acceptance_request_source");
  const initial = ["run", "kill-eight", "kill-download"].includes(action) && !(await exists(join(project, "state.json")));
  const client = new CutmyClient({ baseUrl, requestTimeoutMs: 30_000, transferTimeoutMs: 120_000,
    ...(initial || action === "validate" ? { sessionCookie: prepared.session_cookie } : {}) });
  if (action === "validate") { finish(await client.validateCollection(request, { timeoutMs: timeout * 1000 })); return; }
  const options = { timeoutMs: timeout * 1000, pollIntervalMs: 1000, onProgress, maxOutputBytes: 256 << 20 };
  const revised = ["replay", "revise-two", "draft-selected"].includes(action) ? { request } : {};
  const report = initial ? await runCollection(client, request, project, options) : await resumeCollection(client, project, { ...options, ...revised });
  require(!["kill-eight", "kill-download"].includes(action), "acceptance_crash_checkpoint_missing");
  require(report.complete === true && report.pending.length === 0, "acceptance_collection_incomplete");
  finish(report);
}

try {
  if (process.platform !== "win32") process.umask(0o077);
  await main();
} catch (error) {
  // Exception strings can contain caller-owned data; only bounded codes leave
  // this process. The harness keeps its own independent fault/media evidence.
  const code = typeof error.code === "string" && /^[a-z0-9_]{1,100}$/.test(error.code) ? error.code : "consumer_failure";
  const exceptionType = typeof error.name === "string" && /^[A-Za-z0-9_]{1,80}$/.test(error.name) ? error.name : "Error";
  process.stderr.write(JSON.stringify({ ok: false, code, exception_type: exceptionType, consumer_sdk: SDK }) + "\n");
  process.exitCode = 1;
}
