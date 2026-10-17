/** Node-only entry. The main HTTP entry has no Node built-in imports. */
import { spawn } from "node:child_process";
import { openAsBlob } from "node:fs";
import { link, mkdtemp, open, rm } from "node:fs/promises";
import { basename, dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { ApiError, LocalError } from "./errors.js";
import { decode, JSON_LIMIT, object, positive } from "./protocol.js";
import { subtitleRequest, subtitleResult } from "./subtitles.js";
import { analysisFlags, analysisResult, cutImport, cutExport, cutList, cutFormat, cutExportResult, planResult, renderOptionSets, renderReferences } from "./media.js";
import type { AnalysisKind } from "./media.js";
import type { SilenceOptions, SilenceResult, VideoAnalysisOptions, VideoAnalysisResult, LoudnessOptions, LoudnessResult, SilencePlanOptions, CutList, CutListFormat, CutListImportRequest, LocalCutListExportRequest, CutListExportResult, CutListPlanOptions } from "./types.js";
import type { SubtitleRequest, SubtitleResult } from "./types.js";
import type { CutmyClient } from "./client.js";
import type { BatchResult, CollectionPlan, DownloadOptions, InspectedSource, LocalCollectionOptions, LocalCollectionResult, LocalCollectionValidation, LocalPreflightResult, Plan, RequestOptions, Source } from "./types.js";

export interface LocalOptions {
  /** Installed cutmy executable. No binaries are downloaded or upgraded. */
  binary?: string;
  /** Arguments preceding cutmy flags, for an explicitly configured launcher. */
  binaryArgs?: string[];
  cwd?: string;
  env?: Record<string, string | undefined>;
  profile?: "fast" | "compact";
  threads?: number;
  /** Time for graceful cancellation before the whole process tree is killed. */
  terminationGraceMs?: number;
}

export class LocalCutmy {
  readonly #binary: string;
  readonly #prefix: string[];
  readonly #cwd: string;
  readonly #env: NodeJS.ProcessEnv;
  readonly #profile: string;
  readonly #threads: number;
  readonly #grace: number;
  constructor(options: LocalOptions = {}) {
    this.#binary = options.binary ?? "cutmy";
    if (!this.#binary || this.#binary.includes("\0")) throw new TypeError("binary must name an executable");
    this.#prefix = [...(options.binaryArgs ?? [])];
    this.#cwd = resolve(options.cwd ?? process.cwd());
    this.#env = { ...process.env, ...options.env };
    this.#profile = options.profile ?? "fast";
    this.#threads = options.threads ?? 2;
    this.#grace = positive(options.terminationGraceMs ?? 2000, "terminationGraceMs");
    if (!["fast", "compact"].includes(this.#profile) || !Number.isInteger(this.#threads) || this.#threads < 1 || this.#threads > 32) throw new TypeError("Use profile fast|compact and threads 1..32");
  }
  inspect(input: string, options: RequestOptions = {}): Promise<InspectedSource> {
    if (!input) throw new TypeError("input is required");
    const timeout = positive(options.timeoutMs ?? 120_000, "timeoutMs");
    return this.#run(["inspect", "--input", resolve(this.#cwd, input), "--timeout", `${timeout}ms`], "inspect", timeout, options.signal);
  }
  analyzeSilence(input: string, analysis: SilenceOptions = {}, options: RequestOptions = {}): Promise<SilenceResult> { return this.#analyze("silence",input,analysis,options); }
  analyzeVideo(input: string, analysis: VideoAnalysisOptions = {}, options: RequestOptions = {}): Promise<VideoAnalysisResult> { return this.#analyze("video",input,analysis,options); }
  analyzeLoudness(input: string, analysis: LoudnessOptions = {}, options: RequestOptions = {}): Promise<LoudnessResult> { return this.#analyze("loudness",input,analysis,options); }
  silencePlan(input: string, analysis: SilenceOptions = {}, options: SilencePlanOptions = {}): Promise<Plan> {
    const format = options.format ?? "mp4", mode = options.outputMode ?? "joined";
    if (!["mp4","mp3"].includes(format) || !["joined","separate"].includes(mode)) throw new TypeError("Silence plans require mp4|mp3 and joined|separate");
    return this.#analyze("silence",input,analysis,options,["--plan","--format",format,"--output-mode",mode]);
  }
  async #mediaResult<T>(operation: Promise<unknown>, validate: (value: unknown) => T): Promise<T> {
    try { return validate(await operation); }
    catch (error) { if (error instanceof ApiError) throw new LocalError("Local analysis or cut-list result is invalid", { code: "invalid_response" }); throw error; }
  }
  #analyze<T>(kind: AnalysisKind, input: string, analysis: unknown, options: RequestOptions, extra: string[] = []): Promise<T> {
    if (!input) throw new TypeError("input is required");
    const timeout = positive(options.timeoutMs ?? 120000,"timeoutMs");
    const args = ["analyze",kind,"--input",resolve(this.#cwd,input),"--threads",String(this.#threads),...analysisFlags(kind,analysis),...extra,"--timeout",`${timeout}ms`];
    return this.#mediaResult(this.#run(args,"object",timeout,options.signal), value => extra.includes("--plan") ? planResult<T>(value) : analysisResult<T>(kind,value));
  }
  importCutList(request: CutListImportRequest, options: RequestOptions = {}): Promise<CutList> {
    const body = cutImport(request);
    return this.#cut("import",body.text,body.format,cutList,options);
  }
  planCutList(request: CutListImportRequest, options: CutListPlanOptions): Promise<Plan> {
    const body = cutImport(request), mode = options.outputMode ?? "joined";
    if (!options.media || !Number.isSafeInteger(options.durationMs) || options.durationMs <= 0 || !["joined","separate"].includes(mode)) throw new TypeError("Cut-list plans require media, positive durationMs and joined|separate");
    return this.#cut("import",body.text,body.format,value => planResult<Plan>(value),options,"--input",["--plan","--media",resolve(this.#cwd,options.media),"--duration-ms",String(options.durationMs),"--output-mode",mode]);
  }
  exportCutList(request: LocalCutListExportRequest, options: RequestOptions = {}): Promise<CutListExportResult> {
    let content: unknown, flag: string;
    if (object(request) && "plan" in request && Object.keys(request).sort().join(",") === "format,plan") {
      cutFormat(request.format);
      try { planResult(request.plan); } catch { throw new TypeError("Invalid plan"); }
      content = request.plan; flag = "--plan";
    } else {
      const body = cutExport(request as import("./types.js").CutListExportRequest);
      content = "cut_list" in body ? body.cut_list : body.segments; flag = "cut_list" in body ? "--input" : "--segments";
    }
    return this.#cut("export",JSON.stringify(content),request.format,value => cutExportResult(value,request.format),options,flag);
  }
  async #cut<T>(operation: string, content: string, format: CutListFormat, validate: (value: unknown) => T, options: RequestOptions, flag = "--input", extra: string[] = []): Promise<T> {
    cutFormat(format);
    const timeout = positive(options.timeoutMs ?? 120000,"timeoutMs"), deadline = performance.now() + timeout;
    if (options.signal?.aborted) throw new LocalError("The operation was cancelled", { code: "cancelled" });
    if (Buffer.byteLength(content) > JSON_LIMIT) throw new TypeError("Cut-list input exceeds1MiB");
    const directory = await mkdtemp(join(tmpdir(),"cutmy-sdk-cutlist-"));
    try {
      const path = join(directory,"input.txt"), file = await open(path,"wx",0o600);
      try { await file.writeFile(content); } finally { await file.close(); }
      const remaining = Math.floor(deadline - performance.now());
      if (remaining <= 0) throw new LocalError("The operation timed out", { code: "timeout" });
      return await this.#mediaResult(this.#run(["cutlist",operation,"--format",format,flag,path,...extra],"object",remaining,options.signal),validate);
    } finally { await rm(directory,{recursive:true,force:true}); }
  }
  preflight(plan: Plan, output: string, options: RequestOptions = {}): Promise<LocalPreflightResult> { return this.#plan("preflight", plan, output, options); }
  executePlan(plan: Plan, output: string, options: RequestOptions = {}): Promise<BatchResult> { return this.#plan("batch", plan, output, options); }
  validateCollection(plan: CollectionPlan, output: string, options: LocalCollectionOptions = {}): Promise<LocalCollectionValidation> { return this.#plan("collection validate", plan, output, options, options); }
  executeCollection(plan: CollectionPlan, output: string, options: LocalCollectionOptions = {}): Promise<LocalCollectionResult> { return this.#plan("collection export", plan, output, options, options); }
  async retimeSubtitles(request: SubtitleRequest, options: RequestOptions = {}): Promise<SubtitleResult> {
    const body = subtitleRequest(request), timeout = positive(options.timeoutMs ?? 120000, "timeoutMs"), deadline = performance.now() + timeout;
    const directory = await mkdtemp(join(tmpdir(), "cutmy-sdk-subtitles-"));
    try {
      const path = join(directory, "request.json"), file = await open(path, "wx", 0o600);
      try { await file.writeFile(JSON.stringify(body)); } finally { await file.close(); }
      const remaining = Math.floor(deadline - performance.now());
      if (remaining <= 0) throw new LocalError("Subtitle operation timed out", { code: "timeout" });
      const result = await this.#run(["subtitles", "retime", "--request", path, "--timeout", `${remaining}ms`], "object", remaining, options.signal, 64 << 20);
      try { return await subtitleResult(result, body); }
      catch (error) { if (error instanceof ApiError) throw new LocalError("Local subtitle result identity, size or hash differs", { code: "invalid_response" }); throw error; }
    } finally { await rm(directory, { recursive: true, force: true }); }
  }
  async #plan<T>(command: string, plan: Plan | CollectionPlan, output: string, options: RequestOptions, collection?: LocalCollectionOptions): Promise<T> {
    const timeout = positive(options.timeoutMs ?? (command === "batch" || command === "collection export" ? 30 * 60_000 : 120_000), "timeoutMs");
    if (!plan.input || !output) throw new TypeError("plan.input and output are required");
    if (options.signal?.aborted) throw new LocalError("The operation was cancelled", { code: "cancelled" });
    renderReferences(plan,true);
    // Resolve before moving the plan: CLI resolves relative input beside its plan file.
    const data = JSON.parse(JSON.stringify({ ...plan, input: resolve(this.#cwd, plan.input) })) as Plan | CollectionPlan;
    renderReferences(data,true);
    for (const settings of renderOptionSets(data)) if (object(settings.audio_replace)) settings.audio_replace.input = resolve(this.#cwd,String(settings.audio_replace.input));
    const bytes = Buffer.from(JSON.stringify(data));
    if (bytes.length > JSON_LIMIT) throw new TypeError("Plan exceeds the 1 MiB limit");
    const extra: string[] = [];
    if (collection) {
      if (collection.clipIds !== undefined) {
        if (!Array.isArray(collection.clipIds) || !collection.clipIds.every(id => /^[a-z0-9][a-z0-9_-]{0,63}$/.test(id)) || new Set(collection.clipIds).size !== collection.clipIds.length) throw new TypeError("clipIds must contain unique valid IDs");
        if (collection.clipIds.length) extra.push("--clips", collection.clipIds.join(","));
      }
      if (collection.rendition !== undefined) {
        if (!["final", "draft"].includes(collection.rendition)) throw new TypeError("rendition must be final or draft");
        extra.push("--rendition", collection.rendition);
      }
      if (collection.maxOutputBytes !== undefined) {
        if (!Number.isSafeInteger(collection.maxOutputBytes) || collection.maxOutputBytes <= 0) throw new TypeError("maxOutputBytes must be a positive safe integer");
        extra.push("--max-output-bytes", String(collection.maxOutputBytes));
      }
    }
    const deadline = performance.now() + timeout;
    let directory: string | undefined;
    try {
      directory = await mkdtemp(join(tmpdir(), "cutmy-sdk-"));
      const path = join(directory, "plan.json");
      const file = await open(path, "wx", 0o600);
      try { await file.writeFile(bytes); } finally { await file.close(); }
      const remaining = Math.floor(deadline - performance.now());
      if (remaining <= 0) throw new LocalError("The operation timed out", { code: "timeout" });
      const kind = command === "collection export" ? "local-collection-result" : command === "collection validate" ? "local-collection-validation" : command === "batch" ? "batch" : "local-preflight";
      return await this.#run<T>([...command.split(" "), "--plan", path, "--output", resolve(this.#cwd, output), "--profile", this.#profile, "--threads", `${this.#threads}`, "--timeout", `${remaining}ms`, ...extra], kind, remaining, options.signal);
    } catch (error) { if (error instanceof LocalError) throw error; throw new LocalError("The local plan could not be prepared", { code: "filesystem_error" }); }
    finally { if (directory) await rm(directory, { recursive: true, force: true }); }
  }
  #run<T>(args: string[], kind: string, timeoutMs: number, signal?: AbortSignal, stdoutLimit = JSON_LIMIT): Promise<T> {
    if (signal?.aborted) return Promise.reject(new LocalError("The operation was cancelled", { code: "cancelled" }));
    return new Promise((resolveResult, reject) => {
      const child = spawn(this.#binary, [...this.#prefix, "--json-errors", ...args], { cwd: this.#cwd, env: this.#env, shell: false, windowsHide: true, detached: process.platform !== "win32", stdio: ["ignore", "pipe", "pipe"] });
      let terminalCode: string | undefined;
      let spawnFailed = false;
      let stdoutSize = 0;
      let stderrSize = 0;
      const stdout: Buffer[] = [];
      const stderr: Buffer[] = [];
      let forceTimer: ReturnType<typeof setTimeout> | undefined;
      const killers = new Set<Promise<void>>();
      const windowsKill = (forced: boolean) => {
        const killer = spawn(join(process.env.SystemRoot ?? "C:\\Windows", "System32", "taskkill.exe"), ["/pid", `${child.pid}`, "/T", ...(forced ? ["/F"] : [])], { windowsHide: true, stdio: "ignore", shell: false });
        const fallback = () => { try { child.kill(forced ? "SIGKILL" : "SIGTERM"); } catch { /* The owned child already exited. */ } };
        const closed: Promise<void> = new Promise(done => {
          const limit = setTimeout(() => {
            try { killer.kill("SIGKILL"); } catch { /* The helper already exited. */ }
            if (forced) fallback();
          }, forced ? 1000 : this.#grace);
          killer.once("error", fallback);
          killer.once("exit", code => { if (forced && code !== 0) fallback(); });
          killer.once("close", () => { clearTimeout(limit); killers.delete(closed); done(); });
        });
        killers.add(closed);
      };
      const force = () => {
        if (!child.pid) return;
        if (process.platform === "win32") {
          windowsKill(true);
        } else { try { process.kill(-child.pid, "SIGKILL"); } catch { child.kill("SIGKILL"); } }
      };
      const stop = (code: string) => {
        if (terminalCode) return;
        terminalCode = code;
        if (child.pid) {
          if (process.platform === "win32") {
            // Windows has no POSIX SIGTERM; taskkill /T requests tree termination.
            windowsKill(false);
          } else { try { process.kill(-child.pid, "SIGTERM"); } catch { child.kill("SIGTERM"); } }
        }
        forceTimer = setTimeout(force, this.#grace);
      };
      const abort = () => stop("cancelled");
      signal?.addEventListener("abort", abort, { once: true });
      const timer = setTimeout(() => stop("timeout"), timeoutMs);
      const collect = (chunk: Buffer, out: boolean) => {
        if (out) stdoutSize += chunk.length; else stderrSize += chunk.length;
        if ((out ? stdoutSize : stderrSize) > (out ? stdoutLimit : JSON_LIMIT)) { stop("response_too_large"); return; }
        (out ? stdout : stderr).push(chunk);
      };
      child.stdout.on("data", chunk => collect(chunk as Buffer, true));
      child.stderr.on("data", chunk => collect(chunk as Buffer, false));
      child.on("error", () => { spawnFailed = true; });
      child.on("close", async exitCode => {
        // A launcher can exit after SIGTERM while a descendant ignores it and
        // closes inherited pipes. Finish our own process group before returning.
        if (terminalCode && process.platform !== "win32" && child.pid) {
          try { process.kill(-child.pid, "SIGKILL"); } catch { /* The owned group already exited. */ }
        }
        clearTimeout(timer); if (forceTimer) clearTimeout(forceTimer); signal?.removeEventListener("abort", abort);
        // Tree helpers remain owned until their bounded termination completes.
        // Do not let a hung taskkill keep Node alive after the call settles.
        await Promise.all(killers);
        let result: BatchResult | undefined;
        let value: unknown;
        try { value = JSON.parse(Buffer.concat(stdout).toString("utf8")) as unknown; if (kind === "batch" || kind === "local-collection-result") result = decode<BatchResult>(value, kind); } catch { /* A failure may legitimately have no output JSON. */ }
        let publicCode = terminalCode ?? (spawnFailed ? "tool_unavailable" : "local_error");
        let message = terminalCode === "timeout" ? "The operation timed out" : terminalCode === "cancelled" ? "The operation was cancelled" : terminalCode === "response_too_large" ? "CLI output exceeded the JSON limit" : spawnFailed ? "The cutmy executable could not be started" : "The local operation failed";
        if (exitCode !== 0 || terminalCode || spawnFailed) {
          if (!terminalCode && !spawnFailed) {
            try {
              const envelope: unknown = JSON.parse(Buffer.concat(stderr).toString("utf8"));
              if (object(envelope) && object(envelope.error) && typeof envelope.error.code === "string" && typeof envelope.error.message === "string") { publicCode = envelope.error.code; message = envelope.error.message; }
            } catch { /* Never include raw subprocess diagnostics. */ }
          }
          reject(new LocalError(message, { code: publicCode, exitCode, ...(result ? { result } : {}) })); return;
        }
        try { resolveResult(decode<T>(value, kind)); }
        catch { reject(new LocalError("The CLI returned an unexpected response", { code: "invalid_response", exitCode })); }
      });
    });
  }
}

/** Native FormData reads this disk-backed Blob without loading the file into JS memory. */
export async function uploadFile(client: CutmyClient, path: string, options: RequestOptions & { filename?: string } = {}): Promise<Source> {
  if (options.signal?.aborted) throw new ApiError("The operation was cancelled", { code: "cancelled" });
  let file: Blob;
  try { file = await openAsBlob(path); } catch { throw new ApiError("The upload file could not be opened", { code: "filesystem_error" }); }
  return client.upload(file, options.filename ?? basename(path), options);
}

/** Downloads beside the destination and exclusively publishes it; existing files survive. */
export async function downloadArtifactToFile(client: CutmyClient, id: string, output: string, options: DownloadOptions = {}): Promise<string> {
  const destination = resolve(output);
  const parent = dirname(destination);
  let workspace: string | undefined;
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
  try {
    workspace = await mkdtemp(join(parent, ".cutmy-download-"));
    const temporary = join(workspace, "download");
    const download = await client.downloadArtifact(id, options);
    reader = download.stream.getReader();
    const file = await open(temporary, "wx", 0o600);
    try {
      while (true) {
        const next = await reader.read();
        if (next.done) break;
        let offset = 0;
        while (offset < next.value.byteLength) {
          const written = await file.write(next.value, offset, next.value.byteLength - offset);
          if (written.bytesWritten < 1) throw new ApiError("Could not write download", { code: "filesystem_error" });
          offset += written.bytesWritten;
        }
      }
      await file.sync();
    } finally { await file.close(); }
    try { await link(temporary, destination); }
    catch (error) { throw new ApiError("The download could not be published; an existing file is preserved", { code: object(error) && error.code === "EEXIST" ? "output_exists" : "filesystem_error" }); }
    return destination;
  } catch (error) { if (error instanceof ApiError) throw error; throw new ApiError("The download file could not be written", { code: "filesystem_error" }); }
  finally {
    try { await reader?.cancel().catch(() => {}); } finally { reader?.releaseLock(); if (workspace) await rm(workspace, { recursive: true, force: true }); }
  }
}

export { LocalError } from "./errors.js";
export type { BatchResult, InspectedSource, LocalPreflightResult, Plan } from "./types.js";
