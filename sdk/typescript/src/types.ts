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
  rendition?: "final" | "draft";
}
/** Omitted collection options inherit; -1 resets automatic audio. */
export interface CollectionOptions {
  format?: "" | "mp4" | "mp3" | "mkv";
  quality?: "" | "best" | "1080p" | "720p";
  cut_mode?: "" | "accurate" | "copy";
  subtitle_mode?: "" | "omit" | "preserve";
  audio_track?: number | null;
  no_audio?: boolean | null;
  crop?: Crop | null;
  clear_crop?: boolean;
}
export interface CollectionClip { id: string; label?: string; ranges: Range[]; options?: CollectionOptions }
export interface CollectionPlan { version: 1; input?: string; options?: CollectionOptions; clips: CollectionClip[] }
export interface CollectionRequest { source_id: string; plan: CollectionPlan; clip_ids?: string[]; rendition?: "final" | "draft" }
export interface ContentFingerprint { kind: "content"; sha256: string; size_bytes: number }
export interface ProviderFingerprint { kind: "provider"; provider: string; provider_id: string; canonical_url_sha256: string }
export type SourceFingerprint = ContentFingerprint | ProviderFingerprint;
export type SourcePreparationInput = { kind: "upload"; path: string } | { kind: "import"; url: string };
export interface SourcePreparationReceipt { version: 1; key: string; state: "processing" | "ready" | "retryable" | "failed"; source_id?: string; source?: Source; fingerprint?: SourceFingerprint; retry_after_ms?: number; error?: { code: string; message: string; retryable: boolean } }
export interface SourcePreparationReport { complete: boolean; phase: string; source_id?: string; source?: Source; fingerprint?: SourceFingerprint; error?: { code: string; message?: string; retryable: boolean } }
export interface SourceSubmissionOptions extends RequestOptions { idempotencyKey?: string; fingerprint?: ContentFingerprint }
export interface SubtitleRequest { version: 1; format: "srt" | "vtt"; text: string; output_format?: "srt" | "vtt"; outputs: { clip_id: string; segments: Segment[] }[] }
export interface SubtitleResult { version: 1; outputs: { clip_id: string; format: "srt" | "vtt"; text: string; cue_count: number; sha256: string }[] }
export interface PortableProject { kind: "cutmy.collection.project"; version: 1; source: { fingerprint?: SourceFingerprint; duration_ms?: number; title?: string }; plan: CollectionPlan; clip_ids?: string[]; rendition?: "final" | "draft" }
export interface ProjectClipJob { clip_id: string; job_id: string }
export interface ProjectCreateRequest extends CollectionRequest { collection_key: string; job_ids?: string[]; clip_jobs?: ProjectClipJob[] }
export interface ProjectEditRequest { revision: number; plan: CollectionPlan; clip_ids?: string[]; rendition?: "final" | "draft" }
export interface AgentProject extends CollectionRequest { version: 1; id: string; revision: number; source: Source; rendition: "final" | "draft"; clips: { clip_id: string; request: NormalizedRequest }[]; jobs: { clip_id: string; current: boolean; request: NormalizedRequest; job: Job }[]; expires_at?: string; access_expires_at?: string }
export interface ProjectHandoff { project_id: string; token: string; expires_at: string }
export interface CollectionDiagnostic { code: string; message: string; clip_id?: string; field?: string; [key: string]: unknown }
export interface CollectionValidation {
  version: 1; valid: true; rendition: "final" | "draft"; requested_duration_ms: number;
  clips: { clip_id: string; label: string; preflight: PreflightResult; [key: string]: unknown }[];
  [key: string]: unknown;
}
export interface CollectionSubmissionClip {
  clip_id: string; label: string; requested_duration_ms: number; request: NormalizedRequest;
  job?: Job; error?: CollectionDiagnostic; retryable: boolean; [key: string]: unknown;
}
export interface CollectionSubmission {
  version: 1; rendition: "final" | "draft"; complete: boolean; clips: CollectionSubmissionClip[];
  /** Transport metadata retained for partial reports, including non-2xx admission. */
  http_status?: number; retry_after_seconds?: number; [key: string]: unknown;
}
export interface LocalCollectionOptions extends RequestOptions { clipIds?: string[]; rendition?: "final" | "draft"; maxOutputBytes?: number }
export interface LocalCollectionValidation {
  version: 1; valid: true; rendition: "final" | "draft"; source: SourceInfo; selected_clip_ids: string[];
  clips: { id: string; label?: string; requested_duration_ms: number; request: NormalizedRequest; output_path: string; track_manifest: TrackSelection[]; checks: PreflightResult["checks"]; warnings: PreflightResult["warnings"]; video?: PreflightResult["video"] }[];
}
export interface LocalCollectionOutput extends LocalOutput { id: string; requested_ranges: Range[] }
export interface LocalCollectionResult extends BatchResult { version: 1; rendition: "final" | "draft"; outputs: LocalCollectionOutput[] }
export interface CollectionFile {
  clip_id: string; job_id: string; artifact_id: string; path: string; size_bytes: number; sha256: string;
  segments: Segment[]; track_manifest: TrackSelection[];
}
export interface CollectionReport {
  complete: boolean; source_id: string; rendition: "final" | "draft"; outputs: CollectionFile[];
  pending: { clip_id: string; code: string; retryable: boolean; stage?: string; job_id?: string; job_status?: string; progress_ms?: number; error_code?: string; field?: string; message?: string; diagnostic_clip_id?: string }[];
  phase?: string;
  download?: { clip_id: string; artifact_id: string; offset: number; total_size: number; resumed: boolean };
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
  fingerprint?: SourceFingerprint;
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
export interface RangeDownloadOptions extends DownloadOptions { offset: number; ifRange?: string; expectedSize?: number }
export interface ArtifactRangeDownload extends ArtifactDownload {
  status: 200 | 206; rangeStart: number; totalLength: number | null; lastModified: string | null;
}
export interface ArtifactDownload {
  stream: ReadableStream<Uint8Array>;
  contentType: string | null;
  contentLength: number | null;
  /** Cancel/release the response if the stream will not be consumed. */
  cancel(reason?: unknown): Promise<void>;
}
