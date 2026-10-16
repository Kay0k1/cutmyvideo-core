import { ApiError } from "./errors.js";
import { object } from "./protocol.js";
import type { ContentFingerprint, Source, SourceFingerprint, SourcePreparationReceipt } from "./types.js";

export function preparationKey(key: unknown): asserts key is string {
  if (typeof key !== "string" || !key || key === "." || key === ".." || key.length > 128 || !/^[\x21-\x7e]+$/.test(key)) throw new TypeError("Source preparation keys require 1..128 visible ASCII bytes without spaces or reserved dot paths");
}
export function sourceFingerprint(value: unknown): SourceFingerprint {
  if (object(value) && value.kind === "content" && typeof value.sha256 === "string" && /^[0-9a-f]{64}$/.test(value.sha256) && Number.isSafeInteger(value.size_bytes) && Number(value.size_bytes) > 0) return { kind: "content", sha256: value.sha256, size_bytes: Number(value.size_bytes) };
  if (object(value) && value.kind === "provider" && typeof value.provider === "string" && value.provider.length > 0 && value.provider.length <= 512 && typeof value.provider_id === "string" && value.provider_id.length > 0 && value.provider_id.length <= 512 && typeof value.canonical_url_sha256 === "string" && /^[0-9a-f]{64}$/.test(value.canonical_url_sha256)) return { kind: "provider", provider: value.provider, provider_id: value.provider_id, canonical_url_sha256: value.canonical_url_sha256 };
  throw new ApiError("Invalid source fingerprint", { code: "invalid_response" });
}
export function uploadFingerprint(value: unknown): ContentFingerprint {
  const fingerprint = sourceFingerprint(value);
  if (fingerprint.kind !== "content") throw new TypeError("Uploads require content fingerprints");
  return fingerprint;
}
export function publicPreparedSource(value: unknown): Source {
  if (!object(value) || typeof value.id !== "string" || !/^[A-Za-z0-9_-]{1,160}$/.test(value.id) || !Number.isSafeInteger(value.duration_ms) || Number(value.duration_ms) < 1) throw new ApiError("Invalid prepared source", { code: "invalid_response" });
  const source: Record<string, unknown> = {};
  for (const key of ["id","duration_ms","title","kind","provider","provider_video_id","source_url","preview_kind","preview_url","embed_url","thumbnail_url","expires_at","retention_blocked","media_removed","width","height"]) if (Object.hasOwn(value, key)) source[key] = value[key];
  for (const key of ["title","kind","provider","provider_video_id","source_url","preview_kind","preview_url","embed_url","thumbnail_url","expires_at"]) if (source[key] !== undefined && source[key] !== null && typeof source[key] !== "string") throw new ApiError("Invalid prepared source metadata", { code: "invalid_response" });
  for (const key of ["retention_blocked","media_removed"]) if (source[key] !== undefined && typeof source[key] !== "boolean") throw new ApiError("Invalid prepared source metadata", { code: "invalid_response" });
  for (const key of ["width","height"]) if (source[key] !== undefined && (!Number.isSafeInteger(source[key]) || Number(source[key]) < 0)) throw new ApiError("Invalid prepared source metadata", { code: "invalid_response" });
  if (value.fingerprint !== undefined) source.fingerprint = sourceFingerprint(value.fingerprint);
  return source as unknown as Source;
}
export function preparationReceipt(value: unknown, key: string): SourcePreparationReceipt {
  if (!object(value) || value.version !== 1 || value.key !== key || !["processing","ready","retryable","failed"].includes(String(value.state))) throw new ApiError("Invalid preparation receipt", { code: "invalid_response" });
  const result = { version: 1, key, state: value.state } as SourcePreparationReceipt;
  if (value.source_id !== undefined) {
    if (typeof value.source_id !== "string" || !/^[A-Za-z0-9_-]{1,160}$/.test(value.source_id)) throw new ApiError("Invalid preparation identity", { code: "invalid_response" });
    result.source_id = value.source_id;
  }
  if (value.source !== undefined) { result.source = publicPreparedSource(value.source); if (result.source_id && result.source_id !== result.source.id) throw new ApiError("Preparation identities differ", { code: "invalid_response" }); }
  if (value.fingerprint !== undefined) result.fingerprint = sourceFingerprint(value.fingerprint);
  if (value.retry_after_ms !== undefined) {
    if (!Number.isSafeInteger(value.retry_after_ms) || Number(value.retry_after_ms) < 0 || Number(value.retry_after_ms) > 86400000) throw new ApiError("Invalid preparation delay", { code: "invalid_response" });
    result.retry_after_ms = Number(value.retry_after_ms);
  }
  if (object(value.error) && typeof value.error.code === "string" && /^[a-z0-9_]{1,128}$/.test(value.error.code)) result.error = { code: value.error.code, message: typeof value.error.message === "string" && new TextEncoder().encode(value.error.message).length <= 4096 ? value.error.message : "Preparation needs repair", retryable: value.error.retryable === true };
  if (result.state === "failed" && !result.error) throw new ApiError("Preparation lacks a failure diagnostic", { code: "invalid_response" });
  return result;
}
