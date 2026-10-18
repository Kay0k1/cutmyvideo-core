/** Validate bounded analysis and cut-list JSON without Node or media dependencies. */
import { deliveryOptions } from "./delivery.js";
import { ApiError } from "./errors.js";
import { JSON_LIMIT, object } from "./protocol.js";
import type { CutList, CutListExportRequest, CutListExportResult, CutListFormat, CutListImportRequest } from "./types.js";

export type AnalysisKind = "silence" | "video" | "loudness";
const fields: Record<AnalysisKind, string[]> = {
  silence: ["start_ms","end_ms","audio_track","threshold_db","min_silence_ms","keep_before_ms","keep_after_ms","min_clip_ms"],
  video: ["start_ms","end_ms","scene_threshold","black_pixel_threshold","black_picture_ratio","min_black_ms"],
  loudness: ["start_ms","end_ms","audio_track"],
};
const integer = (v: unknown): v is number => typeof v === "number" && Number.isSafeInteger(v) && v >= 0;
const number = (v: unknown): v is number => typeof v === "number" && Number.isFinite(v);
function invalid(message: string): never { throw new ApiError(message, { code: "invalid_response" }); }
export function analysisOptions(kind: AnalysisKind, value: unknown): Record<string, number> {
  if (!object(value) || Object.entries(value).some(([k,v]) => !fields[kind].includes(k) || !number(v) || (k.endsWith("_ms") || k === "audio_track") && !integer(v))) throw new TypeError("Analysis options require finite numeric fields for this analysis");
  return { ...value } as Record<string, number>;
}
export function analysisFlags(kind: AnalysisKind, value: unknown): string[] {
  return Object.entries(analysisOptions(kind, value)).flatMap(([key,v]) => {
    if (key === "end_ms" && v === 0) return [];
    return key === "start_ms" || key === "end_ms" ? ["--" + key.slice(0,-3), `${Math.floor(v / 1000)}.${String(v % 1000).padStart(3,"0")}`] : ["--" + key.replaceAll("_","-"), String(v)];
  });
}
function intervals(value: unknown, start: number, end: number, labels = false): boolean {
  return Array.isArray(value) && value.length <= 4096 && value.every(row => object(row) && integer(row.start_ms) && integer(row.end_ms) && start <= row.start_ms && row.start_ms < row.end_ms && row.end_ms <= end && (!labels || typeof row.label === "string"));
}
export function analysisResult<T>(kind: AnalysisKind, value: unknown): T {
  if (!object(value) || value.version !== 1 || !integer(value.start_ms) || !integer(value.end_ms) || !integer(value.analyzed_end_ms) || value.start_ms >= value.end_ms || value.analyzed_end_ms < value.start_ms || value.analyzed_end_ms > value.end_ms) invalid("Invalid analysis window response");
  const start = value.start_ms as number, end = value.analyzed_end_ms as number;
  if (kind === "silence") {
    if (!integer(value.audio_track) || !intervals(value.silence,start,end) || !intervals(value.keep,start,end,true)) invalid("Invalid silence analysis response");
  } else if (kind === "video") {
    if (!integer(value.video_track) || !intervals(value.black,start,end) || !Array.isArray(value.scenes) || value.scenes.length + (value.black as unknown[]).length > 4096 || !value.scenes.every(row => object(row) && integer(row.at_ms) && start <= row.at_ms && row.at_ms <= end && number(row.score) && row.score >= 0 && row.score <= 1)) invalid("Invalid video analysis response");
  } else if (!integer(value.audio_track) || !["integrated_lufs","true_peak_dbtp","loudness_range_lu"].every(k => k in value && (value[k] === null || number(value[k])))) invalid("Invalid loudness analysis response");
  return value as T;
}
export function cutFormat(value: unknown): CutListFormat {
  if (value !== "csv" && value !== "ffmetadata") throw new TypeError("Cut-list format must be csv or ffmetadata");
  return value;
}
export function cutList(value: unknown): CutList {
  if (!object(value) || value.version !== 1 || !Array.isArray(value.entries) || !value.entries.length || value.entries.length > 128 || !value.entries.every(row => object(row) && integer(row.start_ms) && "end_ms" in row && (row.end_ms === null || integer(row.end_ms) && row.end_ms > row.start_ms) && typeof row.label === "string" && new TextEncoder().encode(row.label).length <= 200)) invalid("Invalid cut-list response");
  return value as unknown as CutList;
}
export function cutImport(value: CutListImportRequest): CutListImportRequest {
  if (!object(value) || Object.keys(value).sort().join(",") !== "format,text" || typeof value.text !== "string" || new TextEncoder().encode(JSON.stringify(value)).length > JSON_LIMIT) throw new TypeError("Cut-list import requires bounded format and text");
  cutFormat(value.format);
  return { ...value };
}
export function cutExport(value: CutListExportRequest): CutListExportRequest {
  if (!object(value) || !["cut_list,format","format,segments"].includes(Object.keys(value).sort().join(",")) || new TextEncoder().encode(JSON.stringify(value)).length > JSON_LIMIT) throw new TypeError("Cut-list export requires exactly one cut_list or segments");
  cutFormat(value.format);
  if ("cut_list" in value) {
    try { cutList(value.cut_list); } catch { throw new TypeError("Invalid cut_list"); }
  } else if (!Array.isArray(value.segments) || !value.segments.length || value.segments.length > 128) throw new TypeError("segments must contain1..128 segment maps");
  return JSON.parse(JSON.stringify(value)) as CutListExportRequest;
}
export function cutExportResult(value: unknown, format: CutListFormat): CutListExportResult {
  if (!object(value) || value.version !== 1 || value.format !== format || typeof value.text !== "string" || new TextEncoder().encode(value.text).length > JSON_LIMIT) invalid("Invalid cut-list export response");
  return value as unknown as CutListExportResult;
}
export function planResult<T>(value: unknown): T {
  if (!object(value) || value.version !== 1 || typeof value.input !== "string" || !value.input || !object(value.options) || !Array.isArray(value.ranges) || !value.ranges.length || value.ranges.length > 128 || !intervals(value.ranges,0,Number.MAX_SAFE_INTEGER)) invalid("Invalid generated edit plan");
  return value as T;
}
export function renderOptionSets(value: unknown): Record<string, unknown>[] {
  if (!object(value)) return [];
  const rows = [value], plan = object(value.plan) ? value.plan : value;
  if (object(plan.options)) rows.push(plan.options);
  if (Array.isArray(plan.clips)) for (const clip of plan.clips) if (object(clip) && object(clip.options)) rows.push(clip.options);
  return rows;
}
/** JSON.stringify would silently replace nonfinite render numbers with null. */
export function renderFinite(value: unknown): void {
  for (const options of renderOptionSets(value)) deliveryOptions(options);
  for (const options of renderOptionSets(value)) for (const field of ["audio_normalize", "canvas", "audio_replace"]) {
    const edit = options[field];
    if (object(edit) && Object.values(edit).some(v => typeof v === "number" && !Number.isFinite(v))) throw new TypeError("Render options require finite numeric values");
  }
}
export function renderReferences<T>(value: T, local = false): T {
  renderFinite(value);
  for (const options of renderOptionSets(value)) {
    deliveryOptions(options);
    const replacement = options.audio_replace;
    if (replacement === undefined || replacement === null) continue;
    const field = local ? "input" : "source_id", forbidden = local ? "source_id" : "input";
    if (!object(replacement) || forbidden in replacement || typeof replacement[field] !== "string" || !replacement[field] || (replacement[field] as string).includes("\0")) throw new TypeError(local ? "audio_replace requires only a local input" : "HTTP audio_replace requires source_id and cannot open local paths");
  }
  return value;
}

export function posterOptions(value: unknown): { timestamp_ms: number; format: "jpeg" | "png" } {
  if (!object(value) || Object.keys(value).some(k => !["timestamp_ms","format"].includes(k)) || !Number.isSafeInteger(value.timestamp_ms) || Number(value.timestamp_ms) < 0 || value.format !== undefined && value.format !== "jpeg" && value.format !== "png") throw new TypeError("Poster requires a nonnegative integer output timestamp and jpeg|png");
  return { timestamp_ms: value.timestamp_ms as number, format: value.format === "png" ? "png" : "jpeg" };
}
export function posterResult<T>(value: unknown, expected: { timestamp_ms: number; format: string }, parent?: string): T {
  if (!object(value) || !Number.isSafeInteger(value.timestamp_ms) || Number(value.timestamp_ms) < 0 || !["jpeg","png"].includes(String(value.format)) || !["width","height","size_bytes"].every(k => Number.isSafeInteger(value[k]) && Number(value[k]) > 0) || Number(value.width) * Number(value.height) > 16777216 || Number(value.size_bytes) > 16777216 || value.timestamp_ms !== expected.timestamp_ms || value.format !== expected.format) invalid("Invalid poster dimensions, size, format or output clock");
  if (parent === undefined) {
    if (typeof value.path !== "string" || !value.path || value.path.includes("\0")) invalid("Invalid local poster path");
  } else if (value.parent_artifact_id !== parent || typeof value.id !== "string" || !/^[A-Za-z0-9_-]{1,160}$/.test(value.id) || typeof value.filename !== "string" || !value.filename || /[/\\\0]/.test(value.filename) || value.download_url !== `/api/v1/image-artifacts/${encodeURIComponent(value.id)}/download` || !(value.expires_at === null || typeof value.expires_at === "string")) invalid("Invalid owner-protected image artifact identity");
  return value as T;
}
