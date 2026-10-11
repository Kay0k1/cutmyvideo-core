#!/usr/bin/env python3
"""Require real, unskipped native tests, including the complete API/worker path."""
import json
import sys
from pathlib import Path

APP_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/internal/app"
ENGINE_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/pkg/engine"
FS_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
CLI_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/cmd/cutmy"

REQUIRED = [
    {"TestEngineInspectAndExportActualMedia", "TestConcurrentPublicationHasExactlyOneCompleteWinner", "TestCLIInspectAndClipActualMedia"},
    {"TestProcessCancellationTerminatesDescendants", "TestProcessOutputStopsUnboundedProducer"},
    {"TestNativeServiceUploadExportAndDownload", "TestPreviewMediaPolicyUpgradeDoesNotReuseLegacyCache",
     "TestLocalMediaDimensionsRefreshFencesLegacyDecoderCapacity",
     "TestLocalMediaDimensionsRefreshPreservesIdentityAndCancellation",
     "TestLocalMediaDimensionsRefreshKeepsUnknownInputConservative"},
    {"TestStorageAdmissionChecksFilesystemSpaceIndependentlyOfLogicalCap",
     "TestStorageAdmissionChecksNearestExistingFilesystem",
     "TestStorageAdmissionIncludesOutstandingReservationsAndSafetyHeadroom",
     "TestStorageBootstrapProgressSurvivesRestart", "TestStorageScanCanceledBatchDoesNotAdvance",
     "TestStorageBootstrapAmortizesLegacyExpiryAndResumesAfterCancellation", "TestStorageScanClassesRemainFairAcrossNewWorkAndRestart",
     "TestStorageScanPrefixChangeRestartsCoverage", "TestStorageReconcileCoverageGatesExpiredReservations",
     "TestStorageWorkProgressSurvivesRestartAndUnsafeCandidate", "TestStorageDeletePoisonBatchDoesNotStarveAcrossCyclesAndRestart",
     "TestStorageDeleteDirectorySyncFailureKeepsChargeUntilDurableRetry", "TestStorageDeleteLockAndCancellationFencePhysicalRemoval",
     "TestStorageDeleteStaleListCannotRemoveRegisteredReplacement", "TestStorageDeleteSerializesConcurrentReplacementRegistration",
     "TestStorageDeletePreservesAnotherSourceSharingTheFile", "TestStorageDeleteBackoffIsBoundedAndRetombstonePreservesSchedule",
     "TestStorageDeleteDuePickerUsesBoundedIndexBeforeFuturePoison",
     "TestStorageScanSessionReadsNamesLinearlyAcrossFairClasses", "TestStorageScanSessionRestartReplaysPrefixOnce",
     "TestStorageScanSessionDiscardsCanceledAndRacedBatches", "TestStorageScanSessionDiscardsUnknownCommitOutcome",
     "TestStorageScanSessionMembershipChangeRestartsCoverage", "TestStorageScanSessionRejectsStaleDirectoryIdentity",
     "TestStorageScanSessionWorkPathSwitchClosesReader", "TestStorageScanSessionPartialOperationsReleaseDirectories",
     "TestMetadataRetentionBatchesPreserveLiveCacheAndIdentity", "TestMetadataRetentionSkipsConcurrentRefreshLocks",
     "TestMetadataRetentionConcurrentCleanersPreserveFreshRows", "TestMetadataRetentionCacheFailureKeepsCommittedMediaPaths",
     "TestMetadataRetentionPreservesCancellationAndDatabaseErrors", "TestMetadataRetentionWorkerDrainsMediaAfterCacheFailure",
     "TestMigrationsUpgradeMaintenancePredecessorPreservesProgressRetriesAndCache",
     "TestMetadataCacheExpiryCorruptionAndFailureFallback",
     "TestSourcePinAuditOwnedBudgetKeepsContinuationAndAcknowledgedRepairs",
     "TestSourcePinAuditOwnedBudgetPreservesGenuineFailures",
     "TestSourcePinAuditParentCancellationHasPrecedence",
     "TestSourcePinAuditUnknownCommitResumesDurableState",
     "TestSourcePinAuditBlockedAfterMediaCommitKeepsDrainablePaths",
     "TestSourcePinAuditParentCanceledAfterMediaCommitKeepsDrainablePaths"},
    {"TestMediaContractRotationDisplayGeometry", "TestMediaContractSampleAspectNormalization",
     "TestMediaContractVariableFrameTimestamps", "TestMediaContractHDRToneMappingAndCopy",
     "TestMediaContractDefaultAudioSelection", "TestMediaContractAttachedPicturesAreNotVideo",
     "TestMediaContractCopyQualityUsesSelectedVideo", "TestEngineInspectSeparatesCodedAndDisplayDimensions",
     "TestHLSMediaContractRotationAndSAR", "TestHLSMediaContractDefaultAudio", "TestHLSMediaContractHDRTags",
     "TestHLSMediaContractContinuityMetadata", "TestHLSMediaContractFingerprintUsesSelectedTracks",
     "TestHDRCapabilityFailureKeepsProcessCause", "TestHDRFailurePreservesCancellationAndLimits"},
]

REQUIRED_PACKAGES = [
    {ENGINE_PACKAGE, FS_PACKAGE, CLI_PACKAGE},
    {APP_PACKAGE},
    {APP_PACKAGE},
    {APP_PACKAGE},
    {APP_PACKAGE, ENGINE_PACKAGE},
]

BOOKWORM_REQUIRED = {
    "TestCopyPreservesFirstKeyframeAndAudioTimeline", "TestHLSMiddleExportUsesGlobalTimelineAndRemovesStaging",
    "TestHLSPreservesCombinedIntentionalAudioDelay", "TestHLSContinuityPreservesTinyBridgeVideoAndAudioTimeline",
    "TestHLSContinuityRejectsChangedCodecAndCleansFailedGroups",
    "TestMediaContractRotationDisplayGeometry", "TestMediaContractSampleAspectNormalization",
    "TestMediaContractVariableFrameTimestamps", "TestMediaContractHDRToneMappingAndCopy",
    "TestMediaContractDefaultAudioSelection", "TestMediaContractAttachedPicturesAreNotVideo",
    "TestMediaContractCopyQualityUsesSelectedVideo",
    "TestHLSMediaContractRotationAndSAR", "TestHLSMediaContractDefaultAudio", "TestHLSMediaContractHDRTags",
    "TestHLSMediaContractContinuityMetadata", "TestHLSMediaContractFingerprintUsesSelectedTracks",
    "TestHDRCapabilityFailureKeepsProcessCause", "TestHDRFailurePreservesCancellationAndLimits",
}


def verify_report(filename, names, packages):
    events = [json.loads(line) for line in Path(filename).read_text(encoding="utf-8").splitlines() if line.strip()]
    failures = [event for event in events if event.get("Action") in {"fail", "skip"}]
    passed = {event.get("Test") for event in events if event.get("Action") == "pass"}
    completed = {event.get("Package") for event in events if event.get("Action") == "pass" and not event.get("Test")}
    if failures or not names <= passed or not packages <= completed:
        raise SystemExit(f"native verification incomplete: {filename}; missing={sorted(names - passed)}; incomplete_packages={sorted(packages - completed)}; failed/skipped={len(failures)}")


def verify(filenames):
    if len(filenames) != len(REQUIRED):
        raise SystemExit("expected media, process, service, storage and media-contract JSON test reports")
    for filename, names, packages in zip(filenames, REQUIRED, REQUIRED_PACKAGES):
        verify_report(filename, names, packages)


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] == "--bookworm":
        verify_report(sys.argv[2], BOOKWORM_REQUIRED, {APP_PACKAGE})
        print("Bookworm's 19 required media and timeline checks passed without skips.")
    else:
        verify(sys.argv[1:])
        print("Native media contract, filesystem, CLI, process tree, API/worker and storage checks passed without skips.")
