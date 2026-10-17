import { ApiError, JobError } from "./errors.js";
import { abortedError, abortable, boundedJSON, budget, decode, delay, endpointID, object, positive, retryAfter } from "./protocol.js";
import { preparationKey, preparationReceipt, uploadFingerprint } from "./source_protocol.js";
import { subtitleRequest, subtitleResult } from "./subtitles.js";
import { projectResult } from "./projects.js";
import { analysisOptions, analysisResult, cutImport, cutExport, cutList, cutExportResult, renderReferences } from "./media.js";
import type { AnalysisKind } from "./media.js";
import type { SilenceOptions, SilenceResult, VideoAnalysisOptions, VideoAnalysisResult, LoudnessOptions, LoudnessResult, CutList, CutListImportRequest, CutListExportRequest, CutListExportResult } from "./types.js";
import type { AgentProject, ProjectCreateRequest, ProjectEditRequest, ProjectHandoff } from "./types.js";
import type { ContentFingerprint, SourcePreparationReceipt, SourceSubmissionOptions, SubtitleRequest, SubtitleResult } from "./types.js";
import type { ArtifactDownload, ArtifactRangeDownload, CollectionDiagnostic, CollectionRequest, CollectionSubmission, CollectionValidation, DownloadOptions, ExportRequest, Job, PreflightResult, RangeDownloadOptions, RequestOptions, Session, Source, SubmissionOptions, Tracks, WaitOptions } from "./types.js";

export interface ClientOptions {
  baseUrl: string;
  requestTimeoutMs?: number;
  sourceTimeoutMs?: number;
  transferTimeoutMs?: number;
  /** Node-only persisted owner capability, supplied from a private credential store. */
  sessionCookie?: string;
  /** Useful for instrumentation or a platform fetch adapter. Mutations are never retried. */
  fetch?: typeof globalThis.fetch;
}
type Context = { jobId?: string; idempotencyKey?: string; uploadFingerprint?: ContentFingerprint };

export class CutmyClient {
  readonly #base: URL;
  readonly #fetch: typeof globalThis.fetch;
  readonly #requestMs: number;
  readonly #sourceMs: number;
  readonly #transferMs: number;
  #cookie = "";
  #session: Promise<Session> | undefined;
  /** Canonical server scope, safe to store in a public checkpoint. */
  get serverUrl(): string { return this.#base.href.replace(/\/$/, ""); }
  constructor(options: ClientOptions) {
    this.#base = new URL(options.baseUrl);
    if (!["http:", "https:"].includes(this.#base.protocol) || this.#base.username || this.#base.password || this.#base.search || this.#base.hash) throw new TypeError("baseUrl must be an HTTP(S) URL without credentials, query or fragment");
    this.#base.pathname = this.#base.pathname.replace(/\/$/, "") + "/";
    this.#fetch = options.fetch ?? globalThis.fetch.bind(globalThis);
    this.#requestMs = positive(options.requestTimeoutMs ?? 30_000, "requestTimeoutMs");
    this.#sourceMs = positive(options.sourceTimeoutMs ?? 120_000, "sourceTimeoutMs");
    this.#transferMs = positive(options.transferTimeoutMs ?? 300_000, "transferTimeoutMs");
    if (options.sessionCookie !== undefined) {
      const runtime = globalThis as typeof globalThis & { process?: { versions?: { node?: string } } };
      if (!runtime.process?.versions?.node) throw new TypeError("sessionCookie is only supported in Node; browsers use their own HttpOnly cookie");
      if (!/^[A-Za-z0-9_-]{43}$/.test(options.sessionCookie)) throw new TypeError("sessionCookie must be the encoded cutmy_session value");
      this.#cookie = `cutmy_session=${options.sessionCookie}`;
    }
  }
  /** Initializes an owner capability. Concurrent calls share one initialization. */
  async initializeSession(options: RequestOptions = {}): Promise<Session> {
    const scope = budget(options.timeoutMs ?? this.#requestMs, options.signal);
    try { return await abortable(this.#ensureSession(), scope.controller.signal); }
    catch (error) { if (scope.controller.signal.aborted) throw abortedError(scope); throw error; }
    finally { scope.dispose(); }
  }
  /** Explicit Node-only credential export for a caller's private session store. */
  exportSessionCookie(): string {
    const runtime = globalThis as typeof globalThis & { process?: { versions?: { node?: string } } };
    if (!runtime.process?.versions?.node) throw new ApiError("Browser owner cookies are HttpOnly and cannot be exported", { code: "session_unavailable" });
    if (!this.#cookie) throw new ApiError("Initialize the owner session before exporting its capability", { code: "session_unavailable" });
    return this.#cookie.slice(this.#cookie.indexOf("=") + 1);
  }
  /** Node-only restoration refuses replacing an already established owner. */
  restoreSessionCookie(cookie: string): void {
    const runtime = globalThis as typeof globalThis & { process?: { versions?: { node?: string } } };
    if (!runtime.process?.versions?.node || !/^[A-Za-z0-9_-]{43}$/.test(cookie)) throw new TypeError("A valid Node owner capability is required");
    if (this.#cookie && this.#cookie !== "cutmy_session=" + cookie) throw new ApiError("The client already belongs to another owner", { code: "owner_conflict" });
    if (!this.#cookie) { this.#cookie = "cutmy_session=" + cookie; this.#session = undefined; }
  }
  #ensureSession(): Promise<Session> {
    if (!this.#session) {
      const pending = this.#request<Session>("GET", "api/v1/session", undefined, "session", {}, {}, false);
      this.#session = pending;
      void pending.catch(() => { if (this.#session === pending) this.#session = undefined; });
    }
    return this.#session;
  }
  #headers(json: boolean, context: Context): Headers {
    // Browsers set their real Origin. Node may use an internal service address
    // different from PUBLIC_ORIGIN and must not forge a browser Origin header.
    const headers = new Headers({ Accept: "application/json" });
    if (this.#cookie) headers.set("Cookie", this.#cookie);
    if (json) headers.set("Content-Type", "application/json");
    // Headers takes a ByteString; encode UTF-8 exactly as the Go byte limit does.
    if (context.idempotencyKey) headers.set("Idempotency-Key", Array.from(new TextEncoder().encode(context.idempotencyKey), byte => String.fromCharCode(byte)).join(""));
    if (context.uploadFingerprint) { headers.set("Upload-SHA256", context.uploadFingerprint.sha256); headers.set("Upload-Size", String(context.uploadFingerprint.size_bytes)); }
    return headers;
  }
  #captureSession(response: Response): void {
    // Browser fetch owns HttpOnly cookies. Node fetch exposes them separately.
    for (const value of response.headers.getSetCookie?.() ?? []) {
      const match = /^cutmy_session=([A-Za-z0-9_-]+)(?:;|$)/.exec(value);
      if (match) this.#cookie = `cutmy_session=${match[1]}`;
    }
  }
  async #request<T>(method: string, path: string, data: unknown, kind: string, options: RequestOptions, context: Context = {}, initialize = true, jsonLimit = 1 << 20): Promise<T> {
    const scope = budget(options.timeoutMs ?? this.#requestMs, options.signal);
    let response: Response | undefined;
    try {
      if (initialize) await abortable(this.#ensureSession(), scope.controller.signal);
      if (scope.controller.signal.aborted) throw abortedError(scope, context);
      const multipart = data instanceof FormData;
      response = await this.#fetch(new URL(path, this.#base), {
        method, headers: this.#headers(data !== undefined && !multipart, context),
        ...(data === undefined ? {} : { body: multipart ? data : JSON.stringify(data) }),
        credentials: "include", redirect: "manual", signal: scope.controller.signal,
      });
      if (response.status >= 300 && response.status < 400) { await response.body?.cancel(); throw new ApiError("Redirect responses are refused", { code: "redirect_refused", status: response.status, ...context }); }
      this.#captureSession(response);
      const value = await boundedJSON(response, jsonLimit);
      if (kind === "collection-submission" && [200,202,207,400,408,410,413,429,500,502,503,504].includes(response.status) && object(value) && value.version === 1 && Array.isArray(value.clips)) {
        const report = decode<CollectionSubmission>(value, kind);
        const retry = retryAfter(response).retryAfterSeconds;
        return { ...report, http_status: response.status, ...(retry === undefined ? {} : { retry_after_seconds: retry }) } as T;
      }
      if (!response.ok) {
        const envelope = object(value) && object(value.error) ? value.error : undefined;
        let diagnostic: CollectionDiagnostic | undefined;
        if (["collection-validation", "collection-submission"].includes(kind) && envelope && typeof envelope.code === "string" && typeof envelope.message === "string") diagnostic = { code: envelope.code, message: envelope.message, ...(typeof envelope.clip_id === "string" ? { clip_id: envelope.clip_id } : {}), ...(typeof envelope.field === "string" ? { field: envelope.field } : {}) };
        throw new ApiError(envelope && typeof envelope.message === "string" ? envelope.message : "The API request failed", { code: envelope && typeof envelope.code === "string" ? envelope.code : "http_error", status: response.status, ...retryAfter(response), ...context, ...(diagnostic ? { diagnostic } : {}) });
      }
      return decode<T>(value, kind);
    } catch (error) {
      if (scope.controller.signal.aborted) throw abortedError(scope, context);
      if (error instanceof ApiError) throw new ApiError(error.message, { code: error.code, ...(error.status === undefined ? (response ? { status: response.status } : {}) : { status: error.status }), ...(response ? retryAfter(response) : {}), ...context, ...(error.diagnostic ? { diagnostic: error.diagnostic } : {}) });
      throw new ApiError("The API request could not be completed", { code: "network_error", ...(response ? { status: response.status } : {}), ...context });
    } finally { scope.dispose(); }
  }
  async listSources(options: RequestOptions = {}): Promise<Source[]> { return (await this.#request<{ sources: Source[] }>("GET", "api/v1/sources", undefined, "sources", options)).sources; }
  importSource(url: string, options: SourceSubmissionOptions = {}): Promise<Source> {
    if (options.idempotencyKey !== undefined) preparationKey(options.idempotencyKey);
    return this.#request("POST", "api/v1/sources", { url }, "source", { ...options, timeoutMs: options.timeoutMs ?? this.#sourceMs }, options.idempotencyKey === undefined ? {} : { idempotencyKey: options.idempotencyKey });
  }
  upload(file: Blob, filename = "upload", options: SourceSubmissionOptions = {}): Promise<Source> {
    if (options.idempotencyKey !== undefined) preparationKey(options.idempotencyKey);
    if (options.fingerprint && options.idempotencyKey === undefined) throw new TypeError("Fingerprint requires a source preparation key");
    const fingerprint = options.idempotencyKey === undefined ? undefined : uploadFingerprint(options.fingerprint);
    if (fingerprint && file.size !== fingerprint.size_bytes) throw new TypeError("Upload size differs from fingerprint");
    const form = new FormData(); form.append("file", file, filename);
    return this.#request("POST", "api/v1/uploads", form, "source", { ...options, timeoutMs: options.timeoutMs ?? this.#sourceMs }, options.idempotencyKey === undefined ? {} : { idempotencyKey: options.idempotencyKey, ...(fingerprint ? { uploadFingerprint: fingerprint } : {}) });
  }
  async getSourcePreparation(key: string, options: RequestOptions = {}): Promise<SourcePreparationReceipt> {
    preparationKey(key);
    return preparationReceipt(await this.#request("GET", `api/v1/source-preparations/${endpointID(key)}`, undefined, "object", options), key);
  }
  async retimeSubtitles(request: SubtitleRequest, options: RequestOptions = {}): Promise<SubtitleResult> {
    const body = subtitleRequest(request);
    return subtitleResult(await this.#request("POST", "api/v1/subtitles/retime", body, "object", options, {}, true, 64 << 20), body);
  }
  async getProject(id: string, options: RequestOptions = {}): Promise<AgentProject> {
    const result = projectResult(await this.#request("GET", `api/v1/projects/${endpointID(id)}`, undefined, "object", options, {}, true, 16 << 20));
    if (result.id !== id) throw new ApiError("Project response identity differs", { code: "invalid_response" });
    for (const entry of result.jobs) decode(entry.job, "job");
    return result;
  }
  async createProject(request: ProjectCreateRequest, options: RequestOptions = {}): Promise<AgentProject> { return projectResult(await this.#request("POST", "api/v1/projects", renderReferences(request), "object", options, {}, true, 16 << 20)); }
  async editProject(id: string, request: ProjectEditRequest, options: RequestOptions = {}): Promise<AgentProject> { return projectResult(await this.#request("PUT", `api/v1/projects/${endpointID(id)}`, renderReferences(request), "object", options, {}, true, 16 << 20)); }
  async createProjectHandoff(id: string, options: RequestOptions = {}): Promise<ProjectHandoff> {
    const value = await this.#request<unknown>("POST", `api/v1/projects/${endpointID(id)}/handoffs`, {}, "object", options);
    if (!object(value) || value.project_id !== id || typeof value.token !== "string" || !/^[A-Za-z0-9_-]{20,256}$/.test(value.token) || typeof value.expires_at !== "string") throw new ApiError("Invalid scoped handoff", { code: "invalid_response" });
    return { project_id: id, token: value.token, expires_at: value.expires_at };
  }
  submitProjectCollection(id: string, request: { revision: number; clip_ids: string[] }, options: RequestOptions & { retryFailed?: boolean; idempotencyKey?: string } = {}): Promise<CollectionSubmission> {
    if (options.retryFailed) preparationKey(options.idempotencyKey);
    return this.#request("POST", `api/v1/projects/${endpointID(id)}/collections${options.retryFailed ? "/retry" : ""}`, request, "collection-submission", options, options.idempotencyKey === undefined ? {} : { idempotencyKey: options.idempotencyKey });
  }
  getSource(id: string, options: RequestOptions = {}): Promise<Source> { return this.#request("GET", `api/v1/sources/${endpointID(id)}`, undefined, "source", options); }
  async #analyze<T>(kind: AnalysisKind, id: string, analysis: unknown, options: RequestOptions): Promise<T> {
    const body = analysisOptions(kind,analysis);
    return analysisResult<T>(kind,await this.#request("POST",`api/v1/sources/${endpointID(id)}/analysis/${kind}`,body,"object",{...options,timeoutMs:options.timeoutMs ?? 120000}));
  }
  analyzeSilence(id: string, analysis: SilenceOptions = {}, options: RequestOptions = {}): Promise<SilenceResult> { return this.#analyze("silence",id,analysis,options); }
  analyzeVideo(id: string, analysis: VideoAnalysisOptions = {}, options: RequestOptions = {}): Promise<VideoAnalysisResult> { return this.#analyze("video",id,analysis,options); }
  analyzeLoudness(id: string, analysis: LoudnessOptions = {}, options: RequestOptions = {}): Promise<LoudnessResult> { return this.#analyze("loudness",id,analysis,options); }
  async importCutList(request: CutListImportRequest, options: RequestOptions = {}): Promise<CutList> {
    return cutList(await this.#request("POST","api/v1/cut-lists/import",cutImport(request),"object",options));
  }
  async exportCutList(request: CutListExportRequest, options: RequestOptions = {}): Promise<CutListExportResult> {
    const body = cutExport(request);
    return cutExportResult(await this.#request("POST","api/v1/cut-lists/export",body,"object",options),body.format);
  }
  getTracks(id: string, options: RequestOptions = {}): Promise<Tracks> { return this.#request("GET", `api/v1/sources/${endpointID(id)}/tracks`, undefined, "tracks", { ...options, timeoutMs: options.timeoutMs ?? this.#sourceMs }); }
  preflight(request: ExportRequest, options: RequestOptions = {}): Promise<PreflightResult> { return this.#request("POST", "api/v1/jobs/preflight", renderReferences(request), "preflight", { ...options, timeoutMs: options.timeoutMs ?? this.#sourceMs }); }
  validateCollection(request: CollectionRequest, options: RequestOptions = {}): Promise<CollectionValidation> {
    return this.#request("POST", "api/v1/collections/validate", renderReferences(request), "collection-validation", { ...options, timeoutMs: options.timeoutMs ?? this.#sourceMs });
  }
  submitCollection(request: CollectionRequest, options: SubmissionOptions): Promise<CollectionSubmission> {
    if (typeof options?.idempotencyKey !== "string" || !options.idempotencyKey.trim() || new TextEncoder().encode(options.idempotencyKey).byteLength > 128 || /[\x00-\x1f\x7f]/.test(options.idempotencyKey)) throw new TypeError("idempotencyKey must contain 1 to 128 UTF-8 bytes and no control characters");
    return this.#request("POST", "api/v1/collections", renderReferences(request), "collection-submission", { ...options, timeoutMs: options.timeoutMs ?? this.#sourceMs }, { idempotencyKey: options.idempotencyKey });
  }
  createJob(request: ExportRequest, options: SubmissionOptions): Promise<Job> {
    if (typeof options?.idempotencyKey !== "string" || !options.idempotencyKey.trim() || new TextEncoder().encode(options.idempotencyKey).byteLength > 128 || /[\x00-\x1f\x7f]/.test(options.idempotencyKey)) throw new TypeError("idempotencyKey must contain 1 to 128 UTF-8 bytes and no control characters");
    return this.#request("POST", "api/v1/jobs", renderReferences(request), "job", options, { idempotencyKey: options.idempotencyKey });
  }
  getJob(id: string, options: RequestOptions = {}): Promise<Job> { return this.#request("GET", `api/v1/jobs/${endpointID(id)}`, undefined, "job", options, { jobId: id }); }
  cancelJob(id: string, options: RequestOptions = {}): Promise<Job> { return this.#request("POST", `api/v1/jobs/${endpointID(id)}/cancel`, undefined, "job", options, { jobId: id }); }
  /** A timeout stops waiting; only cancelJob requests server-side cancellation. */
  async waitForJob(id: string, options: WaitOptions = {}): Promise<Job> {
    endpointID(id);
    const interval = positive(options.pollIntervalMs ?? 1000, "pollIntervalMs");
    if (interval < 1000) throw new TypeError("pollIntervalMs must be at least 1000");
    const scope = budget(options.timeoutMs ?? 30 * 60_000, options.signal);
    try {
      while (true) {
        const job = await this.getJob(id, { signal: scope.controller.signal });
        if (options.onProgress) await abortable(Promise.resolve(options.onProgress(job)), scope.controller.signal);
        if (job.status === "succeeded") return job;
        if (job.status === "failed" || job.status === "cancelled") throw new JobError(job);
        await delay(interval, scope.controller.signal);
      }
    } catch (error) { if (scope.controller.signal.aborted) throw abortedError(scope, { jobId: id }); throw error; }
    finally { scope.dispose(); }
  }
  /** Uses the owner-protected artifact ID route; download_url is never followed. */
  async downloadArtifact(id: string, options: DownloadOptions = {}): Promise<ArtifactDownload> {
    return this.#download(id, options);
  }
  /** Explicit ranged ID transfer; a 200 response must be restarted, never appended. */
  async downloadArtifactRange(id: string, options: RangeDownloadOptions): Promise<ArtifactRangeDownload> {
    if (!Number.isSafeInteger(options.offset) || options.offset < 0) throw new TypeError("offset must be a nonnegative safe integer");
    if (options.expectedSize !== undefined && (!Number.isSafeInteger(options.expectedSize) || options.expectedSize < 0)) throw new TypeError("expectedSize must be a nonnegative safe integer");
    if (options.ifRange !== undefined && (options.ifRange.length > 128 || /[\x00-\x1f\x7f]/.test(options.ifRange))) throw new TypeError("ifRange must be a bounded validator");
    return this.#download(id, options, options);
  }
  async #download(id: string, options: DownloadOptions, range?: RangeDownloadOptions): Promise<ArtifactRangeDownload> {
    const path = `api/v1/artifacts/${endpointID(id)}/download`;
    if (options.maxBytes !== undefined && (!Number.isSafeInteger(options.maxBytes) || options.maxBytes < 1)) throw new TypeError("maxBytes must be a positive safe integer");
    const scope = budget(options.timeoutMs ?? this.#transferMs, options.signal);
    let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
    let response: Response | undefined;
    try {
      await abortable(this.#ensureSession(), scope.controller.signal);
      const headers = this.#headers(false, {});
      if (range) headers.set("Accept-Encoding", "identity");
      if (range && range.offset > 0) { headers.set("Range", "bytes=" + range.offset + "-"); if (range.ifRange) headers.set("If-Range", range.ifRange); }
      response = await this.#fetch(new URL(path, this.#base), { headers, credentials: "include", redirect: "manual", signal: scope.controller.signal });
      if (response.status >= 300 && response.status < 400) { await response.body?.cancel(); throw new ApiError("Redirect responses are refused", { code: "redirect_refused", status: response.status }); }
      this.#captureSession(response);
      if (!response.ok) {
        const value = await boundedJSON(response);
        const envelope = object(value) && object(value.error) ? value.error : undefined;
        throw new ApiError(envelope && typeof envelope.message === "string" ? envelope.message : "Download failed", { code: envelope && typeof envelope.code === "string" ? envelope.code : "http_error", status: response.status, ...retryAfter(response) });
      }
      if (response.status !== 200 && !(range && range.offset > 0 && response.status === 206)) { await response.body?.cancel(); throw new ApiError("Download returned an incomplete response", { code: "invalid_response", status: response.status }); }
      const encoding = response.headers.get("content-encoding")?.trim().toLowerCase();
      if (range && encoding && encoding !== "identity") { await response.body?.cancel(); throw new ApiError("Ranged artifact transfer requires identity content encoding", { code: "content_encoding_unsupported", status: response.status }); }
      const header = response.headers.get("content-length");
      const length = header && /^\d+$/.test(header) && Number.isSafeInteger(Number(header)) ? Number(header) : null;
      let rangeStart = 0;
      let totalLength = length;
      if (response.status === 206) {
        const match = /^bytes ([0-9]+)-([0-9]+)\/([0-9]+)$/.exec(response.headers.get("content-range") ?? "");
        const start = Number(match?.[1]), end = Number(match?.[2]), total = Number(match?.[3]);
        if (!match || ![start,end,total].every(Number.isSafeInteger) || start !== range!.offset || end < start || end !== total - 1 || (length !== null && length !== end - start + 1) || (range!.expectedSize !== undefined && total !== range!.expectedSize) || (range!.ifRange !== undefined && response.headers.get("last-modified") !== range!.ifRange)) {
          await response.body?.cancel(); throw new ApiError("Range response does not match the saved artifact", { code: "invalid_range", status: response.status });
        }
        rangeStart = start; totalLength = total;
      }
      if (range?.expectedSize !== undefined && response.status === 200 && totalLength !== null && totalLength !== range.expectedSize) { await response.body?.cancel(); throw new ApiError("Artifact size changed", { code: "artifact_changed", status: response.status }); }
      if (options.maxBytes !== undefined && totalLength !== null && totalLength > options.maxBytes) { await response.body?.cancel(); throw new ApiError("Download exceeded maxBytes", { code: "download_too_large", status: response.status }); }
      if (options.maxBytes !== undefined && length !== null && length > options.maxBytes) { await response.body?.cancel(); throw new ApiError("Download exceeded maxBytes", { code: "download_too_large", status: response.status }); }
      if (!response.body) throw new ApiError("Download has no response body", { code: "invalid_response", status: response.status });
      reader = response.body.getReader();
      let size = 0;
      let finished = false;
      let streamController: ReadableStreamDefaultController<Uint8Array>;
      const cleanup = () => { if (!finished) { finished = true; scope.controller.signal.removeEventListener("abort", abort); scope.dispose(); reader?.releaseLock(); } };
      const abort = () => {
        if (finished) return;
        streamController.error(abortedError(scope));
        void reader!.cancel().catch(() => {}).finally(cleanup);
      };
      const stream = new ReadableStream<Uint8Array>({
        start(controller) { streamController = controller; scope.controller.signal.addEventListener("abort", abort, { once: true }); if (scope.controller.signal.aborted) abort(); },
        async pull(controller) {
          try {
            const item = await reader!.read();
            if (finished || scope.controller.signal.aborted) return;
            if (item.done) { cleanup(); if (length !== null && size !== length) controller.error(new ApiError("Download ended before its advertised length", { code: "invalid_response" })); else controller.close(); return; }
            size += item.value.byteLength;
            if (options.maxBytes !== undefined && size > options.maxBytes) {
              await reader!.cancel(); cleanup(); controller.error(new ApiError("Download exceeded maxBytes", { code: "download_too_large" })); return;
            }
            controller.enqueue(item.value);
          } catch { if (finished || scope.controller.signal.aborted) return; cleanup(); controller.error(new ApiError("Download was interrupted", { code: "network_error" })); }
        },
        async cancel(reason) { try { await reader!.cancel(reason); } finally { cleanup(); scope.controller.abort(); } },
      }, { highWaterMark: 0 });
      return { stream, contentType: response.headers.get("content-type"), contentLength: length, status: response.status as 200 | 206, rangeStart, totalLength, lastModified: response.headers.get("last-modified"), cancel: async reason => { await stream.cancel(reason); } };
    } catch (error) {
      scope.dispose();
      if (scope.controller.signal.aborted) throw abortedError(scope);
      if (error instanceof ApiError) throw new ApiError(error.message, { code: error.code, ...(error.status === undefined ? (response ? { status: response.status } : {}) : { status: error.status }), ...(response ? retryAfter(response) : {}) });
      throw new ApiError("Download could not be completed", { code: "network_error" });
    }
  }
}
