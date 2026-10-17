#!/usr/bin/env python3
"""Require real, unskipped native tests, including the complete API/worker path."""
import json
import sys
from pathlib import Path

APP_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/internal/app"
ENGINE_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/pkg/engine"
FS_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/internal/fsdurable"
CLI_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/cmd/cutmy"
SUBTITLE_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/internal/subtitles"
TOOLRUN_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/internal/toolrun"
MEDIAINFO_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
MEDIAEXPORT_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
MEDIAPREFLIGHT_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/internal/mediapreflight"
MEDIASESSION_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/internal/mediasession"
COREMODEL_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/internal/coremodel"
MEDIAERROR_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
LOCALMEDIA_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
EDIT_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/internal/edit"

CORE15_ENGINE_REQUIRED = {
    "TestAnalysisSilenceActualAntiphaseTimelineAndExecutablePlan",
    "TestAnalysisSilenceAllQuietAllAudibleAndExplicitTrack",
    "TestAnalysisSilenceReportsUncoveredVideoTail",
    "TestAnalysisVideoActualSceneAndBlackTimeline",
    "TestAnalysisPlanBoundsAndSelectedTrack",
    "TestAnalysisDetectsSourceMutationDuringDecode",
    "TestAnalysisEngineInvalidInputCancellationAndDeadlines",
    "TestRenderCanvasAndExternalAudioTimelineActualMedia",
    "TestRenderMeasuredNormalizationAndLoudnessActualMedia",
    "TestRenderRejectsIncompatibleOptionsBeforePublication",
    "TestRenderExternalTrackClockAndInputFences",
    "TestRenderCanvasPreservesKnownHDRAndVariableClocks",
    "TestRenderLoudnessWindowSelectionAndActualCoverage",
    "TestRenderOptionsRefusePreservedTextWithoutLosingExistingCopySupport",
    "TestCutListPlanRetainsEditableOrderAndMarkers",
    "TestCutListInterchangeTypedRefusals",
    "TestCutListChaptersAcceptedByIndependentFFmpeg",
}
CORE15_LOCALMEDIA_REQUIRED = {
    "TestAnalysisPCMIndependentChannelEnergyAndSplitWrites",
    "TestAnalysisPCMRejectsPartialNonFiniteAndBudgetOverflow",
    "TestAnalysisKeepMarginsAndEmptyTimeline",
    "TestAnalysisKeepEventBudgetRefusesUntruncatedAlternatingAudio",
    "TestAnalysisOptionsAndWindowBudgets",
    "TestAnalysisVideoStreamingParserIndependentBoundaries",
    "TestAnalysisVideoParserRejectsMalformedAndOversizedStreams",
    "TestAnalysisWriterRefusalCancelsAndRetainsCause",
    "TestAnalysisRuntimeCancelsLiveDecoderAndBudgetRefusal",
}
CORE15_EDIT_REQUIRED = {
    "TestCutListCSVIndependentSegmentsAndMarkers",
    "TestCutListDecimalMillisecondsHaveNoFloatOrOctalDrift",
    "TestCutListChapterIndependentClockAndEscapes",
    "TestCutListOutputChaptersUseActualClock",
    "TestCutListChapterLineEndingsAndLiteralCarriageReturn",
    "TestCutListRejectsInvalidOrExcessiveInput",
    "TestRenderValidationAndCollectionIsolation",
    "TestRenderReplacementJSONDoesNotTurnNullIntoZeroOffset",
}
CORE15_MEDIAEXPORT_REQUIRED = {
    "TestLoudnessReportParsesCompleteBoundedMachineBlock",
    "TestRenderedExportRefusesUnavailableOptionalRuntime",
    "TestRenderedExportRefusesSubtitlePreservationBeforeEffects",
}
CORE15_CLI_REQUIRED = {
    "TestNewInterfacesCLIActualAnalysisAndExplicitTransforms",
    "TestNewInterfacesCLICutListRoundTripAndActualOutputClock",
    "TestNewInterfacesCLIStrictFlagsAndJSONErrors",
    "TestNewInterfacesCLIRelativeReplacementInputsAndLocalIsolation",
}
CORE15_SERVICE_REQUIRED = {
    "TestNewInterfacesHTTPAuthenticationAndStrictObjects",
    "TestNewInterfacesHTTPCutListMarkersAndOutputClockRoundTrip",
    "TestNewInterfacesHTTPCutListBoundedText",
    "TestNewInterfacesHTTPActualAnalysisOwnerIsolationAndCleanup",
    "TestNewInterfacesHTTPAnalysisRemoteRemovedAndRateGuards",
    "TestNewInterfacesHTTPAnalysisAdmissionAndCancellationRelease",
    "TestRenderServicePreflightReferencesAndCleanup",
    "TestRenderServiceProjectGrantDoesNotAuthorizeSecondaryMedia",
    "TestRenderServiceWorkerTransformsAndRetainedRecovery",
    "TestRenderServiceCanvasAndReplacementOwnDecoderBudget",
    "TestRenderServiceCollectionReplacementIsolation",
    "TestRenderServiceDiagnosticsKeepPublicCodesAndPrivateCauses",
}
CORE15_BOOKWORM_REQUIRED = (CORE15_ENGINE_REQUIRED | CORE15_LOCALMEDIA_REQUIRED | CORE15_EDIT_REQUIRED
                            | CORE15_MEDIAEXPORT_REQUIRED | CORE15_CLI_REQUIRED)
EXTERNAL_AUDIO_STORAGE_REQUIRED = {"TestExternalAudioRuntimeDMLGrantsInvokeDerivedPinTriggers"}
EXTERNAL_AUDIO_RECOVERY_REQUIRED = {
    "TestExternalAudioPreviousBinaryRefuses009WithoutWrites",
    "TestExternalAudioCoordinated009BackupRestorePreservesRowsAndMedia",
    "TestExternalAudioRuntimeDMLGrantsInvokeDerivedPinTriggers",
}

AUDIT_MEDIA_REQUIRED = {
    "TestHLSManifestRedirectResolvesFinalRelativeReferences",
    "TestHLSManifestRedirectRetainsTargetGuards",
    "TestHLSManifestRedirectRetainsRedirectLimit",
    "TestHLSManifestRedirectRetainsTransferBudgets",
}
PLATFORM_ORIGINAL_AUDIO_REQUIRED = {
    "TestPlatformOriginalAudioSelection",
    "TestPlatformOriginalAudioRanksSupportedInputs",
    "TestPlatformOriginalAudioSurvivesExtractorAndCache",
    "TestPlatformOriginalAudioInvalidatesPreviewCache",
}
AUDIT_SERVICE_REQUIRED = {
    "TestHTTPAgentProjectJobLimitRejectsAdmissionAtomicallyAndRetainsReplay",
    "TestHTTPAgentProjectConcurrentAdmissionsShareLastBindingSlot",
    "TestHTTPAgentProjectPartialAdmissionPreservesJobsAtBindingLimit",
    "TestProjectAdmissionRechecksScopeBeforeCommittingJob",
    "TestHTTPProjectAndPreparationRollbackHasIndependentBound",
}

PROJECT_CONTRACT_SERVICE_REQUIRED = {
    "TestProjectCollectionContractExamples",
    "TestProjectLifecycleRevokeAndDeletePreserveAcceptedWork",
    "TestProjectLifecycleAccessExpiryPrecedesAdmissionCleanup",
    "TestProjectLifecycleLiveProjectAndGrantDoNotPinSource",
}

EDIT_MODEL_SERVICE_REQUIRED = {
    "TestEditAdapterPreservesRequestNormalizationOrder",
    "TestEditAdapterPreservesCollectionErrorChains",
}
EDIT_MODEL_ENGINE_REQUIRED = {"TestEngineEditModelPreservesPublicShapes"}
ENGINE_MEDIA_REQUIRED = {
    "TestAdvancedCropPlanPublishesActualCroppedOutput",
    "TestEngineInspectSeparatesCodedAndDisplayDimensions",
}
SUBTITLE_MODULE_REQUIRED = {"TestSubtitlePackageHasNoRuntimeDependencies"}
MEDIA_BOUNDARY_ENGINE_REQUIRED = {"TestEngineSubtitleBoundaryPreservesPublicContracts"}
TOOLRUN_MODULE_REQUIRED = {
    "TestToolrunPackageUsesOnlyStandardLibrary",
    "TestRunOutputPreservesRawFailureAndStreaming",
    "TestRunOutputPreservesCancellation",
    "TestDiagnosticCopyCannotBypassMemoryBound",
}
TOOLRUN_LIFECYCLE_REQUIRED = {"TestRunOutputCleansDescendantsAfterParentExit"}
MEDIAINFO_MODULE_REQUIRED = {
    "TestInfoValidationPreservesProbeBoundaries",
    "TestInfoDurationAndSelectionPreserveSourceOrder",
    "TestTrackDecisionPreservesManifestAndErrorContracts",
    "TestMediaInfoPackageHasPureDependencies",
}
MEDIAINFO_ADAPTER_REQUIRED = {
    "TestMediaInfoErrorBridgePreservesLegacyTypesAndCauses",
    "TestMediaInfoGeometryPreservesValidationOrder",
}
MEDIAEXPORT_MODULE_REQUIRED = {
    "TestSingleExportPreservesCopyClockAndVerificationOrder",
    "TestSingleExportPreservesFailurePrecedenceAndErrorIdentity",
    "TestSingleExportPreservesValidationAndCancellationOrder",
    "TestSingleExportPreservesRenderProfilesAndIntermediateOutputs",
    "TestMediaExportPackageHasNoIOOrServiceDependencies",
}
MEDIAEXPORT_ADAPTER_REQUIRED = {
    "TestMediaExportBridgePreservesLegacyErrorsAndRuntime",
    "TestMediaExportBackendPreservesProcessPaths",
}
JOINED_MEDIAEXPORT_REQUIRED = {
    "TestJoinedExportPreservesAdmissionOrderSubtitlesAndCleanup",
    "TestJoinedCopyPreservesMeasuredTimelineReuseAndBudget",
    "TestJoinedAccuratePreservesPlanRestrictionsAndVerification",
    "TestJoinedCopyAudioPreservesCapabilityFallbackAndStages",
    "TestJoinedVerificationPreservesBoundsAndCausePrecedence",
    "TestJoinedPlanPreservesEscapingFingerprintsAndPacketEligibility",
}
MEDIAPREFLIGHT_MODULE_REQUIRED = {
    "TestPreflightPlanPreservesRequestOwnershipAndDecisionOrder",
    "TestPreflightCapabilitiesPreserveExactTablesAndRequirements",
    "TestPreflightBatchPreservesUnionAndFailureOrder",
    "TestMediaPreflightPackageHasNoIOOrServiceDependencies",
}
MEDIAPREFLIGHT_ADAPTER_REQUIRED = {
    "TestMediaPreflightBridgePreservesLegacyShapesAndCauses",
}
MEDIASESSION_MODULE_REQUIRED = {
    "TestSessionPreservesProbeIdentityAndCancellation",
    "TestSessionPreservesBoundedKeyframeLookahead",
    "TestSessionPreservesSubtitleCacheOwnershipAndCleanup",
    "TestSessionReusePreservesIndependentFilesAndFallback",
    "TestMediaSessionPackageHasNoServiceDependencies",
}
SOURCE_SUBTITLE_MODULE_REQUIRED = {
    "TestExtractedSourceParserPreservesLegacyTolerance",
}
MEDIASESSION_ADAPTER_REQUIRED = {
    "TestMediaSessionBridgePreservesLegacyTypesAndCallbacks",
}
COREMODEL_MODULE_REQUIRED = {
    "TestCoreModelPreservesLegacyValidationAndShapes",
    "TestCoreModelPackageHasNoServiceDependencies",
}
MEDIAERROR_MODULE_REQUIRED = {
    "TestMediaErrorPreservesClassificationAndCauses",
    "TestMediaErrorPackageUsesOnlyStandardLibrary",
}
LOCALMEDIA_MODULE_REQUIRED = {
    "TestLocalMediaSessionPreservesOwnershipAndFreshness",
    "TestLocalMediaProcessPreservesBoundsStreamingAndObserver",
}
COREMODEL_MEDIAERROR_ADAPTER_REQUIRED = {
    "TestCoreModelAndMediaErrorAdaptersPreserveLegacyContracts",
}
ENGINE_DEPENDENCY_REQUIRED = {
    "TestEngineRuntimeDependencyBoundary",
    "TestEngineExternalConsumerPreservesPublicContracts",
}

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
    "TestJoinedAccurateDiscoveryDecoderBudgetPreservesEncoding",
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


# Pool lifecycle must retain explicit operator settings and acquired work while
# retiring only genuinely released idle connections on every native service host.
POOL_SERVICE_REQUIRED = {
    "TestStorePoolIdleDefaultsPreserveExplicitDurations",
    "TestStorePoolInvalidIdleDurationsRemainParserErrors",
    "TestStorePoolIdleRetirementPreservesAcquiredTransaction",
}

# Transfer observation is portable; the same owner/project metadata lookup also
# requires an actual PostgreSQL service on every native host and on Bookworm.
DRAFT_CACHE_TRANSFER_REQUIRED = {
    "TestDraftCacheWrappedFullTransferAndFlushError",
    "TestDraftCacheTransferWriterPreservesInterfacesAndCountsErrors",
    "TestDraftCacheFullTransferGuards",
    "TestDraftCacheSourceAndFinalServingUnchanged",
}
DRAFT_CACHE_DATABASE_REQUIRED = {"TestDraftCacheMetadataOwnerLegacyAndScopedDownload"}

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
     "TestMediaContractCopyQualityUsesSelectedVideo",
     "TestHLSMediaContractRotationAndSAR", "TestHLSMediaContractDefaultAudio", "TestHLSMediaContractHDRTags",
     "TestHLSMediaContractContinuityMetadata", "TestHLSMediaContractFingerprintUsesSelectedTracks",
     "TestHDRCapabilityFailureKeepsProcessCause", "TestHDRFailurePreservesCancellationAndLimits",
     "TestBatchAudioSelectionSubtitlesAndJoinedTimeline", "TestJoinedRemoteEntryKeepsMediaFormatAndProtocolRestrictions"},
]

REQUIRED[0].add("TestParseCrop")
REQUIRED[0].update(ENGINE_MEDIA_REQUIRED)
REQUIRED[2].update(NEW_SERVICE_REQUIRED)
REQUIRED[4].update(NEW_MEDIA_REQUIRED)
REQUIRED[2].update(EFFICIENT_CUTTING_SERVICE_REQUIRED)
REQUIRED[3].update(EFFICIENT_CUTTING_STORAGE_REQUIRED)
REQUIRED[4].update(EFFICIENT_CUTTING_MEDIA_REQUIRED)

REQUIRED[0].update(UNIVERSAL_ENGINE_REQUIRED)
REQUIRED[2].update(UNIVERSAL_SERVICE_REQUIRED)
REQUIRED[3].update(UNIVERSAL_STORAGE_REQUIRED)
REQUIRED[4].update(UNIVERSAL_MEDIA_REQUIRED)

REQUIRED[0].update(PREFLIGHT_ENGINE_REQUIRED | PREFLIGHT_CLI_REQUIRED)
REQUIRED[2].update(PREFLIGHT_SERVICE_REQUIRED)
REQUIRED[2].update(POOL_SERVICE_REQUIRED)
REQUIRED[2].update(DRAFT_CACHE_TRANSFER_REQUIRED | DRAFT_CACHE_DATABASE_REQUIRED)
REQUIRED[4].update(PREFLIGHT_MEDIA_REQUIRED)
REQUIRED[0].update(COLLECTION_ENGINE_REQUIRED | COLLECTION_CLI_REQUIRED)
REQUIRED[2].update(COLLECTION_SERVICE_REQUIRED)
REQUIRED[0].update(SUBTITLE_CLI_REQUIRED)
REQUIRED[2].update(AGENT_PROJECT_SERVICE_REQUIRED | SUBTITLE_SERVICE_REQUIRED)
REQUIRED[3].update(AGENT_PROJECT_STORAGE_REQUIRED)
REQUIRED[2].update(AUDIT_SERVICE_REQUIRED)
REQUIRED[2].update(PROJECT_CONTRACT_SERVICE_REQUIRED)
REQUIRED[2].update(EDIT_MODEL_SERVICE_REQUIRED)
REQUIRED[0].update(EDIT_MODEL_ENGINE_REQUIRED)
REQUIRED[4].update(AUDIT_MEDIA_REQUIRED)
REQUIRED[4].update(PLATFORM_ORIGINAL_AUDIO_REQUIRED)
REQUIRED[0].update(SUBTITLE_MODULE_REQUIRED | MEDIA_BOUNDARY_ENGINE_REQUIRED)
REQUIRED[1].update(TOOLRUN_MODULE_REQUIRED)
REQUIRED[1].update(TOOLRUN_LIFECYCLE_REQUIRED)
REQUIRED[0].update(MEDIAINFO_MODULE_REQUIRED)
REQUIRED[4].update(MEDIAINFO_ADAPTER_REQUIRED)
REQUIRED[0].update(MEDIAEXPORT_MODULE_REQUIRED)
REQUIRED[4].update(MEDIAEXPORT_ADAPTER_REQUIRED)
REQUIRED[0].update(JOINED_MEDIAEXPORT_REQUIRED | MEDIAPREFLIGHT_MODULE_REQUIRED)
REQUIRED[4].update(MEDIAPREFLIGHT_ADAPTER_REQUIRED)
REQUIRED[0].update(MEDIASESSION_MODULE_REQUIRED | SOURCE_SUBTITLE_MODULE_REQUIRED)
REQUIRED[4].update(MEDIASESSION_ADAPTER_REQUIRED)
REQUIRED[0].update(COREMODEL_MODULE_REQUIRED | MEDIAERROR_MODULE_REQUIRED | LOCALMEDIA_MODULE_REQUIRED | ENGINE_DEPENDENCY_REQUIRED)
REQUIRED[4].update(COREMODEL_MEDIAERROR_ADAPTER_REQUIRED)
REQUIRED[0].update(CORE15_BOOKWORM_REQUIRED)
REQUIRED[2].update(CORE15_SERVICE_REQUIRED)
REQUIRED[3].update(EXTERNAL_AUDIO_STORAGE_REQUIRED)

REQUIRED_PACKAGES = [
    {ENGINE_PACKAGE, FS_PACKAGE, CLI_PACKAGE, SUBTITLE_PACKAGE, MEDIAINFO_PACKAGE, MEDIAEXPORT_PACKAGE, MEDIAPREFLIGHT_PACKAGE, MEDIASESSION_PACKAGE, COREMODEL_PACKAGE, MEDIAERROR_PACKAGE, LOCALMEDIA_PACKAGE, EDIT_PACKAGE},
    {APP_PACKAGE, TOOLRUN_PACKAGE},
    {APP_PACKAGE},
    {APP_PACKAGE},
    {APP_PACKAGE},
]

# The mixed reports must retain each test's owning package. Adding a standalone
# module must not let its similarly named test satisfy an engine or app result.
REQUIRED_TEST_PACKAGES = [
    {name: (COREMODEL_PACKAGE if name in COREMODEL_MODULE_REQUIRED else
            MEDIAERROR_PACKAGE if name in MEDIAERROR_MODULE_REQUIRED else
            LOCALMEDIA_PACKAGE if name in LOCALMEDIA_MODULE_REQUIRED | CORE15_LOCALMEDIA_REQUIRED else
            EDIT_PACKAGE if name in CORE15_EDIT_REQUIRED else
            MEDIASESSION_PACKAGE if name in MEDIASESSION_MODULE_REQUIRED else
            MEDIAPREFLIGHT_PACKAGE if name in MEDIAPREFLIGHT_MODULE_REQUIRED else
            MEDIAEXPORT_PACKAGE if name in MEDIAEXPORT_MODULE_REQUIRED | JOINED_MEDIAEXPORT_REQUIRED | CORE15_MEDIAEXPORT_REQUIRED else
            MEDIAINFO_PACKAGE if name in MEDIAINFO_MODULE_REQUIRED else
            SUBTITLE_PACKAGE if name in SUBTITLE_MODULE_REQUIRED | SOURCE_SUBTITLE_MODULE_REQUIRED else
            CLI_PACKAGE if name.startswith("TestCLI") or name == "TestParseCrop" or name in CORE15_CLI_REQUIRED else
            FS_PACKAGE if name == "TestConcurrentPublicationHasExactlyOneCompleteWinner" else
            ENGINE_PACKAGE) for name in REQUIRED[0]},
    {name: TOOLRUN_PACKAGE if name in TOOLRUN_MODULE_REQUIRED | TOOLRUN_LIFECYCLE_REQUIRED else APP_PACKAGE for name in REQUIRED[1]},
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
BOOKWORM_REQUIRED.update(DRAFT_CACHE_TRANSFER_REQUIRED)
BOOKWORM_REQUIRED.update(AUDIT_MEDIA_REQUIRED)


def verify_report(filename, names, packages, test_packages=None):
    events = [json.loads(line) for line in Path(filename).read_text(encoding="utf-8").splitlines() if line.strip()]
    failures = [event for event in events if event.get("Action") in {"fail", "skip"}]
    passed = {(event.get("Package"), event.get("Test")) for event in events if event.get("Action") == "pass"}
    completed = {event.get("Package") for event in events if event.get("Action") == "pass" and not event.get("Test")}
    expected_packages = test_packages or {}
    missing = {name for name in names if not any((package, name) in passed for package in
               ({expected_packages[name]} if name in expected_packages else packages))}
    if failures or missing or not packages <= completed:
        raise SystemExit(f"native verification incomplete: {filename}; missing={sorted(missing)}; incomplete_packages={sorted(packages - completed)}; failed/skipped={len(failures)}")


def verify(filenames):
    if len(filenames) != len(REQUIRED):
        raise SystemExit("expected media, process, service, storage and media-contract JSON test reports")
    for index, (filename, names, packages) in enumerate(zip(filenames, REQUIRED, REQUIRED_PACKAGES)):
        test_packages = REQUIRED_TEST_PACKAGES[index] if index < len(REQUIRED_TEST_PACKAGES) else None
        verify_report(filename, names, packages, test_packages)


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] == "--bookworm":
        verify_report(sys.argv[2], BOOKWORM_REQUIRED, {APP_PACKAGE})
        print(f"Bookworm's {len(BOOKWORM_REQUIRED)} required media and timeline checks passed without skips.")
    elif len(sys.argv) == 3 and sys.argv[1] == "--draft-cache-service":
        verify_report(sys.argv[2], DRAFT_CACHE_DATABASE_REQUIRED, {APP_PACKAGE})
        print("Bookworm draft artifact ownership, project scope and ZIP checks passed without skips.")
    elif len(sys.argv) == 3 and sys.argv[1] == "--core15-bookworm":
        verify_report(sys.argv[2], CORE15_BOOKWORM_REQUIRED,
                      {ENGINE_PACKAGE, LOCALMEDIA_PACKAGE, CLI_PACKAGE, EDIT_PACKAGE, MEDIAEXPORT_PACKAGE},
                      REQUIRED_TEST_PACKAGES[0])
        print("Bookworm's six core feature engine, CLI, streaming and interchange checks passed without skips.")
    elif len(sys.argv) == 3 and sys.argv[1] == "--preflight-contract":
        verify_report(sys.argv[2], PREFLIGHT_CONTRACT_REQUIRED | COLLECTION_CONTRACT_REQUIRED | AGENT_PROJECT_CONTRACT_REQUIRED, {APP_PACKAGE})
        print("Preflight, collection, agent project and subtitle public-handler OpenAPI samples passed without skips.")
    elif len(sys.argv) == 3 and sys.argv[1] == "--agent-project-recovery":
        verify_report(sys.argv[2], AGENT_PROJECT_RECOVERY_REQUIRED, {APP_PACKAGE})
        print("Schema8 previous-binary refusal, restricted runtime and actual PostgreSQL/media restore passed without skips.")
    elif len(sys.argv) == 3 and sys.argv[1] == "--external-audio-recovery":
        verify_report(sys.argv[2], EXTERNAL_AUDIO_RECOVERY_REQUIRED, {APP_PACKAGE})
        print("Schema9 previous-release refusal, derived runtime pins and coordinated media/database restore passed without skips.")
    else:
        verify(sys.argv[1:])
        print("Native media contract, filesystem, CLI, process tree, API/worker and storage checks passed without skips.")
