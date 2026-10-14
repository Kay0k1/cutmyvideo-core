/** Millisecond intervals refer to the original source; order and repeats matter. */
export interface Range { start_ms: number; end_ms: number; label?: string }
export interface Crop { x: number; y: number; width: number; height: number }
export interface ExportOptions {
  format?: "mp4" | "mp3" | "mkv";
  quality?: "best" | "1080p" | "720p";
  cut_mode?: "accurate" | "copy";
  audio_track?: number;
  no_audio?: boolean;
  subtitle_mode?: "omit" | "preserve";
  crop?: Crop;
}
export interface ExportRequest extends ExportOptions {
  source_id: string;
  ranges: Range[];
  format: "mp4" | "mp3" | "mkv";
  quality: "best" | "1080p" | "720p";
  cut_mode: "accurate" | "copy";
  output_mode?: "separate" | "joined";
  cleanup_source?: boolean;
}
/** Serialized responses can retain normalized empty defaults and additive fields. */
export type NormalizedRequest = Omit<ExportRequest, "output_mode" | "subtitle_mode"> & {
  output_mode?: "" | "separate" | "joined";
  subtitle_mode?: "" | "omit" | "preserve";
  [key: string]: unknown;
};
export interface Plan { version: 1; input: string; ranges: Range[]; options: ExportOptions; output_mode?: "separate" | "joined" }
export interface Track {
  index: number; type: string; codec: string; default: boolean;
  language?: string; title?: string; channels?: number; sample_rate?: string; attached_picture?: boolean;
  [key: string]: unknown;
}
export interface TrackSelection extends Track { included: boolean; reason: string }
export interface SourceInfo {
  duration_ms: number; width?: number; height?: number; display_width?: number; display_height?: number;
  [key: string]: unknown;
}
export interface InspectedSource extends SourceInfo { tracks: Track[] }
export interface Source extends SourceInfo {
  id: string; title: string; kind: string; provider: string;
  provider_video_id: string | null; source_url: string | null;
  preview_kind: string; preview_url: string | null; embed_url: string | null; thumbnail_url: string | null;
  expires_at: string | null; retention_blocked: boolean; media_removed?: boolean; tracks?: Track[];
}
export interface Tracks { tracks: Track[]; display_width?: number; display_height?: number; [key: string]: unknown }
export interface Session {
  ok: true;
  limits: { max_source_bytes: number; max_output_bytes: number; max_fetch_bytes: number; max_ranges: number; max_range_ms: number; max_job_ms: number; [key: string]: unknown };
  [key: string]: unknown;
}
export interface PreflightResult {
  version: 1; valid: true; source: SourceInfo; request: NormalizedRequest;
  requested_duration_ms: number; output_count: number; track_manifest: TrackSelection[];
  video?: { width: number; height: number; hdr_to_sdr: boolean; [key: string]: unknown };
  checks: { name: string; status: string; [key: string]: unknown }[];
  warnings: { code: string; message: string; [key: string]: unknown }[];
  [key: string]: unknown;
}
export interface LocalPreflightResult extends PreflightResult { output_paths: string[] }
export interface Segment { index: number; label: string; source_start_ms: number; source_end_ms: number; output_start_ms: number; output_end_ms: number; [key: string]: unknown }
export interface LocalOutput {
  path: string; size_bytes: number; actual_start_ms: number; actual_end_ms: number;
  label?: string; segments: Segment[]; track_manifest: TrackSelection[]; [key: string]: unknown;
}
export interface BatchResult { outputs: LocalOutput[]; incomplete?: boolean; [key: string]: unknown }
export interface Artifact {
  id: string; filename: string; size_bytes: number; download_url: string;
  actual_start_ms: number; actual_end_ms: number; expires_at: string | null;
  segments?: Segment[]; track_manifest?: TrackSelection[]; [key: string]: unknown;
}
export interface JobItem {
  id: string; label: string; start_ms: number; end_ms: number; status: string;
  artifact: Artifact | null; progress_ms?: number; message?: string; error_code?: string; [key: string]: unknown;
}
/** Unknown future states are preserved and polled until an explicit terminal state. */
export interface Job { id: string; status: string; stage: string; message: string; items: JobItem[]; storage_wait_until: string | null; [key: string]: unknown }
export interface RequestOptions { signal?: AbortSignal; timeoutMs?: number }
export interface SubmissionOptions extends RequestOptions { idempotencyKey: string }
export interface WaitOptions extends RequestOptions { pollIntervalMs?: number; onProgress?: (job: Job) => void | Promise<void> }
export interface DownloadOptions extends RequestOptions { maxBytes?: number }
export interface ArtifactDownload {
  stream: ReadableStream<Uint8Array>;
  contentType: string | null;
  contentLength: number | null;
  /** Cancel/release the response if the stream will not be consumed. */
  cancel(reason?: unknown): Promise<void>;
}
