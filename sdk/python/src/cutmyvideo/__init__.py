"""Embed cutmyvideo via its local binary or owner-scoped HTTP API."""

from .client import CutmyClient
from .errors import APIError, CollectionError, CutmyError, JobError, LocalError, WaitCancelled, WaitTimeout, ready_artifacts
from .local import LocalCutmy
from .types import (
    Artifact, BatchOutput, BatchResult, Crop, ExportRequest, Job, JobItem,
    LocalPreflightResult, LocalSourceInfo, NamedRange, Plan, PlanOptions,
    PreflightCheck, PreflightResult, PreflightVideo, PreflightWarning, Range, Segment, Source,
    SourceInfo, Track, TrackReport, TrackSelection,
    CollectionOptions, CollectionClip, CollectionPlan, CollectionRequest, CollectionDiagnostic,
    CollectionValidation, CollectionSubmission, CollectionOutput, LocalCollectionResult,
    LocalCollectionPreflight, CollectionRunOutput, CollectionRunReport, CollectionPending, CollectionDownload,
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
]
