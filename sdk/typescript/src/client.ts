import { ApiError, JobError } from "./errors.js";
import { abortedError, abortable, boundedJSON, budget, decode, delay, endpointID, object, positive, retryAfter } from "./protocol.js";
import type { ArtifactDownload, DownloadOptions, ExportRequest, Job, PreflightResult, RequestOptions, Session, Source, SubmissionOptions, Tracks, WaitOptions } from "./types.js";

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
type Context = { jobId?: string; idempotencyKey?: string };

export class CutmyClient {
  readonly #base: URL;
  readonly #fetch: typeof globalThis.fetch;
  readonly #requestMs: number;
  readonly #sourceMs: number;
  readonly #transferMs: number;
  #cookie = "";
  #session: Promise<Session> | undefined;
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
    return headers;
  }
  #captureSession(response: Response): void {
    // Browser fetch owns HttpOnly cookies. Node fetch exposes them separately.
    for (const value of response.headers.getSetCookie?.() ?? []) {
      const match = /^cutmy_session=([A-Za-z0-9_-]+)(?:;|$)/.exec(value);
      if (match) this.#cookie = `cutmy_session=${match[1]}`;
    }
  }
  async #request<T>(method: string, path: string, data: unknown, kind: string, options: RequestOptions, context: Context = {}, initialize = true): Promise<T> {
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
      const value = await boundedJSON(response);
      if (!response.ok) {
        const envelope = object(value) && object(value.error) ? value.error : undefined;
        throw new ApiError(envelope && typeof envelope.message === "string" ? envelope.message : "The API request failed", { code: envelope && typeof envelope.code === "string" ? envelope.code : "http_error", status: response.status, ...retryAfter(response), ...context });
      }
      return decode<T>(value, kind);
    } catch (error) {
      if (scope.controller.signal.aborted) throw abortedError(scope, context);
      if (error instanceof ApiError) throw new ApiError(error.message, { code: error.code, ...(error.status === undefined ? (response ? { status: response.status } : {}) : { status: error.status }), ...(response ? retryAfter(response) : {}), ...context });
      throw new ApiError("The API request could not be completed", { code: "network_error", ...(response ? { status: response.status } : {}), ...context });
    } finally { scope.dispose(); }
  }
  async listSources(options: RequestOptions = {}): Promise<Source[]> { return (await this.#request<{ sources: Source[] }>("GET", "api/v1/sources", undefined, "sources", options)).sources; }
  importSource(url: string, options: RequestOptions = {}): Promise<Source> { return this.#request("POST", "api/v1/sources", { url }, "source", { ...options, timeoutMs: options.timeoutMs ?? this.#sourceMs }); }
  upload(file: Blob, filename = "upload", options: RequestOptions = {}): Promise<Source> {
    const form = new FormData(); form.append("file", file, filename);
    return this.#request("POST", "api/v1/uploads", form, "source", { ...options, timeoutMs: options.timeoutMs ?? this.#sourceMs });
  }
  getSource(id: string, options: RequestOptions = {}): Promise<Source> { return this.#request("GET", `api/v1/sources/${endpointID(id)}`, undefined, "source", options); }
  getTracks(id: string, options: RequestOptions = {}): Promise<Tracks> { return this.#request("GET", `api/v1/sources/${endpointID(id)}/tracks`, undefined, "tracks", { ...options, timeoutMs: options.timeoutMs ?? this.#sourceMs }); }
  preflight(request: ExportRequest, options: RequestOptions = {}): Promise<PreflightResult> { return this.#request("POST", "api/v1/jobs/preflight", request, "preflight", { ...options, timeoutMs: options.timeoutMs ?? this.#sourceMs }); }
  createJob(request: ExportRequest, options: SubmissionOptions): Promise<Job> {
    if (typeof options?.idempotencyKey !== "string" || !options.idempotencyKey.trim() || new TextEncoder().encode(options.idempotencyKey).byteLength > 128 || /[\x00-\x1f\x7f]/.test(options.idempotencyKey)) throw new TypeError("idempotencyKey must contain 1 to 128 UTF-8 bytes and no control characters");
    return this.#request("POST", "api/v1/jobs", request, "job", options, { idempotencyKey: options.idempotencyKey });
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
    const path = `api/v1/artifacts/${endpointID(id)}/download`;
    if (options.maxBytes !== undefined && (!Number.isSafeInteger(options.maxBytes) || options.maxBytes < 1)) throw new TypeError("maxBytes must be a positive safe integer");
    const scope = budget(options.timeoutMs ?? this.#transferMs, options.signal);
    let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
    let response: Response | undefined;
    try {
      await abortable(this.#ensureSession(), scope.controller.signal);
      response = await this.#fetch(new URL(path, this.#base), { headers: this.#headers(false, {}), credentials: "include", redirect: "manual", signal: scope.controller.signal });
      if (response.status >= 300 && response.status < 400) { await response.body?.cancel(); throw new ApiError("Redirect responses are refused", { code: "redirect_refused", status: response.status }); }
      this.#captureSession(response);
      if (!response.ok) {
        const value = await boundedJSON(response);
        const envelope = object(value) && object(value.error) ? value.error : undefined;
        throw new ApiError(envelope && typeof envelope.message === "string" ? envelope.message : "Download failed", { code: envelope && typeof envelope.code === "string" ? envelope.code : "http_error", status: response.status, ...retryAfter(response) });
      }
      if (response.status !== 200) { await response.body?.cancel(); throw new ApiError("Download returned an incomplete response", { code: "invalid_response", status: response.status }); }
      const header = response.headers.get("content-length");
      const length = header && /^\d+$/.test(header) && Number.isSafeInteger(Number(header)) ? Number(header) : null;
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
      return { stream, contentType: response.headers.get("content-type"), contentLength: length, cancel: async reason => { await stream.cancel(reason); } };
    } catch (error) {
      scope.dispose();
      if (scope.controller.signal.aborted) throw abortedError(scope);
      if (error instanceof ApiError) throw new ApiError(error.message, { code: error.code, ...(error.status === undefined ? (response ? { status: response.status } : {}) : { status: error.status }), ...(response ? retryAfter(response) : {}) });
      throw new ApiError("Download could not be completed", { code: "network_error" });
    }
  }
}
