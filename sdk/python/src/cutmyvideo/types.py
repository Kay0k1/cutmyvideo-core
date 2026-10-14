"""Public JSON shapes. Strings remain open for additive server values."""

from typing import TypedDict


class Range(TypedDict):
    start_ms: int
    end_ms: int


class NamedRange(Range, total=False):
    label: str


class Crop(TypedDict):
    x: float
    y: float
    width: float
    height: float


class _ExportRequired(TypedDict):
    source_id: str
    ranges: list[NamedRange]
    format: str
    quality: str
    cut_mode: str


class ExportRequest(_ExportRequired, total=False):
    output_mode: str
    audio_track: int
    no_audio: bool
    subtitle_mode: str
    crop: Crop
    cleanup_source: bool


class PlanOptions(TypedDict, total=False):
    format: str
    quality: str
    cut_mode: str
    audio_track: int
    no_audio: bool
    subtitle_mode: str
    crop: Crop


class _PlanRequired(TypedDict):
    version: int
    input: str
    ranges: list[NamedRange]
    options: PlanOptions


class Plan(_PlanRequired, total=False):
    output_mode: str


class _TrackRequired(TypedDict):
    index: int
    type: str
    codec: str
    default: bool


class Track(_TrackRequired, total=False):
    language: str
    title: str
    channels: int
    sample_rate: str
    attached_picture: bool


class TrackSelection(Track):
    included: bool
    reason: str


class _SourceInfoRequired(TypedDict):
    duration_ms: int


class SourceInfo(_SourceInfoRequired, total=False):
    width: int
    height: int
    display_width: int
    display_height: int


class LocalSourceInfo(SourceInfo):
    tracks: list[Track]


class _SourceRequired(TypedDict):
    id: str
    title: str
    duration_ms: int
    kind: str
    provider: str
    provider_video_id: str | None
    source_url: str | None
    preview_kind: str
    preview_url: str | None
    embed_url: str | None
    thumbnail_url: str | None
    expires_at: str | None
    retention_blocked: bool


class Source(_SourceRequired, total=False):
    media_removed: bool
    width: int
    height: int
    tracks: list[Track]


class _TrackReportRequired(TypedDict):
    tracks: list[Track]


class TrackReport(_TrackReportRequired, total=False):
    display_width: int
    display_height: int


class Segment(TypedDict):
    index: int
    label: str
    source_start_ms: int
    source_end_ms: int
    output_start_ms: int
    output_end_ms: int


class _ArtifactRequired(TypedDict):
    id: str
    filename: str
    size_bytes: int
    download_url: str
    actual_start_ms: int
    actual_end_ms: int
    expires_at: str | None


class Artifact(_ArtifactRequired, total=False):
    segments: list[Segment]
    track_manifest: list[TrackSelection]


class _JobItemRequired(TypedDict):
    id: str
    label: str
    start_ms: int
    end_ms: int
    status: str
    artifact: Artifact | None


class JobItem(_JobItemRequired, total=False):
    progress_ms: int
    message: str
    error_code: str


class Job(TypedDict):
    id: str
    status: str
    stage: str
    message: str
    items: list[JobItem]
    storage_wait_until: str | None


class PreflightCheck(TypedDict):
    name: str
    status: str


class PreflightWarning(TypedDict):
    code: str
    message: str


class _PreflightRequired(TypedDict):
    version: int
    valid: bool
    source: SourceInfo
    request: ExportRequest
    requested_duration_ms: int
    output_count: int
    track_manifest: list[TrackSelection]
    checks: list[PreflightCheck]
    warnings: list[PreflightWarning]


class PreflightVideo(TypedDict):
    width: int
    height: int
    hdr_to_sdr: bool


class PreflightResult(_PreflightRequired, total=False):
    video: PreflightVideo


class LocalPreflightResult(PreflightResult):
    output_paths: list[str]


class _BatchOutputRequired(TypedDict):
    path: str
    size_bytes: int
    actual_start_ms: int
    actual_end_ms: int
    segments: list[Segment]
    track_manifest: list[TrackSelection]


class BatchOutput(_BatchOutputRequired, total=False):
    label: str


class BatchResult(TypedDict):
    outputs: list[BatchOutput]
