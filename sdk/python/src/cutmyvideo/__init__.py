"""Embed cutmyvideo via its local binary or owner-scoped HTTP API."""

from .client import CutmyClient
from .errors import APIError, CollectionError, CutmyError, JobError, LocalError, SourcePreparationError, WaitCancelled, WaitTimeout, ready_artifacts
from .local import LocalCutmy
from .projects import export_portable_project, import_portable_project
from .types import (
    Artifact, BatchOutput, BatchResult, Crop, ExportRequest, Job, JobItem,
    LocalPreflightResult, LocalSourceInfo, NamedRange, Plan, PlanOptions,
    PreflightCheck, PreflightResult, PreflightVideo, PreflightWarning, Range, Segment, Source,
    SourceInfo, Track, TrackReport, TrackSelection,
    CollectionOptions, CollectionClip, CollectionPlan, CollectionRequest, CollectionDiagnostic,
    CollectionValidation, CollectionSubmission, CollectionOutput, LocalCollectionResult,
    LocalCollectionPreflight, CollectionRunOutput, CollectionRunReport, CollectionPending, CollectionDownload,
    ContentFingerprint, ProviderFingerprint, SourceFingerprint, SourcePreparationInput, SourcePreparationReport,
    SubtitleMap, SubtitleRequest, SubtitleOutput, SubtitleResult,
    SourcePreparationReceipt, AgentProject, ProjectClip, ProjectJob, ProjectClipJob, ProjectCreateRequest, ProjectEditRequest, ProjectHandoff,
    AudioNormalize, Canvas, AudioReplace, LocalAudioReplace, AnalysisOptions, SilenceOptions, SilenceResult,
    VideoAnalysisOptions, VideoAnalysisResult, SceneChange, LoudnessOptions, LoudnessResult,
    CutList, CutListEntry, CutListImportRequest, CutListExportRequest, LocalCutListExportRequest, CutListExportResult,
)

__all__ = [
    "CutmyClient", "LocalCutmy", "APIError", "CutmyError", "JobError", "LocalError",
    "WaitCancelled", "WaitTimeout", "ready_artifacts", "Artifact", "BatchOutput", "BatchResult",
    "Crop", "ExportRequest", "Job", "JobItem", "LocalPreflightResult", "LocalSourceInfo",
    "NamedRange", "Plan", "PlanOptions", "PreflightCheck", "PreflightResult", "PreflightVideo", "PreflightWarning",
    "Range", "Segment", "Source", "SourceInfo", "Track", "TrackReport", "TrackSelection",
    "CollectionError", "CollectionOptions", "CollectionClip", "CollectionPlan", "CollectionRequest",
    "CollectionDiagnostic", "CollectionValidation", "CollectionSubmission", "CollectionOutput",
    "LocalCollectionResult", "LocalCollectionPreflight", "CollectionRunOutput", "CollectionRunReport",
    "CollectionPending", "CollectionDownload",
    "SourcePreparationError", "ContentFingerprint", "ProviderFingerprint", "SourceFingerprint", "SourcePreparationInput", "SourcePreparationReport",
    "SubtitleMap", "SubtitleRequest", "SubtitleOutput", "SubtitleResult",
    "export_portable_project", "import_portable_project",
    "SourcePreparationReceipt", "AgentProject", "ProjectClip", "ProjectJob", "ProjectClipJob", "ProjectCreateRequest", "ProjectEditRequest", "ProjectHandoff",
    "AudioNormalize", "Canvas", "AudioReplace", "LocalAudioReplace", "AnalysisOptions", "SilenceOptions", "SilenceResult",
    "VideoAnalysisOptions", "VideoAnalysisResult", "SceneChange", "LoudnessOptions", "LoudnessResult",
    "CutList", "CutListEntry", "CutListImportRequest", "CutListExportRequest", "LocalCutListExportRequest", "CutListExportResult",
]
