#!/usr/bin/env python3
"""Require real, unskipped native tests, including the complete API/worker path."""
import json
import sys
from pathlib import Path

APP_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/internal/app"
ENGINE_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/pkg/engine"
FS_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
CLI_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/cmd/cutmy"

NEW_MEDIA_REQUIRED = {
    "TestWaveformPCMStreamingBoundsAndSplitSamples", "TestWaveformMatchesIndependentAudioTimelineAndOffsets",
    "TestWaveformSelectsActualAudioTrack", "TestWaveformPreservesAntiphaseStereoActivity",
    "TestWaveformRetainsAudibleHighFrequencyActivity", "TestWaveformSourceRatePreservesClockAndChannelFrameBins",
    "TestCropRejectsInvalidCoordinatesAndModes", "TestCropResolutionCapIsAppliedAfterSelection",
    "TestCropFractionalBoundariesStayWithinOneDisplayedPixel",
    "TestMediaCropDecodedRegionMatchesDisplayedRotationAndSAR", "TestMediaCropRetainsHDRColorAndVariableFrameTiming",
    "TestPreviewGeometryIgnoresMalformedMetadataAndEnforcesSize", "TestPreviewScalingKeepsExactHeightCapDespiteFloatRounding",
}
NEW_SERVICE_REQUIRED = {
    "TestLocalProbeCacheReusesVerifiedFileAndInvalidatesChanges",
    "TestLocalProbeCacheBoundsMetadataAndDoesNotReuseOutputProbes",
    "TestVerifiedExportAdmissionDoesNotWaitForBusyPreviewPool",
    "TestSourceMediaReaderSaturationHasRetryableDiagnostic", "TestSourceMediaReaderRetentionIsBoundedWithoutCounterQuarantine",
    "TestExpiredSourceMediaReadersPruneInBoundedUnlockedBatches",
    "TestWaveformPrivateCacheAccountingAndDeletion", "TestPreviewOriginalDisplayGeometrySurvivesChargedToolFreeCache",
    "TestSourceMediaCleanupKeepsMetadataJobsArtifactsAndDownloads",
    "TestSourceMediaCleanupWaitsForReadersPreviewsAndAcceptedJobs",
    "TestSourceMediaCleanupFailureCancellationAndLegacyKeepSource",
    "TestSourceMediaCleanupPartialSuccessAndCancellationRaceKeepSource",
    "TestSourceMediaCleanupRestartTombstoneChargeAndPlatformUnavailable",
    "TestSourceMediaCleanupJobAdmissionAndCleanupShareFence", "TestSourceMediaCleanupMigrationUpgradeKeepsOldData",
    "TestSourceMediaCleanupJSONCompatibility", "TestSourceMediaCleanupRecoversPendingRequestAfterReaderCrash",
    "TestSourceMediaCleanupPreservesEarlierOverlappingFailure",
}

EFFICIENT_CUTTING_SERVICE_REQUIRED = {
    "TestArchivePreservesOrderAcrossPartialRetriesAndOwnerAfterMediaRemoval",
    "TestArchiveOpensAllFilesBeforeStreamingAndKeepsReadDescriptors",
    "TestQuickPreviewUsesIndependentAlignedCacheAndWaitsForTrackInspection",
    "TestWorkerSubtitleCacheRemainsChargedUntilFinalUnfinishedPublication",
}
EFFICIENT_CUTTING_MEDIA_REQUIRED = {
    "TestLocalExportSessionReusesOriginalAndVerifiesEveryJoinedResult",
    "TestLocalExportSessionExtractsSubtitlesOnceAndClipsEveryResult",
    "TestLocalExportSessionRejectsChangedSourceAndDoesNotCacheStages",
}
EFFICIENT_CUTTING_STORAGE_REQUIRED = {
    "TestAccurateJoinReservationOnlyIncludesActualProducers",
    "TestAccurateJoinCanBeClaimedWithRoomForItsRealOutput",
    "TestSeparateSubtitleReservationChargesSharedSourceTextOnce",
}

UNIVERSAL_ENGINE_REQUIRED = {
    "TestPlanRepeatedIntervalsProduceIndependentFilesAndPreserveLabels",
    "TestPlanRepeatedIntervalRegeneratesChangedPublishedResult",
}
UNIVERSAL_SERVICE_REQUIRED = {
    "TestWorkerReusesLocalProbeAcrossJobsAndStillVerifiesEachOutput",
    "TestWorkerRepeatedLocalRangesReuseOwnedPublicationWithIndependentFiles",
    "TestLazyThumbnailPresentationAcceptsOnlyCanonicalOwnerEndpoint",
    "TestLazyThumbnailImportPublishesMetadataWithoutImageWork",
    "TestLazyThumbnailOwnerRejectionPerformsNoResolutionOrFetch",
    "TestLazyThumbnailConcurrentGetsReuseFileAndCommitExactCharge",
    "TestLazyThumbnailCancelledBodyRemovesPartialAndReleasesPins",
    "TestLazyThumbnailRestartAndExpiredMetadataRefresh",
    "TestLazyThumbnailSmallQuotaBoundsActualTransfer",
    "TestLazyThumbnailDisabledQuotaKeepsImageAllowanceBounded",
    "TestLazyThumbnailDeadlineAndChangedIdentityDoNotFetch",
    "TestLazyThumbnailOversizedBodyCannotWriteBeyondAllowance",
    "TestLazyThumbnailUncertainAttachmentKeepsCompletedBytes",
}
UNIVERSAL_STORAGE_REQUIRED = {
    "TestMetadataResolutionForceFreshInvalidationAndWinnerFence",
    "TestMetadataResolutionForceFreshUnavailableClaimReturnsFreshWithoutCaching",
    "TestMetadataResolutionInvalidationWaitsForPublisherAndRemovesFreshCache",
    "TestMetadataResolutionRuntimeGrantsPermitLeaseCRUDWithoutOwnership",
    "TestMetadataResolutionDoesNotRetainPoolConnections",
    "TestMetadataResolutionExpiredAndInvalidatedOwnersCannotPublish",
    "TestMetadataResolutionMigrationPreserves006RowsAndRestartsWithoutWrites",
    "TestSourceMediaCleanupProtectedPrefixDoesNotHideEligibleRequests",
    "TestWorkerCompactProgressPreservesOtherItemsAndLeaseFencing",
    "TestFinalPlatformPublicationKeepsHLSCacheChargedUntilWorkspaceRemoval",
    "TestPlatformHLSCacheReservationIsBoundedSharedAndDisabledOnSmallStores",
    "TestPipelineInspectionSharesDecoderBudgetAndLegacyInspectionDoesNotReacquire",
    "TestCopyExportsRetainDecoderBudgetForCompositeVerification",
}
UNIVERSAL_MEDIA_REQUIRED = {
    "TestOwnedMP4MetadataMatchesFreshFFprobe",
    "TestJoinedMP4UsesFreshStructuralVerification",
    "TestOwnedMP4UnknownAndCorruptShapesKeepNativeVerification",
    "TestOwnedMP4FreshIdentityBoundsAndCancellation",
    "TestOwnedMP4ValidUnsupportedShapesUseFreshFallback",
    "TestOwnedMP4TrackAndEditRoundingMatchesFFprobe",
    "TestOwnedMP3UnknownProbeFamilyKeepsFreshVerification",
    "TestOwnedMP3FreshScanRejectsReplacedPathIdentity",
    "TestOwnedMP3MetadataMatchesFreshFFprobe",
    "TestJoinedMP3UsesFreshBoundedVerificationAndLargeFallback",
    "TestOwnedMP3UnprovedBytesKeepNativeVerification",
    "TestOwnedMP3ValidUnsupportedShapesUseFreshFallback",
    "TestOwnedMP3ReaderKeepsIOBoundAndCancellation",
    "TestJoinedAccurateDiscoveryPreservesReorderedVFRAndSilence",
    "TestJoinedAccurateDiscoveryPreservesDualInputClocks",
    "TestCopyJoinAudioPacketCopyPreservesClocks",
    "TestCopyJoinAudioPreservesFineSourcePacketClocks",
    "TestCopyPacketDropPreservesUnknownPresentationTimestamps",
    "TestCopyJoinAudioFallsBackWhenPacketTrimUnavailable",
    "TestCopyKeyframeSelectionKeepsInitialAndDecodeClocks",
    "TestLocalKeyframeCacheChecksSourceToolStreamAndCancellation",
    "TestLocalKeyframeLookaheadBoundsFallbackAndCacheLimit",
    "TestSubtitleIndexPreservesSpanningUnsortedRepeatedAndEmptyCues",
    "TestSourceSubtitleIndexReusesOnlyOneBoundedTrack",
    "TestCopyJoinReusesRepeatedSegmentsAndFaststartOnlyFinal",
    "TestParallelHLSAdvancesPastStragglerInsideBoundedWindow",
    "TestHLSJobCacheReusesBytesButKeepsIntervalAndNetworkBudgets",
    "TestHLSInitializationCacheHasImmutableMapAndBoundedPinnedEntries",
    "TestHLSJobCacheDisabledForSmallStores",
    "TestHLSPacketAnchorHonorsPreparationDecoderBudget",
    "TestPacketDropCapabilitiesCheckToolIdentityBoundsAndCancellation",
    "TestCopyPacketTrimEligibilityPreservesUnprovenClockPaths",
}


# Preflight must run on real media and expose deferred checks without rendering.
PREFLIGHT_ENGINE_REQUIRED = {
    "TestPlanJoinedParentFailureKeepsPublicationCode",
    "TestPreflightPlanMatchesExportAndLeavesNoFiles",
    "TestPreflightPlanInvalidSettingsAndOutputConflicts",
    "TestPreflightPlanCancellationAndToolErrors",
}
PREFLIGHT_CLI_REQUIRED = {
    "TestCLIPreflightPlanJSONAndNoOutput",
    "TestCLIPreflightStrictPlansAndDiagnostics",
}
PREFLIGHT_SERVICE_REQUIRED = {
    "TestHTTPPreflightRemoteMetadataAndGuards",
    "TestHTTPPreflightLocalReadOnlyAndAdmissionCleanup",
    "TestHTTPPreflightCancellationReleasesLeases",
}
PREFLIGHT_CONTRACT_REQUIRED = {"TestHTTPPreflightPublicContract"}
PREFLIGHT_MEDIA_REQUIRED = {
    "TestPreflightLocalChecksMediaWithoutRendering",
    "TestPreflightCapabilitiesParseExactLists",
    "TestPreflightCapabilitiesFailuresAreBoundedAndCancelable",
    "TestPreflightLocalRejectsChangingInput",
    "TestPreflightLocalBatchSharesFreshInspectionAndCapabilities",
    "TestPreflightLocalBatchPreservesClipErrorsAndOrdering",
    "TestPreflightLocalBatchDeferredFailuresAndCancellation",
    "TestPreflightLocalBatchRejectsChangedSourceAndTools",
    "TestPreflightLocalBatchRetainsOnlyRelevantCapabilityNames",
}

# The collection workflow must execute on every native platform; HTTP contract
# dependencies are installed only in the separate Linux contract job.
COLLECTION_ENGINE_REQUIRED = {
    "TestCollectionRoundTripSelectedDraftAndNoOverwrite",
    "TestCollectionInvalidSelectedAndGlobalShape",
    "TestCollectionPartialPublicationAndAggregateLimit",
    "TestCollectionCancellationAndTimeout",
    "TestCollectionDetectsSourceChangeAndLateCollision",
}
COLLECTION_CLI_REQUIRED = {
    "TestCLICollectionWorkflow", "TestCLICollectionStrictPlansAndDiagnostics",
}
COLLECTION_SERVICE_REQUIRED = {
    "TestHTTPCollectionsRemoteValidationAndGuards",
    "TestHTTPCollectionsSubmitRetryAndSelectedEdit",
    "TestHTTPCollectionsLocalPreflightAndDraft",
    "TestHTTPCollectionsBatchPreflightRejectsLateClipBeforeAdmission",
    "TestHTTPCollectionsPartialFailureRecovery",
    "TestHTTPCollectionsRetainedRecoveryAfterSourceChanges",
}
COLLECTION_CONTRACT_REQUIRED = {"TestHTTPCollectionsPublicContract"}

AGENT_PROJECT_SERVICE_REQUIRED = {
    "TestSourcePreparationsUploadLostReplyReplayAndFingerprint",
    "TestSourcePreparationLeaseFencingAndRetainedReceipt",
    "TestSourcePreparationKeyGrammarForPathsAndHeaders",
    "TestSourcePreparationsDirectAndProviderIdentityReplay",
    "TestHTTPAgentProjectHandoffIsolationEditsAndRepair",
    "TestHTTPAgentProjectExpiryAndPutMutationAdmission",
    "TestHTTPAgentProjectCreationRecoveryAndConcurrentHandoff",
    "TestHTTPAgentProjectAttachedArtifactsAndRange",
    "TestHTTPAgentProjectExplicitBindingsPreserveIdenticalClips",
    "TestHTTPAgentProjectExpandedPlanLimitIsCorrectiveAndAtomic",
    "TestAgentProjectPersistenceLimitRecognizesOnlyKnownConstraints",
}
AGENT_PROJECT_STORAGE_REQUIRED = {
    "TestAgentProjectsMigrationPreserves007RowsAndRestartsWithoutWrites",
    "TestAgentProjectsRuntimeGrantsPermitCRUDWithoutOwnership",
}
AGENT_PROJECT_RECOVERY_REQUIRED = {
    "TestAgentProjectsPreviousBinaryRefuses008WithoutWrites",
    "TestAgentProjectsRuntimeGrantsPermitCRUDWithoutOwnership",
    "TestAgentProjectsCoordinated008BackupRestorePreservesRowsAndMedia",
}
SUBTITLE_SERVICE_REQUIRED = {
    "TestRetimeSubtitlesActualMapsAndRepeatedReorderedCues",
    "TestRetimeSubtitlesVTTSettingsAndOverlappingCueIdentity",
    "TestRetimeSubtitlesConversionAndHalfOpenEmptyOutputs",
    "TestRetimeSubtitlesRejectsUnsupportedAndMalformedText",
    "TestRetimeSubtitlesMapValidationAndResourceBounds",
    "TestRetimeSubtitlesCancellationAndShortOutputClock",
    "TestSubtitleRetimePublicHandler",
    "TestSubtitleRetimePublicHandlerAllowsBoundedEscapedText",
}
SUBTITLE_CLI_REQUIRED = {"TestCLIRetimeSubtitlesWithoutToolsOrDatabase"}
AGENT_PROJECT_CONTRACT_REQUIRED = {"TestHTTPAgentProjectPublicContract", "TestHTTPSubtitleRetimePublicContract"}

REQUIRED = [
    {"TestEngineInspectAndExportActualMedia", "TestConcurrentPublicationHasExactlyOneCompleteWinner", "TestCLIInspectAndClipActualMedia", "TestCLIBatchSavedPlanRelativeInputAndJoinedAudio", "TestPlanRoundTripActualOutputsAndPartialPublication"},
    {"TestProcessCancellationTerminatesDescendants", "TestProcessOutputStopsUnboundedProducer",
     "TestProcessEnvironmentSanitizesRealChild", "TestProcessEnvironmentArenaBoundsAndPlatforms"},
    {"TestNativeServiceUploadExportAndDownload", "TestPreviewMediaPolicyUpgradeDoesNotReuseLegacyCache",
     "TestLocalMediaDimensionsRefreshFencesLegacyDecoderCapacity",
     "TestLocalMediaDimensionsRefreshPreservesIdentityAndCancellation",
     "TestLocalMediaDimensionsRefreshKeepsUnknownInputConservative",
     "TestHTTPTracksAreFreshOwnerProtectedAndRemoteExplicit",
     "TestSeparateBatchPartialFailureKeepsCompletedDownload",
     "TestJoinedLostPublicationAcknowledgementResumesWithoutRendering",
     "TestWorkerNotificationWakesAfterCommitAndClosesOnCancellation",
     "TestInsertedJobEmitsQueueNotification"},
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
     "TestStorageScanSessionDeadlineBatchesPreserveAcknowledgedPrefix",
     "TestStorageScanSchedulingBatchFailuresPreserveClocks",
     "TestStorageScanSessionReadBatchPreservesNonISODateStyle",
     "TestStorageScanSessionReadBatchLookupFailureRollsBackAndRecovers",
     "TestMetadataRetentionBatchesPreserveLiveCacheAndIdentity", "TestMetadataRetentionSkipsConcurrentRefreshLocks",
     "TestMetadataRetentionConcurrentCleanersPreserveFreshRows", "TestMetadataRetentionCacheFailureKeepsCommittedMediaPaths",
     "TestMetadataRetentionPreservesCancellationAndDatabaseErrors", "TestMetadataRetentionWorkerDrainsMediaAfterCacheFailure",
     "TestMigrationsUpgradeMaintenancePredecessorPreservesProgressRetriesAndCache",
     "TestMetadataCacheExpiryCorruptionAndFailureFallback",
     "TestSourceRetentionFalsePreviewZerosPreservePinsAndBatchLimit",
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
     "TestHDRCapabilityFailureKeepsProcessCause", "TestHDRFailurePreservesCancellationAndLimits",
     "TestBatchAudioSelectionSubtitlesAndJoinedTimeline", "TestJoinedRemoteEntryKeepsMediaFormatAndProtocolRestrictions"},
]

REQUIRED[0].add("TestParseCrop")
REQUIRED[2].update(NEW_SERVICE_REQUIRED)
REQUIRED[4].update(NEW_MEDIA_REQUIRED | {"TestAdvancedCropPlanPublishesActualCroppedOutput"})
REQUIRED[2].update(EFFICIENT_CUTTING_SERVICE_REQUIRED)
REQUIRED[3].update(EFFICIENT_CUTTING_STORAGE_REQUIRED)
REQUIRED[4].update(EFFICIENT_CUTTING_MEDIA_REQUIRED)

REQUIRED[0].update(UNIVERSAL_ENGINE_REQUIRED)
REQUIRED[2].update(UNIVERSAL_SERVICE_REQUIRED)
REQUIRED[3].update(UNIVERSAL_STORAGE_REQUIRED)
REQUIRED[4].update(UNIVERSAL_MEDIA_REQUIRED)

REQUIRED[0].update(PREFLIGHT_ENGINE_REQUIRED | PREFLIGHT_CLI_REQUIRED)
REQUIRED[2].update(PREFLIGHT_SERVICE_REQUIRED)
REQUIRED[4].update(PREFLIGHT_MEDIA_REQUIRED)
REQUIRED[0].update(COLLECTION_ENGINE_REQUIRED | COLLECTION_CLI_REQUIRED)
REQUIRED[2].update(COLLECTION_SERVICE_REQUIRED)
REQUIRED[0].update(SUBTITLE_CLI_REQUIRED)
REQUIRED[2].update(AGENT_PROJECT_SERVICE_REQUIRED | SUBTITLE_SERVICE_REQUIRED)
REQUIRED[3].update(AGENT_PROJECT_STORAGE_REQUIRED)

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
    "TestBatchAudioSelectionSubtitlesAndJoinedTimeline", "TestJoinedRemoteEntryKeepsMediaFormatAndProtocolRestrictions",
}

BOOKWORM_REQUIRED.update(NEW_MEDIA_REQUIRED)
BOOKWORM_REQUIRED.update(EFFICIENT_CUTTING_MEDIA_REQUIRED)
BOOKWORM_REQUIRED.update(UNIVERSAL_MEDIA_REQUIRED)
BOOKWORM_REQUIRED.update(PREFLIGHT_MEDIA_REQUIRED)


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
        print(f"Bookworm's {len(BOOKWORM_REQUIRED)} required media and timeline checks passed without skips.")
    elif len(sys.argv) == 3 and sys.argv[1] == "--preflight-contract":
        verify_report(sys.argv[2], PREFLIGHT_CONTRACT_REQUIRED | COLLECTION_CONTRACT_REQUIRED | AGENT_PROJECT_CONTRACT_REQUIRED, {APP_PACKAGE})
        print("Preflight, collection, agent project and subtitle public-handler OpenAPI samples passed without skips.")
    elif len(sys.argv) == 3 and sys.argv[1] == "--agent-project-recovery":
        verify_report(sys.argv[2], AGENT_PROJECT_RECOVERY_REQUIRED, {APP_PACKAGE})
        print("Schema8 previous-binary refusal, restricted runtime and actual PostgreSQL/media restore passed without skips.")
    else:
        verify(sys.argv[1:])
        print("Native media contract, filesystem, CLI, process tree, API/worker and storage checks passed without skips.")
