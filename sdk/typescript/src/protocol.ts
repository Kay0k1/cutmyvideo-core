import { ApiError } from "./errors.js";

export const JSON_LIMIT = 1 << 20;
export function object(value: unknown): value is Record<string, unknown> { return typeof value === "object" && value !== null && !Array.isArray(value); }
export function parseJSON(text: string): unknown {
  try { return JSON.parse(text) as unknown; } catch { throw new ApiError("The server returned invalid JSON", { code: "invalid_response" }); }
}
/** Responses accept additive fields but require the shape clients rely on. */
export function decode<T>(value: unknown, kind: string): T {
  let valid = object(value);
  if (valid) {
    const v = value as Record<string, unknown>;
    switch (kind) {
      case "source": valid = typeof v.id === "string" && typeof v.duration_ms === "number"; break;
      case "sources": valid = Array.isArray(v.sources) && v.sources.every(x => object(x) && typeof x.id === "string" && typeof x.duration_ms === "number"); break;
      case "tracks": case "inspect": valid = Array.isArray(v.tracks) && v.tracks.every(x => object(x) && Number.isInteger(x.index) && typeof x.type === "string" && typeof x.codec === "string" && typeof x.default === "boolean") && (kind !== "inspect" || typeof v.duration_ms === "number"); break;
      case "session": valid = v.ok === true && object(v.limits) && ["max_source_bytes", "max_output_bytes", "max_fetch_bytes", "max_ranges", "max_range_ms", "max_job_ms"].every(key => typeof (v.limits as Record<string, unknown>)[key] === "number"); break;
      case "job": valid = typeof v.id === "string" && typeof v.status === "string" && typeof v.stage === "string" && typeof v.message === "string" && Array.isArray(v.items) && v.items.every(x => object(x) && typeof x.status === "string" && (x.artifact === null || (object(x.artifact) && typeof x.artifact.id === "string"))) && (v.storage_wait_until === null || typeof v.storage_wait_until === "string"); break;
      case "preflight": case "local-preflight": valid = v.version === 1 && v.valid === true && object(v.source) && typeof v.source.duration_ms === "number" && object(v.request) && Array.isArray(v.request.ranges) && Number.isInteger(v.output_count) && typeof v.requested_duration_ms === "number" && Array.isArray(v.track_manifest) && Array.isArray(v.checks) && v.checks.every(x => object(x) && typeof x.name === "string" && typeof x.status === "string") && Array.isArray(v.warnings) && v.warnings.every(x => object(x) && typeof x.code === "string" && typeof x.message === "string") && (kind !== "local-preflight" || (Array.isArray(v.output_paths) && v.output_paths.every(x => typeof x === "string"))); break;
      case "batch": valid = Array.isArray(v.outputs) && v.outputs.every(x => object(x) && typeof x.path === "string" && typeof x.size_bytes === "number" && typeof x.actual_start_ms === "number" && typeof x.actual_end_ms === "number" && Array.isArray(x.segments) && Array.isArray(x.track_manifest)); break;
      default: valid = false;
    }
  }
  if (!valid) throw new ApiError("The server returned an unexpected response", { code: "invalid_response" });
  return value as T;
}
export function positive(value: number, name: string): number {
  if (!Number.isFinite(value) || value <= 0 || value > 2_147_483_647) throw new TypeError(`${name} must be positive and at most 2147483647`);
  return value;
}
export function endpointID(id: string): string {
  if (typeof id !== "string" || !id || id === "." || id === "..") throw new TypeError("A resource ID is required");
  return encodeURIComponent(id);
}
export function retryAfter(response: Response): { retryAfterSeconds?: number } {
  const value = response.headers.get("retry-after");
  if (value === null) return {};
  const seconds = /^\d+$/.test(value) ? Number(value) : (Date.parse(value) - Date.now()) / 1000;
  return Number.isFinite(seconds) ? { retryAfterSeconds: Math.max(0, seconds) } : {};
}
export interface Budget { controller: AbortController; timeout: () => boolean; dispose: () => void }
export function budget(timeoutMs: number, caller?: AbortSignal): Budget {
  positive(timeoutMs, "timeoutMs");
  const controller = new AbortController();
  let timedOut = false;
  const aborted = () => controller.abort();
  if (caller?.aborted) controller.abort();
  else caller?.addEventListener("abort", aborted, { once: true });
  const timer = setTimeout(() => { timedOut = true; controller.abort(); }, timeoutMs);
  return { controller, timeout: () => timedOut, dispose: () => { clearTimeout(timer); caller?.removeEventListener("abort", aborted); } };
}
export function abortedError(b: Budget, context: { jobId?: string; idempotencyKey?: string } = {}): ApiError {
  return new ApiError(b.timeout() ? "The operation timed out" : "The operation was cancelled", { code: b.timeout() ? "timeout" : "cancelled", ...context });
}
export function abortable<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise((resolve, reject) => {
    const abort = () => reject(new Error("cancelled"));
    if (signal.aborted) { reject(new Error("cancelled")); return; }
    signal.addEventListener("abort", abort, { once: true });
    promise.then(resolve, reject).finally(() => signal.removeEventListener("abort", abort));
  });
}
export function delay(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const abort = () => { clearTimeout(timer); signal.removeEventListener("abort", abort); reject(new Error("cancelled")); };
    if (signal.aborted) { reject(new Error("cancelled")); return; }
    const timer = setTimeout(() => { signal.removeEventListener("abort", abort); resolve(); }, ms);
    signal.addEventListener("abort", abort, { once: true });
  });
}
export async function boundedJSON(response: Response): Promise<unknown> {
  const advertised = response.headers.get("content-length");
  if (advertised && /^\d+$/.test(advertised) && Number(advertised) > JSON_LIMIT) {
    await response.body?.cancel();
    throw new ApiError("The server response exceeded the JSON limit", { code: "response_too_large", status: response.status });
  }
  if (!response.body) throw new ApiError("The server returned no JSON body", { code: "invalid_response", status: response.status });
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    while (true) {
      const next = await reader.read();
      if (next.done) break;
      size += next.value.byteLength;
      if (size > JSON_LIMIT) { await reader.cancel(); throw new ApiError("The server response exceeded the JSON limit", { code: "response_too_large", status: response.status }); }
      chunks.push(next.value);
    }
  } finally { reader.releaseLock(); }
  const data = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) { data.set(chunk, offset); offset += chunk.byteLength; }
  return parseJSON(new TextDecoder().decode(data));
}
