"""Negative controls for the mandatory, unskipped native runtime gate."""
import importlib.util
import json
import re
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("check-native-results.py")
spec = importlib.util.spec_from_file_location("native_results", SCRIPT)
native_results = importlib.util.module_from_spec(spec)
spec.loader.exec_module(native_results)


class NativeResultsChecks(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.reports = []
        for index, names in enumerate(native_results.REQUIRED):
            path = Path(self.directory.name) / f"report-{index}.jsonl"
            events = []
            for name in sorted(names):
                package = native_results.APP_PACKAGE
                if name in native_results.COREMODEL_MODULE_REQUIRED:
                    package = native_results.COREMODEL_PACKAGE
                elif name in native_results.MEDIAERROR_MODULE_REQUIRED:
                    package = native_results.MEDIAERROR_PACKAGE
                elif name in native_results.LOCALMEDIA_MODULE_REQUIRED | native_results.CORE15_LOCALMEDIA_REQUIRED:
                    package = native_results.LOCALMEDIA_PACKAGE
                elif name in native_results.CORE15_EDIT_REQUIRED:
                    package = native_results.EDIT_PACKAGE
                elif name in native_results.MEDIASESSION_MODULE_REQUIRED:
                    package = native_results.MEDIASESSION_PACKAGE
                elif name in native_results.MEDIAPREFLIGHT_MODULE_REQUIRED:
                    package = native_results.MEDIAPREFLIGHT_PACKAGE
                elif name in native_results.MEDIAEXPORT_MODULE_REQUIRED | native_results.JOINED_MEDIAEXPORT_REQUIRED | native_results.CORE15_MEDIAEXPORT_REQUIRED:
                    package = native_results.MEDIAEXPORT_PACKAGE
                elif name in native_results.MEDIAINFO_MODULE_REQUIRED:
                    package = native_results.MEDIAINFO_PACKAGE
                elif name in native_results.SUBTITLE_MODULE_REQUIRED | native_results.SOURCE_SUBTITLE_MODULE_REQUIRED | native_results.CORE16_SUBTITLE_REQUIRED:
                    package = native_results.SUBTITLE_PACKAGE
                elif name in native_results.TOOLRUN_MODULE_REQUIRED | native_results.TOOLRUN_LIFECYCLE_REQUIRED:
                    package = native_results.TOOLRUN_PACKAGE
                elif name.startswith("TestCLI") or name == "TestParseCrop" or name in native_results.CORE15_CLI_REQUIRED | native_results.CORE16_CLI_REQUIRED:
                    package = native_results.CLI_PACKAGE
                elif name == "TestConcurrentPublicationHasExactlyOneCompleteWinner":
                    package = native_results.FS_PACKAGE
                elif index == 0:
                    package = native_results.ENGINE_PACKAGE
                events.append({"Action": "pass", "Test": name, "Package": package})
            events.extend({"Action": "pass", "Package": package} for package in sorted(native_results.REQUIRED_PACKAGES[index]))
            path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
            self.reports.append(path)

    def run_gate(self, reports=None):
        return subprocess.run([sys.executable, str(SCRIPT), *map(str, self.reports if reports is None else reports)],
                              capture_output=True, text=True, timeout=10)

    def assert_required_results_reject_mutations(self, cases, wrong_package, other_report):
        originals = [path.read_text(encoding="utf-8") for path in self.reports]
        baseline = [[json.loads(line) for line in text.splitlines()] for text in originals]
        self.assertEqual(self.run_gate().returncode, 0)
        for index, package, names in cases:
            path = self.reports[index]
            for name in names:
                for action in ("missing", "skip", "fail", "failed-subtest", "wrong-package", "wrong-report", "incomplete-package"):
                    with self.subTest(name=name, action=action):
                        if action == "incomplete-package":
                            events = [event for event in baseline[index]
                                      if event.get("Test") or event["Package"] != package]
                        else:
                            events = [event for event in baseline[index]
                                      if action == "failed-subtest" or event.get("Test") != name]
                        if action in ("skip", "fail", "failed-subtest", "wrong-package"):
                            wrong = wrong_package(index, package)
                            events.append({"Action": "pass" if action == "wrong-package" else "fail" if action == "failed-subtest" else action,
                                           "Test": name + "/fault" if action == "failed-subtest" else name,
                                           "Package": wrong if action == "wrong-package" else package})
                        path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                        if action == "wrong-report":
                            other = other_report(index)
                            moved = baseline[other] + [{"Action": "pass", "Test": name, "Package": package}]
                            self.reports[other].write_text("".join(json.dumps(event) + "\n" for event in moved), encoding="utf-8")
                        result = self.run_gate()
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn("incomplete_packages" if action == "incomplete-package" else "failed/skipped=1" if action == "failed-subtest" else name, result.stderr)
                        for report, original in zip(self.reports, originals):
                            report.write_text(original, encoding="utf-8")

    def test_original_platform_audio_is_required_on_every_native_target(self):
        required = {"TestPlatformOriginalAudioSelection",
                    "TestPlatformOriginalAudioRanksSupportedInputs",
                    "TestPlatformOriginalAudioSurvivesExtractorAndCache",
                    "TestPlatformOriginalAudioInvalidatesPreviewCache"}
        self.assertEqual(native_results.PLATFORM_ORIGINAL_AUDIO_REQUIRED, required)
        self.assertLessEqual(required, native_results.REQUIRED[4])
        self.assertEqual(native_results.REQUIRED_PACKAGES[4], {native_results.APP_PACKAGE})
        workflow = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        self.assertIn("runner: [ubuntu-24.04, ubuntu-24.04-arm, macos-15, macos-15-intel, windows-2025]", workflow)
        native = workflow.split("  native:\n", 1)[1].split("  bookworm-media:\n", 1)[0]
        self.assertIn("python-version: '3.10'", native)
        extractor = native.split("      - name: Pinned native extractor for offline metadata fixtures\n", 1)[1].split("\n      - ", 1)[0]
        self.assertIn("shell: python", extractor)
        self.assertNotIn("if: ", extractor)
        self.assertIn("filename = 'yt-dlp.exe' if os.name == 'nt' else 'yt-dlp'", extractor)
        self.assertIn("66674953fe251b89f4d08c5f0e35e0728679bd67ab3d7d05c0562af101dd3e7a", extractor)
        self.assertIn("1fa6733c37ea6fb51c99ad8fe785e7b7e5f3246c9b980230329d4fb72ed8d4d6", extractor)
        self.assertIn("https://github.com/yt-dlp/yt-dlp/releases/download/2026.08.19/", extractor)
        self.assertIn("if hashlib.sha256(data).hexdigest() != expected:", extractor)
        self.assertIn("'--version'", extractor)
        self.assertIn(".strip() != '2026.08.19':", extractor)
        self.assertIn("stream.write(f'CUTMY_TEST_YTDLP={path}\\n')", extractor)
        self.assertLess(native.index("Pinned native extractor for offline metadata fixtures"), native.index("tee native-media-contract-tests.jsonl"))
        media = next(line for line in workflow.splitlines() if "tee native-media-contract-tests.jsonl" in line)
        self.assertIn("./internal/app -run ", media)
        selector = re.search(r"-run '([^']+)'", media).group(1)
        for name in required:
            self.assertIsNotNone(re.fullmatch(selector, name))
        self.assert_required_results_reject_mutations(
            ((4, native_results.APP_PACKAGE, required),),
            lambda index, package: native_results.ENGINE_PACKAGE,
            lambda index: 0)

    def test_process_return_cleanup_is_required_and_rejects_incomplete_native_proof(self):
        required = {"TestRunOutputCleansDescendantsAfterParentExit"}
        self.assertEqual(native_results.TOOLRUN_LIFECYCLE_REQUIRED, required)
        self.assertLessEqual(required, native_results.REQUIRED[1])
        self.assertIn(native_results.TOOLRUN_PACKAGE, native_results.REQUIRED_PACKAGES[1])
        for name in required:
            self.assertEqual(native_results.REQUIRED_TEST_PACKAGES[1][name], native_results.TOOLRUN_PACKAGE)
        workflow = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        self.assertIn("runner: [ubuntu-24.04, ubuntu-24.04-arm, macos-15, macos-15-intel, windows-2025]", workflow)
        process = next(line for line in workflow.splitlines() if "tee native-process-tests.jsonl" in line)
        self.assertIn("./internal/app ./internal/toolrun", process)
        selector = re.search(r"-run '([^']+)'", process).group(1)
        for name in required:
            self.assertIsNotNone(re.fullmatch(selector, name))
        self.assert_required_results_reject_mutations(
            ((1, native_results.TOOLRUN_PACKAGE, required),),
            lambda index, package: native_results.APP_PACKAGE,
            lambda index: 0)

    def test_engine_separation_checks_are_required_on_every_native_target(self):
        coremodel = {"TestCoreModelPreservesLegacyValidationAndShapes",
                     "TestCoreModelPackageHasNoServiceDependencies"}
        mediaerror = {"TestMediaErrorPreservesClassificationAndCauses",
                      "TestMediaErrorPackageUsesOnlyStandardLibrary"}
        localmedia = {"TestLocalMediaSessionPreservesOwnershipAndFreshness",
                      "TestLocalMediaProcessPreservesBoundsStreamingAndObserver"}
        engine = {"TestEngineRuntimeDependencyBoundary",
                  "TestEngineExternalConsumerPreservesPublicContracts"}
        adapter = {"TestCoreModelAndMediaErrorAdaptersPreserveLegacyContracts"}
        self.assertEqual(native_results.COREMODEL_MODULE_REQUIRED, coremodel)
        self.assertEqual(native_results.MEDIAERROR_MODULE_REQUIRED, mediaerror)
        self.assertEqual(native_results.LOCALMEDIA_MODULE_REQUIRED, localmedia)
        self.assertEqual(native_results.ENGINE_DEPENDENCY_REQUIRED, engine)
        self.assertEqual(native_results.COREMODEL_MEDIAERROR_ADAPTER_REQUIRED, adapter)
        for names, package in ((coremodel, native_results.COREMODEL_PACKAGE),
                               (mediaerror, native_results.MEDIAERROR_PACKAGE),
                               (localmedia, native_results.LOCALMEDIA_PACKAGE),
                               (engine, native_results.ENGINE_PACKAGE)):
            self.assertLessEqual(names, native_results.REQUIRED[0])
            self.assertIn(package, native_results.REQUIRED_PACKAGES[0])
            for name in names:
                self.assertEqual(native_results.REQUIRED_TEST_PACKAGES[0][name], package)
        self.assertLessEqual(adapter, native_results.REQUIRED[4])
        self.assertEqual(native_results.REQUIRED_PACKAGES[4], {native_results.APP_PACKAGE})
        workflow = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        self.assertIn("runner: [ubuntu-24.04, ubuntu-24.04-arm, macos-15, macos-15-intel, windows-2025]", workflow)
        complete = next(line for line in workflow.splitlines() if "tee native-media-tests.jsonl" in line)
        for package in ("./internal/coremodel", "./internal/mediaerror", "./internal/localmedia", "./pkg/engine"):
            self.assertIn(package, complete)
        self.assertNotIn("-run ", complete)
        media = next(line for line in workflow.splitlines() if "tee native-media-contract-tests.jsonl" in line)
        self.assertIn("./internal/app -run ", media)
        for package in ("./internal/coremodel", "./internal/mediaerror", "./internal/localmedia"):
            self.assertNotIn(package, media)
        selector = re.search(r"-run '([^']+)'", media).group(1)
        for name in adapter:
            self.assertIsNotNone(re.fullmatch(selector, name))

    def test_engine_separation_results_reject_missing_skipped_failed_and_substituted_proof(self):
        cases = ((0, native_results.COREMODEL_PACKAGE, native_results.COREMODEL_MODULE_REQUIRED),
                 (0, native_results.MEDIAERROR_PACKAGE, native_results.MEDIAERROR_MODULE_REQUIRED),
                 (0, native_results.LOCALMEDIA_PACKAGE, native_results.LOCALMEDIA_MODULE_REQUIRED),
                 (0, native_results.ENGINE_PACKAGE, native_results.ENGINE_DEPENDENCY_REQUIRED),
                 (4, native_results.APP_PACKAGE, native_results.COREMODEL_MEDIAERROR_ADAPTER_REQUIRED))
        self.assert_required_results_reject_mutations(
            cases,
            lambda index, package: native_results.APP_PACKAGE if index == 0 else native_results.ENGINE_PACKAGE,
            lambda index: 4 if index == 0 else 0)

    def test_engine_separation_modules_cannot_satisfy_existing_engine_and_app_checks(self):
        for package in (native_results.COREMODEL_PACKAGE, native_results.MEDIAERROR_PACKAGE,
                        native_results.LOCALMEDIA_PACKAGE):
            for index, name in ((0, "TestEngineInspectAndExportActualMedia"),
                                (4, "TestMediaContractRotationDisplayGeometry")):
                with self.subTest(package=package, name=name):
                    path = self.reports[index]
                    original = path.read_text(encoding="utf-8")
                    events = [json.loads(line) for line in original.splitlines()]
                    events = [{**event, "Package": package} if event.get("Test") == name else event for event in events]
                    path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                    result = self.run_gate()
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(name, result.stderr)
                    path.write_text(original, encoding="utf-8")

    def test_joined_preflight_and_session_checks_are_required_on_every_native_target(self):
        joined = {"TestJoinedExportPreservesAdmissionOrderSubtitlesAndCleanup",
                  "TestJoinedCopyPreservesMeasuredTimelineReuseAndBudget",
                  "TestJoinedAccuratePreservesPlanRestrictionsAndVerification",
                  "TestJoinedCopyAudioPreservesCapabilityFallbackAndStages",
                  "TestJoinedVerificationPreservesBoundsAndCausePrecedence",
                  "TestJoinedPlanPreservesEscapingFingerprintsAndPacketEligibility"}
        preflight = {"TestPreflightPlanPreservesRequestOwnershipAndDecisionOrder",
                     "TestPreflightCapabilitiesPreserveExactTablesAndRequirements",
                     "TestPreflightBatchPreservesUnionAndFailureOrder",
                     "TestMediaPreflightPackageHasNoIOOrServiceDependencies"}
        session = {"TestSessionPreservesProbeIdentityAndCancellation",
                   "TestSessionPreservesBoundedKeyframeLookahead",
                   "TestSessionPreservesSubtitleCacheOwnershipAndCleanup",
                   "TestSessionReusePreservesIndependentFilesAndFallback",
                   "TestMediaSessionPackageHasNoServiceDependencies"}
        parser = {"TestExtractedSourceParserPreservesLegacyTolerance"}
        preflight_adapter = {"TestMediaPreflightBridgePreservesLegacyShapesAndCauses"}
        session_adapter = {"TestMediaSessionBridgePreservesLegacyTypesAndCallbacks"}
        adapters = preflight_adapter | session_adapter
        self.assertEqual(native_results.JOINED_MEDIAEXPORT_REQUIRED, joined)
        self.assertEqual(native_results.MEDIAPREFLIGHT_MODULE_REQUIRED, preflight)
        self.assertEqual(native_results.MEDIASESSION_MODULE_REQUIRED, session)
        self.assertEqual(native_results.SOURCE_SUBTITLE_MODULE_REQUIRED, parser)
        self.assertEqual(native_results.MEDIAPREFLIGHT_ADAPTER_REQUIRED, preflight_adapter)
        self.assertEqual(native_results.MEDIASESSION_ADAPTER_REQUIRED, session_adapter)
        for names, package in ((joined, native_results.MEDIAEXPORT_PACKAGE),
                               (preflight, native_results.MEDIAPREFLIGHT_PACKAGE),
                               (session, native_results.MEDIASESSION_PACKAGE),
                               (parser, native_results.SUBTITLE_PACKAGE)):
            self.assertLessEqual(names, native_results.REQUIRED[0])
            self.assertIn(package, native_results.REQUIRED_PACKAGES[0])
            for name in names:
                self.assertEqual(native_results.REQUIRED_TEST_PACKAGES[0][name], package)
        self.assertLessEqual(adapters, native_results.REQUIRED[4])
        self.assertEqual(native_results.REQUIRED_PACKAGES[4], {native_results.APP_PACKAGE})
        workflow = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        self.assertIn("runner: [ubuntu-24.04, ubuntu-24.04-arm, macos-15, macos-15-intel, windows-2025]", workflow)
        complete = next(line for line in workflow.splitlines() if "tee native-media-tests.jsonl" in line)
        for package in ("./internal/mediaexport", "./internal/mediapreflight", "./internal/mediasession",
                        "./internal/subtitles", "./pkg/engine"):
            self.assertIn(package, complete)
        self.assertNotIn("-run ", complete)
        media = next(line for line in workflow.splitlines() if "tee native-media-contract-tests.jsonl" in line)
        self.assertIn("./internal/app -run ", media)
        self.assertNotIn("./internal/mediapreflight", media)
        self.assertNotIn("./internal/mediasession", media)
        selector = re.search(r"-run '([^']+)'", media).group(1)
        for name in adapters:
            self.assertIsNotNone(re.fullmatch(selector, name))

    def test_joined_preflight_and_session_results_reject_missing_skipped_failed_and_substituted_proof(self):
        cases = ((0, native_results.MEDIAEXPORT_PACKAGE, native_results.JOINED_MEDIAEXPORT_REQUIRED),
                 (0, native_results.MEDIAPREFLIGHT_PACKAGE, native_results.MEDIAPREFLIGHT_MODULE_REQUIRED),
                 (0, native_results.MEDIASESSION_PACKAGE, native_results.MEDIASESSION_MODULE_REQUIRED),
                 (0, native_results.SUBTITLE_PACKAGE, native_results.SOURCE_SUBTITLE_MODULE_REQUIRED),
                 (4, native_results.APP_PACKAGE, native_results.MEDIAPREFLIGHT_ADAPTER_REQUIRED | native_results.MEDIASESSION_ADAPTER_REQUIRED))
        self.assert_required_results_reject_mutations(
            cases,
            lambda index, package: native_results.ENGINE_PACKAGE if index == 0 else native_results.MEDIAEXPORT_PACKAGE,
            lambda index: 4 if index == 0 else 0)

    def test_joined_preflight_and_session_modules_cannot_satisfy_existing_engine_and_app_checks(self):
        for package in (native_results.MEDIAEXPORT_PACKAGE, native_results.MEDIAPREFLIGHT_PACKAGE,
                        native_results.MEDIASESSION_PACKAGE, native_results.SUBTITLE_PACKAGE):
            for index, name in ((0, "TestEngineInspectAndExportActualMedia"),
                                (4, "TestMediaContractRotationDisplayGeometry")):
                with self.subTest(package=package, name=name):
                    path = self.reports[index]
                    original = path.read_text(encoding="utf-8")
                    events = [json.loads(line) for line in original.splitlines()]
                    events = [{**event, "Package": package} if event.get("Test") == name else event for event in events]
                    path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                    result = self.run_gate()
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(name, result.stderr)
                    path.write_text(original, encoding="utf-8")

    def test_mediaexport_module_and_app_bridges_are_required_on_every_native_target(self):
        module = {"TestSingleExportPreservesCopyClockAndVerificationOrder",
                  "TestSingleExportPreservesFailurePrecedenceAndErrorIdentity",
                  "TestSingleExportPreservesValidationAndCancellationOrder",
                  "TestSingleExportPreservesRenderProfilesAndIntermediateOutputs",
                  "TestMediaExportPackageHasNoIOOrServiceDependencies"}
        adapters = {"TestMediaExportBridgePreservesLegacyErrorsAndRuntime",
                    "TestMediaExportBackendPreservesProcessPaths"}
        self.assertEqual(native_results.MEDIAEXPORT_MODULE_REQUIRED, module)
        self.assertEqual(native_results.MEDIAEXPORT_ADAPTER_REQUIRED, adapters)
        self.assertLessEqual(module, native_results.REQUIRED[0])
        self.assertLessEqual(adapters, native_results.REQUIRED[4])
        self.assertIn(native_results.MEDIAEXPORT_PACKAGE, native_results.REQUIRED_PACKAGES[0])
        self.assertEqual(native_results.REQUIRED_PACKAGES[4], {native_results.APP_PACKAGE})
        for name in module:
            self.assertEqual(native_results.REQUIRED_TEST_PACKAGES[0][name], native_results.MEDIAEXPORT_PACKAGE)
        workflow = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        self.assertIn("runner: [ubuntu-24.04, ubuntu-24.04-arm, macos-15, macos-15-intel, windows-2025]", workflow)
        complete = next(line for line in workflow.splitlines() if "tee native-media-tests.jsonl" in line)
        for package in ("./internal/mediaexport", "./internal/mediainfo", "./internal/subtitles", "./pkg/engine"):
            self.assertIn(package, complete)
        self.assertNotIn("-run ", complete)
        media = next(line for line in workflow.splitlines() if "tee native-media-contract-tests.jsonl" in line)
        self.assertIn("./internal/app -run ", media)
        self.assertNotIn("./internal/mediaexport", media)
        selector = re.search(r"-run '([^']+)'", media).group(1)
        for name in adapters:
            self.assertIsNotNone(re.fullmatch(selector, name))

    def test_mediaexport_results_reject_missing_skipped_failed_and_substituted_proof(self):
        cases = ((0, native_results.MEDIAEXPORT_PACKAGE, native_results.MEDIAEXPORT_MODULE_REQUIRED),
                 (4, native_results.APP_PACKAGE, native_results.MEDIAEXPORT_ADAPTER_REQUIRED))
        self.assert_required_results_reject_mutations(
            cases,
            lambda index, package: native_results.ENGINE_PACKAGE if index == 0 else native_results.MEDIAEXPORT_PACKAGE,
            lambda index: 4 if index == 0 else 0)

    def test_mediaexport_cannot_satisfy_existing_engine_and_app_checks(self):
        for index, name in ((0, "TestEngineInspectAndExportActualMedia"),
                            (4, "TestMediaContractRotationDisplayGeometry")):
            path = self.reports[index]
            original = path.read_text(encoding="utf-8")
            events = [json.loads(line) for line in original.splitlines()]
            events = [{**event, "Package": native_results.MEDIAEXPORT_PACKAGE} if event.get("Test") == name else event for event in events]
            path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
            result = self.run_gate()
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(name, result.stderr)
            path.write_text(original, encoding="utf-8")

    def test_mediainfo_module_and_app_bridges_are_required_on_every_native_target(self):
        module = {"TestInfoValidationPreservesProbeBoundaries",
                  "TestInfoDurationAndSelectionPreserveSourceOrder",
                  "TestTrackDecisionPreservesManifestAndErrorContracts",
                  "TestMediaInfoPackageHasPureDependencies"}
        adapters = {"TestMediaInfoErrorBridgePreservesLegacyTypesAndCauses",
                    "TestMediaInfoGeometryPreservesValidationOrder"}
        self.assertEqual(native_results.MEDIAINFO_MODULE_REQUIRED, module)
        self.assertEqual(native_results.MEDIAINFO_ADAPTER_REQUIRED, adapters)
        self.assertLessEqual(module, native_results.REQUIRED[0])
        self.assertLessEqual(adapters, native_results.REQUIRED[4])
        self.assertIn(native_results.MEDIAINFO_PACKAGE, native_results.REQUIRED_PACKAGES[0])
        self.assertEqual(native_results.REQUIRED_PACKAGES[4], {native_results.APP_PACKAGE})
        for name in module:
            self.assertEqual(native_results.REQUIRED_TEST_PACKAGES[0][name], native_results.MEDIAINFO_PACKAGE)
        workflow = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        self.assertIn("runner: [ubuntu-24.04, ubuntu-24.04-arm, macos-15, macos-15-intel, windows-2025]", workflow)
        complete = next(line for line in workflow.splitlines() if "tee native-media-tests.jsonl" in line)
        self.assertIn("./internal/mediainfo", complete)
        self.assertIn("./pkg/engine", complete)
        self.assertNotIn("-run ", complete)
        media = next(line for line in workflow.splitlines() if "tee native-media-contract-tests.jsonl" in line)
        self.assertIn("./internal/app -run ", media)
        self.assertNotIn("./internal/mediainfo", media)
        selector = re.search(r"-run '([^']+)'", media).group(1)
        for name in adapters:
            self.assertIsNotNone(re.fullmatch(selector, name))

    def test_mediainfo_results_reject_missing_skipped_failed_and_substituted_proof(self):
        cases = ((0, native_results.MEDIAINFO_PACKAGE, native_results.MEDIAINFO_MODULE_REQUIRED),
                 (4, native_results.APP_PACKAGE, native_results.MEDIAINFO_ADAPTER_REQUIRED))
        self.assert_required_results_reject_mutations(
            cases,
            lambda index, package: native_results.ENGINE_PACKAGE if index == 0 else native_results.MEDIAINFO_PACKAGE,
            lambda index: 4 if index == 0 else 0)

    def test_mediainfo_cannot_satisfy_existing_engine_and_app_checks(self):
        for index, name in ((0, "TestEngineInspectAndExportActualMedia"),
                            (4, "TestMediaContractRotationDisplayGeometry")):
            path = self.reports[index]
            original = path.read_text(encoding="utf-8")
            events = [json.loads(line) for line in original.splitlines()]
            events = [{**event, "Package": native_results.MEDIAINFO_PACKAGE} if event.get("Test") == name else event for event in events]
            path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
            result = self.run_gate()
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(name, result.stderr)
            path.write_text(original, encoding="utf-8")

    def test_media_component_checks_are_required_and_selected_on_every_native_target(self):
        subtitle = {"TestSubtitlePackageHasNoRuntimeDependencies"}
        engine = {"TestEngineSubtitleBoundaryPreservesPublicContracts"}
        runner = {"TestToolrunPackageUsesOnlyStandardLibrary",
                  "TestRunOutputPreservesRawFailureAndStreaming",
                  "TestRunOutputPreservesCancellation",
                  "TestDiagnosticCopyCannotBypassMemoryBound"}
        self.assertEqual(native_results.SUBTITLE_MODULE_REQUIRED, subtitle)
        self.assertEqual(native_results.MEDIA_BOUNDARY_ENGINE_REQUIRED, engine)
        self.assertEqual(native_results.TOOLRUN_MODULE_REQUIRED, runner)
        workflow = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        self.assertIn("runner: [ubuntu-24.04, ubuntu-24.04-arm, macos-15, macos-15-intel, windows-2025]", workflow)
        media = next(line for line in workflow.splitlines() if "tee native-media-tests.jsonl" in line)
        process = next(line for line in workflow.splitlines() if "tee native-process-tests.jsonl" in line)
        self.assertIn("./internal/subtitles", media)
        self.assertNotIn("-run ", media)
        self.assertIn("./internal/app ./internal/toolrun", process)
        selector = re.search(r"-run '([^']+)'", process).group(1)
        for index, package, names in ((0, native_results.SUBTITLE_PACKAGE, subtitle),
                                      (0, native_results.ENGINE_PACKAGE, engine),
                                      (1, native_results.TOOLRUN_PACKAGE, runner)):
            self.assertLessEqual(names, native_results.REQUIRED[index])
            self.assertIn(package, native_results.REQUIRED_PACKAGES[index])
            for name in names:
                self.assertEqual(native_results.REQUIRED_TEST_PACKAGES[index][name], package)
                if index == 1:
                    self.assertIsNotNone(re.fullmatch(selector, name))

    def test_media_component_results_reject_missing_failed_skipped_and_substituted_proof(self):
        cases = ((0, native_results.SUBTITLE_PACKAGE, {"TestSubtitlePackageHasNoRuntimeDependencies"}),
                 (0, native_results.ENGINE_PACKAGE, {"TestEngineSubtitleBoundaryPreservesPublicContracts"}),
                 (1, native_results.TOOLRUN_PACKAGE, {"TestToolrunPackageUsesOnlyStandardLibrary",
                  "TestRunOutputPreservesRawFailureAndStreaming", "TestRunOutputPreservesCancellation",
                  "TestDiagnosticCopyCannotBypassMemoryBound"}))
        self.assert_required_results_reject_mutations(
            cases,
            lambda index, package: native_results.ENGINE_PACKAGE if package == native_results.SUBTITLE_PACKAGE else native_results.SUBTITLE_PACKAGE if index == 0 else native_results.APP_PACKAGE,
            lambda index: 1 - index)

    def test_new_modules_cannot_satisfy_existing_engine_and_app_requirements(self):
        for index, name, package in ((0, "TestEngineInspectAndExportActualMedia", native_results.SUBTITLE_PACKAGE),
                                     (1, "TestProcessCancellationTerminatesDescendants", native_results.TOOLRUN_PACKAGE)):
            path = self.reports[index]
            original = path.read_text(encoding="utf-8")
            events = [json.loads(line) for line in original.splitlines()]
            events = [{**event, "Package": package} if event.get("Test") == name else event for event in events]
            path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
            result = self.run_gate()
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(name, result.stderr)
            path.write_text(original, encoding="utf-8")

    def test_engine_media_checks_run_once_in_the_complete_engine_report(self):
        names = {"TestAdvancedCropPlanPublishesActualCroppedOutput",
                 "TestEngineInspectSeparatesCodedAndDisplayDimensions"}
        self.assertEqual(native_results.ENGINE_MEDIA_REQUIRED, names)
        self.assertLessEqual(names, native_results.REQUIRED[0])
        self.assertFalse(names & native_results.REQUIRED[4])
        self.assertIn(native_results.ENGINE_PACKAGE, native_results.REQUIRED_PACKAGES[0])
        self.assertEqual(native_results.REQUIRED_PACKAGES[4], {native_results.APP_PACKAGE})
        workflow = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        complete = next(line for line in workflow.splitlines() if "tee native-media-tests.jsonl" in line)
        media = next(line for line in workflow.splitlines() if "tee native-media-contract-tests.jsonl" in line)
        self.assertIn("./pkg/engine", complete)
        self.assertNotIn("-run ", complete, "The engine report must keep the complete package invocation")
        self.assertIn("./internal/app -run ", media)
        self.assertNotIn("./pkg/engine", media)
        selector = re.search(r"-run '([^']+)'", media).group(1)
        for name in names:
            self.assertIsNone(re.fullmatch(selector, name))

    def test_moved_engine_media_checks_reject_missing_skipped_failed_and_incomplete_results(self):
        path = self.reports[0]
        original = path.read_text(encoding="utf-8")
        good = [json.loads(line) for line in original.splitlines()]
        self.assertEqual(self.run_gate().returncode, 0)
        for name in native_results.ENGINE_MEDIA_REQUIRED:
            for action in ("missing", "skip", "fail", "failed-subtest", "incomplete-package"):
                with self.subTest(name=name, action=action):
                    if action == "incomplete-package":
                        events = [event for event in good if event.get("Test") or event["Package"] != native_results.ENGINE_PACKAGE]
                    else:
                        events = [event for event in good if action == "failed-subtest" or event.get("Test") != name]
                        if action != "missing":
                            events.append({"Action": "fail" if action == "failed-subtest" else action,
                                           "Test": name + "/fault" if action == "failed-subtest" else name,
                                           "Package": native_results.ENGINE_PACKAGE})
                    path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                    result = self.run_gate()
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn("incomplete_packages" if action == "incomplete-package" else
                                  "failed/skipped=1" if action == "failed-subtest" else name, result.stderr)
        path.write_text(original, encoding="utf-8")

    def test_moved_engine_media_checks_require_the_engine_package_and_right_report(self):
        engine, media = self.reports[0], self.reports[4]
        originals = {path: path.read_text(encoding="utf-8") for path in (engine, media)}
        good = {path: [json.loads(line) for line in text.splitlines()] for path, text in originals.items()}
        for name in native_results.ENGINE_MEDIA_REQUIRED:
            for package in (native_results.CLI_PACKAGE, native_results.FS_PACKAGE,
                            native_results.APP_PACKAGE, "unrelated/package", None):
                with self.subTest(name=name, package=package):
                    events = [{**event, "Package": package} if event.get("Test") == name else event
                              for event in good[engine]]
                    engine.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                    result = self.run_gate()
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(name, result.stderr)
            with self.subTest(name=name, report="wrong-report"):
                engine.write_text("".join(json.dumps(event) + "\n" for event in good[engine]
                                          if event.get("Test") != name), encoding="utf-8")
                events = good[media] + [{"Action": "pass", "Test": name, "Package": native_results.ENGINE_PACKAGE}]
                media.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                result = self.run_gate()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(name, result.stderr)
            media.write_text(originals[media], encoding="utf-8")
        engine.write_text(originals[engine], encoding="utf-8")

    def test_media_contract_report_requires_app_results_and_completion(self):
        path = self.reports[4]
        good = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]
        name = "TestMediaContractRotationDisplayGeometry"
        for action in ("wrong-test-package", "wrong-completed-package"):
            with self.subTest(action=action):
                events = [{**event, "Package": native_results.ENGINE_PACKAGE}
                          if (event.get("Test") == name if action == "wrong-test-package" else not event.get("Test"))
                          else event for event in good]
                path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                result = self.run_gate()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(name if action == "wrong-test-package" else "incomplete_packages", result.stderr)

    def test_audit_regressions_are_required_and_selected_on_native_and_bookworm(self):
        media = {"TestHLSManifestRedirectResolvesFinalRelativeReferences",
                 "TestHLSManifestRedirectRetainsTargetGuards",
                 "TestHLSManifestRedirectRetainsRedirectLimit",
                 "TestHLSManifestRedirectRetainsTransferBudgets"}
        service = {"TestHTTPAgentProjectJobLimitRejectsAdmissionAtomicallyAndRetainsReplay",
                   "TestHTTPAgentProjectConcurrentAdmissionsShareLastBindingSlot",
                   "TestHTTPAgentProjectPartialAdmissionPreservesJobsAtBindingLimit",
                   "TestProjectAdmissionRechecksScopeBeforeCommittingJob",
                   "TestHTTPProjectAndPreparationRollbackHasIndependentBound"}
        self.assertEqual(native_results.AUDIT_MEDIA_REQUIRED, media)
        self.assertEqual(native_results.AUDIT_SERVICE_REQUIRED, service)
        workflow = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        self.assertIn("runner: [ubuntu-24.04, ubuntu-24.04-arm, macos-15, macos-15-intel, windows-2025]", workflow)
        for report, required, names in (
            ("native-media-contract-tests.jsonl", native_results.REQUIRED[4], media),
            ("native-service-tests.jsonl", native_results.REQUIRED[2], service),
            ("bookworm-media-tests.jsonl", native_results.BOOKWORM_REQUIRED, media),
        ):
            command = next(line for line in workflow.splitlines() if "tee " + report in line)
            selector = re.search(r"-run '([^']+)'", command).group(1)
            for name in names:
                with self.subTest(report=report, name=name):
                    self.assertIn(name, required)
                    self.assertIsNotNone(re.fullmatch(selector, name))
            if report == "bookworm-media-tests.jsonl":
                for name in service:
                    self.assertNotIn(name, required)
                    self.assertIsNone(re.fullmatch(selector, name), "Database tests belong to the service invocation")

    def test_project_contract_and_lifecycle_regressions_are_required_on_every_native_target(self):
        names = {"TestProjectCollectionContractExamples",
                 "TestProjectLifecycleRevokeAndDeletePreserveAcceptedWork",
                 "TestProjectLifecycleAccessExpiryPrecedesAdmissionCleanup",
                 "TestProjectLifecycleLiveProjectAndGrantDoNotPinSource"}
        self.assertEqual(native_results.PROJECT_CONTRACT_SERVICE_REQUIRED, names)
        self.assertLessEqual(names, native_results.REQUIRED[2])
        workflow = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        command = next(line for line in workflow.splitlines() if "tee native-service-tests.jsonl" in line)
        selector = re.search(r"-run '([^']+)'", command).group(1)
        for name in names:
            with self.subTest(name=name):
                self.assertIsNotNone(re.fullmatch(selector, name))

    def test_edit_model_compatibility_is_required_on_every_native_target(self):
        service = {"TestEditAdapterPreservesRequestNormalizationOrder",
                   "TestEditAdapterPreservesCollectionErrorChains"}
        engine = {"TestEngineEditModelPreservesPublicShapes"}
        self.assertEqual(native_results.EDIT_MODEL_SERVICE_REQUIRED, service)
        self.assertEqual(native_results.EDIT_MODEL_ENGINE_REQUIRED, engine)
        workflow = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        self.assertIn("runner: [ubuntu-24.04, ubuntu-24.04-arm, macos-15, macos-15-intel, windows-2025]", workflow)
        for index, names, report in (
            (2, service, "native-service-tests.jsonl"),
            (0, engine, "native-media-tests.jsonl"),
        ):
            command = next(line for line in workflow.splitlines() if "tee " + report in line)
            selector = re.search(r"-run '([^']+)'", command)
            self.assertLessEqual(names, native_results.REQUIRED[index])
            if index == 0:
                self.assertIn("./pkg/engine", command)
                self.assertIsNone(selector, "engine compatibility runs in the complete native package")
            else:
                self.assertIsNotNone(selector)
                for name in names:
                    self.assertIsNotNone(re.fullmatch(selector.group(1), name))

    def test_edit_model_compatibility_rejects_missing_skipped_failed_and_nested_failed_results(self):
        for index, names, package in (
            (2, native_results.EDIT_MODEL_SERVICE_REQUIRED, native_results.APP_PACKAGE),
            (0, native_results.EDIT_MODEL_ENGINE_REQUIRED, native_results.ENGINE_PACKAGE),
        ):
            path = self.reports[index]
            original = path.read_text(encoding="utf-8")
            good = [json.loads(line) for line in original.splitlines()]
            self.assertEqual(self.run_gate().returncode, 0)
            for name in names:
                for action in ("missing", "skip", "fail", "failed-subtest"):
                    with self.subTest(name=name, action=action):
                        events = [event for event in good if action == "failed-subtest" or event.get("Test") != name]
                        if action != "missing":
                            events.append({"Action": "fail" if action == "failed-subtest" else action,
                                           "Test": name + "/fault" if action == "failed-subtest" else name,
                                           "Package": package})
                        path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                        result = self.run_gate()
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn("failed/skipped=1" if action == "failed-subtest" else name, result.stderr)
            path.write_text(original, encoding="utf-8")

    def test_audit_regressions_reject_missing_skipped_and_failed_results(self):
        bookworm = Path(self.directory.name) / "audit-bookworm.jsonl"
        bookworm_events = [{"Action": "pass", "Test": name, "Package": native_results.APP_PACKAGE}
                           for name in sorted(native_results.BOOKWORM_REQUIRED)]
        bookworm_events.append({"Action": "pass", "Package": native_results.APP_PACKAGE})
        bookworm.write_text("".join(json.dumps(event) + "\n" for event in bookworm_events), encoding="utf-8")
        for path, names, option in (
            (self.reports[4], native_results.AUDIT_MEDIA_REQUIRED, None),
            (self.reports[2], native_results.AUDIT_SERVICE_REQUIRED, None),
            (self.reports[2], native_results.PROJECT_CONTRACT_SERVICE_REQUIRED, None),
            (bookworm, native_results.AUDIT_MEDIA_REQUIRED, "--bookworm"),
        ):
            good = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]
            invocation = ([sys.executable, str(SCRIPT), option, str(path)] if option else
                          [sys.executable, str(SCRIPT), *map(str, self.reports)])
            self.assertEqual(subprocess.run(invocation, capture_output=True, timeout=10).returncode, 0)
            for name in names:
                for action in ("missing", "skip", "fail", "failed-subtest"):
                    with self.subTest(report=path.name, name=name, action=action):
                        events = [event for event in good if action == "failed-subtest" or event.get("Test") != name]
                        if action != "missing":
                            events.append({"Action": "fail" if action == "failed-subtest" else action,
                                           "Test": name + "/fault" if action == "failed-subtest" else name,
                                           "Package": native_results.APP_PACKAGE})
                        path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                        result = subprocess.run(invocation, capture_output=True, text=True, timeout=10)
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn("failed/skipped=1" if action == "failed-subtest" else name, result.stderr)
            path.write_text("".join(json.dumps(event) + "\n" for event in good), encoding="utf-8")

    def test_process_environment_guards_are_required_and_selected(self):
        names = {"TestProcessEnvironmentSanitizesRealChild", "TestProcessEnvironmentArenaBoundsAndPlatforms"}
        workflow = SCRIPT.parent.parent / ".github" / "workflows" / "ci.yml"
        command = next(line for line in workflow.read_text(encoding="utf-8").splitlines()
                       if "tee native-process-tests.jsonl" in line)
        selector = re.search(r"-run '([^']+)'", command).group(1)
        original = self.reports[1].read_text(encoding="utf-8")
        for name in names:
            self.assertIn(name, native_results.REQUIRED[1])
            self.assertIsNotNone(re.fullmatch(selector, name))
            for action in ("missing", "skip", "failed-subtest"):
                with self.subTest(name=name, action=action):
                    events = [json.loads(line) for line in original.splitlines()]
                    if action != "failed-subtest":
                        events = [event for event in events if event.get("Test") != name]
                    if action != "missing":
                        events.append({"Action": "skip" if action == "skip" else "fail",
                                       "Test": name if action == "skip" else name + "/fault",
                                       "Package": native_results.APP_PACKAGE})
                    self.reports[1].write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                    self.assertNotEqual(self.run_gate().returncode, 0)
        self.reports[1].write_text(original, encoding="utf-8")

    def test_new_release_features_are_required_and_selected_on_all_native_targets(self):
        workflow = SCRIPT.parent.parent / ".github" / "workflows" / "ci.yml"
        lines = workflow.read_text(encoding="utf-8").splitlines()
        for index, names, report in ((2, native_results.NEW_SERVICE_REQUIRED | native_results.EFFICIENT_CUTTING_SERVICE_REQUIRED | native_results.UNIVERSAL_SERVICE_REQUIRED, "native-service-tests.jsonl"),
                                     (3, native_results.EFFICIENT_CUTTING_STORAGE_REQUIRED | native_results.UNIVERSAL_STORAGE_REQUIRED, "native-storage-tests.jsonl"),
                                     (4, native_results.NEW_MEDIA_REQUIRED | native_results.EFFICIENT_CUTTING_MEDIA_REQUIRED | native_results.UNIVERSAL_MEDIA_REQUIRED, "native-media-contract-tests.jsonl")):
            command = next(line for line in lines if "tee " + report in line)
            selector = re.search(r"-run '([^']+)'", command).group(1)
            original = self.reports[index].read_text(encoding="utf-8")
            for name in names:
                self.assertIn(name, native_results.REQUIRED[index])
                self.assertIsNotNone(re.fullmatch(selector, name))
                for action in ("missing", "skip", "failed-subtest"):
                    with self.subTest(name=name, action=action):
                        events = [json.loads(line) for line in original.splitlines()]
                        if action != "failed-subtest":
                            events = [event for event in events if event.get("Test") != name]
                        if action != "missing":
                            events.append({"Action":"skip" if action == "skip" else "fail", "Test":name if action == "skip" else name + "/fault", "Package":native_results.APP_PACKAGE})
                        self.reports[index].write_text("".join(json.dumps(event)+"\n" for event in events),encoding="utf-8")
                        self.assertNotEqual(self.run_gate().returncode, 0)
            self.reports[index].write_text(original,encoding="utf-8")
        bookworm = next(line for line in lines if "tee bookworm-media-tests.jsonl" in line)
        selector = re.search(r"-run '([^']+)'", bookworm).group(1)
        for name in native_results.NEW_MEDIA_REQUIRED | native_results.EFFICIENT_CUTTING_MEDIA_REQUIRED | native_results.UNIVERSAL_MEDIA_REQUIRED:
            self.assertIn(name,native_results.BOOKWORM_REQUIRED)
            self.assertIsNotNone(re.fullmatch(selector,name))
        self.assertIsNone(re.fullmatch(selector,"TestWaveformPrivateCacheAccountingAndDeletion"))
        service = next(line for line in lines if "tee native-service-tests.jsonl" in line)
        selector = re.search(r"-run '([^']+)'", service).group(1)
        self.assertIsNone(re.fullmatch(selector,"TestSourceMediaCleanupCoordinated005ReverseAndCompatible006BackupRestore"))

    def test_pool_idle_defaults_and_lifecycle_are_required_on_every_native_service_host(self):
        names = {"TestStorePoolIdleDefaultsPreserveExplicitDurations",
                 "TestStorePoolInvalidIdleDurationsRemainParserErrors",
                 "TestStorePoolIdleRetirementPreservesAcquiredTransaction"}
        self.assertEqual(native_results.POOL_SERVICE_REQUIRED, names)
        lines = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8").splitlines()
        command = next(line for line in lines if "tee native-service-tests.jsonl" in line)
        selector = re.search(r"-run '([^']+)'", command).group(1)
        bookworm = next(line for line in lines if "tee bookworm-media-tests.jsonl" in line)
        bookworm_selector = re.search(r"-run '([^']+)'", bookworm).group(1)
        path = self.reports[2]
        original = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]
        for name in names:
            self.assertIn(name, native_results.REQUIRED[2])
            self.assertNotIn(name, native_results.BOOKWORM_REQUIRED)
            self.assertIsNotNone(re.fullmatch(selector, name))
            self.assertIsNone(re.fullmatch(bookworm_selector, name), "Pool integration belongs outside the media-only invocation")
            for action in ("missing", "skip", "failed-subtest"):
                with self.subTest(name=name, action=action):
                    events = [event for event in original if action == "failed-subtest" or event.get("Test") != name]
                    if action != "missing":
                        events.append({"Action": "skip" if action == "skip" else "fail",
                                       "Test": name if action == "skip" else name + "/fault",
                                       "Package": native_results.APP_PACKAGE})
                    path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                    result = self.run_gate()
                    self.assertNotEqual(result.returncode, 0)
                    if action != "failed-subtest":
                        self.assertIn(name, result.stderr)
        path.write_text("".join(json.dumps(event) + "\n" for event in original), encoding="utf-8")

    def test_draft_cache_checks_select_native_and_bookworm_without_silent_database_skips(self):
        transfer = {
            "TestDraftCacheWrappedFullTransferAndFlushError",
            "TestDraftCacheTransferWriterPreservesInterfacesAndCountsErrors",
            "TestDraftCacheFullTransferGuards",
            "TestDraftCacheSourceAndFinalServingUnchanged",
        }
        database = {"TestDraftCacheMetadataOwnerLegacyAndScopedDownload"}
        self.assertEqual(native_results.DRAFT_CACHE_TRANSFER_REQUIRED, transfer)
        self.assertEqual(native_results.DRAFT_CACHE_DATABASE_REQUIRED, database)
        workflow = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        lines = workflow.splitlines()
        command = next(line for line in lines if "tee native-service-tests.jsonl" in line)
        native_selector = re.search(r"-run '([^']+)'", command).group(1)
        command = next(line for line in lines if "tee bookworm-media-tests.jsonl" in line)
        media_selector = re.search(r"-run '([^']+)'", command).group(1)
        command = next(line for line in lines if "tee bookworm-draft-cache-service-tests.jsonl" in line)
        database_selector = re.search(r"-run '([^']+)'", command).group(1)
        for name in transfer | database:
            self.assertIn(name, native_results.REQUIRED[2])
            self.assertIsNotNone(re.fullmatch(native_selector, name))
            self.assertEqual(name in native_results.BOOKWORM_REQUIRED, name in transfer)
            self.assertEqual(re.fullmatch(media_selector, name) is not None, name in transfer)
            self.assertEqual(re.fullmatch(database_selector, name) is not None, name in database)
        job = workflow.split("\n  bookworm-media:\n", 1)[1].split("\n  test:\n", 1)[0]
        self.assertIn("services:\n      postgres:\n        image: postgres:17.11-bookworm", job)
        self.assertIn('--health-cmd "pg_isready -U cutmy -d cutmy_draft_cache"', job)
        self.assertIn("TEST_DATABASE_URL: postgres://cutmy:bookworm-test-only@postgres:5432/cutmy_draft_cache?sslmode=disable", job)
        self.assertIn("scripts/check-native-results.py --draft-cache-service bookworm-draft-cache-service-tests.jsonl", job)
        self.assertIn("            bookworm-draft-cache-service-tests.jsonl\n", job)

    def test_draft_cache_required_results_reject_missing_skipped_failed_and_truncated_reports(self):
        native = self.reports[2]
        original = native.read_text(encoding="utf-8")
        cases = [(native, native_results.DRAFT_CACHE_TRANSFER_REQUIRED | native_results.DRAFT_CACHE_DATABASE_REQUIRED,
                  [json.loads(line) for line in original.splitlines()], None)]
        for filename, required, checked, option in (
            ("draft-bookworm-media.jsonl", native_results.BOOKWORM_REQUIRED,
             native_results.DRAFT_CACHE_TRANSFER_REQUIRED, "--bookworm"),
            ("draft-bookworm-service.jsonl", native_results.DRAFT_CACHE_DATABASE_REQUIRED,
             native_results.DRAFT_CACHE_DATABASE_REQUIRED, "--draft-cache-service"),
        ):
            good = [{"Action": "pass", "Test": name, "Package": native_results.APP_PACKAGE} for name in sorted(required)]
            good.append({"Action": "pass", "Package": native_results.APP_PACKAGE})
            cases.append((Path(self.directory.name) / filename, checked, good, option))
        for path, checked, good, option in cases:
            invocation = ([sys.executable, str(SCRIPT), option, str(path)] if option else
                          [sys.executable, str(SCRIPT), *map(str, self.reports)])
            path.write_text("".join(json.dumps(event) + "\n" for event in good), encoding="utf-8")
            self.assertEqual(subprocess.run(invocation, capture_output=True, timeout=10).returncode, 0)
            for name in checked:
                for action in ("missing", "skip", "fail", "failed-subtest", "truncated-package"):
                    with self.subTest(report=path.name, name=name, action=action):
                        if action == "truncated-package":
                            events = [event for event in good if event.get("Test")]
                        elif action == "failed-subtest":
                            events = good + [{"Action": "fail", "Test": name + "/fault", "Package": native_results.APP_PACKAGE}]
                        else:
                            events = [event for event in good if event.get("Test") != name]
                            if action != "missing":
                                events.append({"Action": action, "Test": name, "Package": native_results.APP_PACKAGE})
                        path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                        result = subprocess.run(invocation, capture_output=True, text=True, timeout=10)
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn("incomplete_packages" if action == "truncated-package" else
                                      "failed/skipped" if action == "failed-subtest" else name, result.stderr)
            path.write_text("".join(json.dumps(event) + "\n" for event in good), encoding="utf-8")
        native.write_text(original, encoding="utf-8")

    def test_joined_decoder_budget_is_required_on_native_and_bookworm(self):
        name = "TestJoinedAccurateDiscoveryDecoderBudgetPreservesEncoding"
        self.assertIn(name, native_results.REQUIRED[4])
        self.assertIn(name, native_results.BOOKWORM_REQUIRED)
        lines = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8").splitlines()
        for report in ("native-media-contract-tests.jsonl", "bookworm-media-tests.jsonl"):
            command = next(line for line in lines if "tee " + report in line)
            selector = re.search(r"-run '([^']+)'", command).group(1)
            self.assertIsNotNone(re.fullmatch(selector, name))
        native = self.reports[4]
        original = [json.loads(line) for line in native.read_text(encoding="utf-8").splitlines()]
        bookworm = Path(self.directory.name) / "bookworm-decoder.jsonl"
        bookworm_events = [{"Action": "pass", "Test": item, "Package": native_results.APP_PACKAGE}
                           for item in sorted(native_results.BOOKWORM_REQUIRED)]
        bookworm_events.append({"Action": "pass", "Package": native_results.APP_PACKAGE})
        for target, good in ((native, original), (bookworm, bookworm_events)):
            for action in ("missing", "skip", "failed-subtest"):
                with self.subTest(target=target.name, action=action):
                    events = [event for event in good if action == "failed-subtest" or event.get("Test") != name]
                    if action != "missing":
                        events.append({"Action": "skip" if action == "skip" else "fail",
                                       "Test": name if action == "skip" else name + "/fault",
                                       "Package": native_results.APP_PACKAGE})
                    target.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                    result = (self.run_gate() if target == native else
                              subprocess.run([sys.executable, str(SCRIPT), "--bookworm", str(target)],
                                             capture_output=True, text=True, timeout=10))
                    self.assertNotEqual(result.returncode, 0)
        native.write_text("".join(json.dumps(event) + "\n" for event in original), encoding="utf-8")

    def test_universal_engine_results_cannot_be_missing_or_skipped(self):
        path = self.reports[0]
        original = path.read_text(encoding="utf-8")
        for name in native_results.UNIVERSAL_ENGINE_REQUIRED:
            for action in ("missing", "skip"):
                with self.subTest(name=name, action=action):
                    events = [json.loads(line) for line in original.splitlines()]
                    events = [event for event in events if event.get("Test") != name]
                    if action == "skip":
                        events.append({"Action": "skip", "Test": name, "Package": native_results.ENGINE_PACKAGE})
                    path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                    self.assertNotEqual(self.run_gate().returncode, 0)
        path.write_text(original, encoding="utf-8")

    def test_preflight_results_are_required_and_selected_without_skips(self):
        lines = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8").splitlines()
        groups = (
            (0, native_results.PREFLIGHT_ENGINE_REQUIRED | native_results.PREFLIGHT_CLI_REQUIRED, "native-media-tests.jsonl"),
            (2, native_results.PREFLIGHT_SERVICE_REQUIRED, "native-service-tests.jsonl"),
            (4, native_results.PREFLIGHT_MEDIA_REQUIRED, "native-media-contract-tests.jsonl"),
        )
        for index, names, report in groups:
            command = next(line for line in lines if "tee " + report in line)
            selector = re.search(r"-run '([^']+)'", command)
            if index == 0:
                self.assertIn("./pkg/engine", command)
                self.assertIn("./cmd/cutmy", command)
                self.assertIsNone(selector, "engine and CLI preflight must run in their complete native packages")
            original = self.reports[index].read_text(encoding="utf-8")
            for name in names:
                self.assertIn(name, native_results.REQUIRED[index])
                if selector:
                    self.assertIsNotNone(re.fullmatch(selector.group(1), name))
                for action in ("missing", "skip", "failed-subtest"):
                    with self.subTest(name=name, action=action):
                        events = [json.loads(line) for line in original.splitlines()]
                        if action != "failed-subtest":
                            events = [event for event in events if event.get("Test") != name]
                        if action != "missing":
                            events.append({"Action": "skip" if action == "skip" else "fail", "Test": name if action == "skip" else name + "/fault", "Package": native_results.APP_PACKAGE})
                        self.reports[index].write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                        result = self.run_gate()
                        self.assertNotEqual(result.returncode, 0)
                        if action != "failed-subtest":
                            self.assertIn(name, result.stderr)
            self.reports[index].write_text(original, encoding="utf-8")

    def test_collection_results_are_required_and_selected_without_skips(self):
        lines = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8").splitlines()
        groups = (
            (0, native_results.COLLECTION_ENGINE_REQUIRED | native_results.COLLECTION_CLI_REQUIRED, "native-media-tests.jsonl"),
            (2, native_results.COLLECTION_SERVICE_REQUIRED, "native-service-tests.jsonl"),
        )
        for index, names, report in groups:
            command = next(line for line in lines if "tee " + report in line)
            selector = re.search(r"-run '([^']+)'", command)
            if index == 0:
                self.assertIn("./pkg/engine", command)
                self.assertIn("./cmd/cutmy", command)
                self.assertIsNone(selector)
            original = [json.loads(line) for line in self.reports[index].read_text(encoding="utf-8").splitlines()]
            for name in names:
                self.assertIn(name, native_results.REQUIRED[index])
                if selector:
                    self.assertIsNotNone(re.fullmatch(selector.group(1), name))
                for action in ("missing", "skip", "failed-subtest"):
                    with self.subTest(name=name, action=action):
                        events = [event for event in original if action == "failed-subtest" or event.get("Test") != name]
                        if action != "missing":
                            events.append({"Action": "skip" if action == "skip" else "fail", "Test": name if action == "skip" else name + "/fault", "Package": native_results.APP_PACKAGE})
                        self.reports[index].write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                        self.assertNotEqual(self.run_gate().returncode, 0)
            self.reports[index].write_text("".join(json.dumps(event) + "\n" for event in original), encoding="utf-8")
        bookworm = next(line for line in lines if "tee bookworm-media-tests.jsonl" in line)
        selector = re.search(r"-run '([^']+)'", bookworm).group(1)
        for name in native_results.COLLECTION_SERVICE_REQUIRED:
            self.assertIsNone(re.fullmatch(selector, name), "Bookworm media job has no test PostgreSQL")

    def test_bookworm_preflight_checks_require_actual_complete_results(self):
        lines = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8").splitlines()
        command = next(line for line in lines if "tee bookworm-media-tests.jsonl" in line)
        selector = re.search(r"-run '([^']+)'", command).group(1)
        for name in native_results.PREFLIGHT_MEDIA_REQUIRED:
            self.assertIn(name, native_results.BOOKWORM_REQUIRED)
            self.assertIsNotNone(re.fullmatch(selector, name))
        for name in native_results.PREFLIGHT_SERVICE_REQUIRED:
            self.assertIsNone(re.fullmatch(selector, name), "Bookworm media job has no test PostgreSQL")
        path = Path(self.directory.name) / "bookworm-preflight.jsonl"
        events = [{"Action": "pass", "Test": name, "Package": native_results.APP_PACKAGE} for name in sorted(native_results.BOOKWORM_REQUIRED)]
        events.append({"Action": "pass", "Package": native_results.APP_PACKAGE})
        invocation = [sys.executable, str(SCRIPT), "--bookworm", str(path)]
        for name in native_results.PREFLIGHT_MEDIA_REQUIRED:
            for action in ("missing", "skip", "failed-subtest"):
                with self.subTest(name=name, action=action):
                    changed = [event for event in events if action == "failed-subtest" or event.get("Test") != name]
                    if action != "missing":
                        changed.append({"Action": "skip" if action == "skip" else "fail", "Test": name if action == "skip" else name + "/fault", "Package": native_results.APP_PACKAGE})
                    path.write_text("".join(json.dumps(event) + "\n" for event in changed), encoding="utf-8")
                    self.assertNotEqual(subprocess.run(invocation, capture_output=True, timeout=10).returncode, 0)

    def test_agent_projects_and_subtitles_cannot_be_missing_skipped_or_failed(self):
        lines = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8").splitlines()
        groups = (
            (0, native_results.SUBTITLE_CLI_REQUIRED, "native-media-tests.jsonl"),
            (2, native_results.AGENT_PROJECT_SERVICE_REQUIRED | native_results.SUBTITLE_SERVICE_REQUIRED, "native-service-tests.jsonl"),
            (3, native_results.AGENT_PROJECT_STORAGE_REQUIRED, "native-storage-tests.jsonl"),
        )
        for index, names, report in groups:
            command = next(line for line in lines if "tee " + report in line)
            selector = re.search(r"-run '([^']+)'", command)
            if index == 0:
                self.assertIn("./cmd/cutmy", command)
                self.assertIsNone(selector)
            original = [json.loads(line) for line in self.reports[index].read_text(encoding="utf-8").splitlines()]
            for name in names:
                self.assertIn(name, native_results.REQUIRED[index])
                if selector:
                    self.assertIsNotNone(re.fullmatch(selector.group(1), name))
                for action in ("missing", "skip", "failed-subtest"):
                    with self.subTest(name=name, action=action):
                        events = [event for event in original if action == "failed-subtest" or event.get("Test") != name]
                        if action != "missing":
                            events.append({"Action": "skip" if action == "skip" else "fail", "Test": name if action == "skip" else name + "/fault", "Package": native_results.APP_PACKAGE})
                        self.reports[index].write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                        self.assertNotEqual(self.run_gate().returncode, 0)
            self.reports[index].write_text("".join(json.dumps(event) + "\n" for event in original), encoding="utf-8")
        subtitle_source = (SCRIPT.parent.parent / "internal/app/subtitle_retime_test.go").read_text(encoding="utf-8")
        actual = set(re.findall(r"func (TestRetimeSubtitles\w+)\(", subtitle_source))
        self.assertTrue(actual)
        self.assertLessEqual(actual, native_results.SUBTITLE_SERVICE_REQUIRED)

    def test_preflight_public_contract_report_cannot_be_missing_or_skipped(self):
        lines = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8").splitlines()
        command = next(line for line in lines if "tee preflight-contract-tests.jsonl" in line)
        self.assertIn("CUTMY_REQUIRE_CONTRACT=1", command)
        selector = re.search(r"-run '([^']+)'", command).group(1)
        native_service = next(line for line in lines if "tee native-service-tests.jsonl" in line)
        native_selector = re.search(r"-run '([^']+)'", native_service).group(1)
        names = native_results.PREFLIGHT_CONTRACT_REQUIRED | native_results.COLLECTION_CONTRACT_REQUIRED | native_results.AGENT_PROJECT_CONTRACT_REQUIRED
        for name in names:
            self.assertIsNotNone(re.fullmatch(selector, name))
            self.assertIsNone(re.fullmatch(native_selector, name), "native matrix does not install contract Python dependencies")
        path = Path(self.directory.name) / "preflight-contract.jsonl"
        events = [{"Action": "pass", "Test": name, "Package": native_results.APP_PACKAGE} for name in sorted(names)]
        events.append({"Action": "pass", "Package": native_results.APP_PACKAGE})
        invocation = [sys.executable, str(SCRIPT), "--preflight-contract", str(path)]
        def invoke(changed):
            path.write_text("".join(json.dumps(event) + "\n" for event in changed), encoding="utf-8")
            return subprocess.run(invocation, capture_output=True, timeout=10)
        self.assertEqual(invoke(events).returncode, 0)
        for name in names:
            for action in ("missing", "skip", "failed-subtest"):
                with self.subTest(name=name, action=action):
                    changed = [event for event in events if action == "failed-subtest" or event.get("Test") != name]
                    if action != "missing":
                        changed.append({"Action": "skip" if action == "skip" else "fail", "Test": name if action == "skip" else name + "/fault", "Package": native_results.APP_PACKAGE})
                    self.assertNotEqual(invoke(changed).returncode, 0)
        self.assertNotEqual(invoke([event for event in events if event.get("Test")]).returncode, 0)

    def test_schema8_recovery_requires_previous_release_runtime_and_real_restore(self):
        workflow=(SCRIPT.parent.parent/".github/workflows/ci.yml").read_text(encoding="utf-8")
        command=next(line for line in workflow.splitlines() if "tee agent-project-recovery-tests.jsonl" in line)
        selector=re.search(r"-run '([^']+)'",command).group(1)
        self.assertIn('git archive e4a0477b727ccfbbf21f86027fdd07f6ffe78419 | tar -x -C "$previous_source"',workflow)
        self.assertIn('export CUTMY_TEST_PREVIOUS_SCHEMA007_BINARY="$previous_binary"',workflow)
        self.assertIn('export CUTMY_TEST_PREVIOUS_SCHEMA007_SHA256=',workflow)
        self.assertIn('scripts/check-native-results.py --agent-project-recovery agent-project-recovery-tests.jsonl',workflow)
        path=Path(self.directory.name)/"schema8-recovery.jsonl"
        names=native_results.AGENT_PROJECT_RECOVERY_REQUIRED
        events=[{"Action":"pass","Test":name,"Package":native_results.APP_PACKAGE} for name in sorted(names)]
        events.append({"Action":"pass","Package":native_results.APP_PACKAGE})
        invocation=[sys.executable,str(SCRIPT),"--agent-project-recovery",str(path)]
        def invoke(changed):
            path.write_text("".join(json.dumps(event)+"\n" for event in changed),encoding="utf-8")
            return subprocess.run(invocation,capture_output=True,timeout=10)
        self.assertEqual(invoke(events).returncode,0)
        for name in names:
            self.assertIsNotNone(re.fullmatch(selector,name))
            for action in ("missing","skip","failed-subtest"):
                with self.subTest(name=name,action=action):
                    changed=[event for event in events if action=="failed-subtest" or event.get("Test")!=name]
                    if action!="missing":changed.append({"Action":"skip" if action=="skip" else "fail","Test":name if action=="skip" else name+"/fault","Package":native_results.APP_PACKAGE})
                    self.assertNotEqual(invoke(changed).returncode,0)
        self.assertNotEqual(invoke([event for event in events if event.get("Test")]).returncode,0)

    def test_complete_reports_pass(self):
        self.assertEqual(self.run_gate().returncode, 0)

    def test_schema9_recovery_requires_previous_release_derived_pins_and_complete_restore(self):
        workflow = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        command = next(line for line in workflow.splitlines() if "tee external-audio-recovery-tests.jsonl" in line)
        selector = re.search(r"-run '([^']+)'", command).group(1)
        self.assertIn('git archive 106d0d2d7887540768cc21e3bd4e2c71629af6c4 | tar -x -C "$previous_source"', workflow)
        self.assertIn('export CUTMY_TEST_PREVIOUS_SCHEMA008_BINARY="$previous_binary"', workflow)
        self.assertIn('export CUTMY_TEST_PREVIOUS_SCHEMA008_SHA256=', workflow)
        self.assertIn('scripts/check-native-results.py --external-audio-recovery external-audio-recovery-tests.jsonl', workflow)
        path = Path(self.directory.name) / "schema9-recovery.jsonl"
        names = native_results.EXTERNAL_AUDIO_RECOVERY_REQUIRED
        events = [{"Action": "pass", "Test": name, "Package": native_results.APP_PACKAGE} for name in sorted(names)]
        events.append({"Action": "pass", "Package": native_results.APP_PACKAGE})
        invocation = [sys.executable, str(SCRIPT), "--external-audio-recovery", str(path)]
        def invoke(changed):
            path.write_text("".join(json.dumps(event) + "\n" for event in changed), encoding="utf-8")
            return subprocess.run(invocation, capture_output=True, timeout=10)
        self.assertEqual(invoke(events).returncode, 0)
        for name in names:
            self.assertIsNotNone(re.fullmatch(selector, name))
            for action in ("missing", "skip", "failed-subtest", "wrong-package"):
                with self.subTest(name=name, action=action):
                    changed = [event for event in events if action == "failed-subtest" or event.get("Test") != name]
                    if action != "missing":
                        changed.append({"Action": "pass" if action == "wrong-package" else "skip" if action == "skip" else "fail",
                                        "Test": name + "/fault" if action == "failed-subtest" else name,
                                        "Package": native_results.ENGINE_PACKAGE if action == "wrong-package" else native_results.APP_PACKAGE})
                    self.assertNotEqual(invoke(changed).returncode, 0)
        self.assertNotEqual(invoke([event for event in events if event.get("Test")]).returncode, 0)
        self.assertLessEqual(native_results.EXTERNAL_AUDIO_STORAGE_REQUIRED, native_results.REQUIRED[3])
        storage = next(line for line in workflow.splitlines() if "tee native-storage-tests.jsonl" in line)
        storage_selector = re.search(r"-run '([^']+)'", storage).group(1)
        for name in native_results.EXTERNAL_AUDIO_STORAGE_REQUIRED:
            self.assertIsNotNone(re.fullmatch(storage_selector, name))
        self.assert_required_results_reject_mutations(
            ((3, native_results.APP_PACKAGE, native_results.EXTERNAL_AUDIO_STORAGE_REQUIRED),),
            lambda index, package: native_results.ENGINE_PACKAGE, lambda index: 4)

    def test_core16_results_require_real_packages_and_current_workflow_paths(self):
        workflow = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        service = next(line for line in workflow.splitlines() if "tee native-service-tests.jsonl" in line)
        selector = re.search(r"-run '([^']+)'", service).group(1)
        for name in native_results.CORE16_SERVICE_REQUIRED:
            self.assertIsNotNone(re.fullmatch(selector, name))
        cases = ((0, native_results.ENGINE_PACKAGE, native_results.CORE16_ENGINE_REQUIRED),
                 (0, native_results.CLI_PACKAGE, native_results.CORE16_CLI_REQUIRED),
                 (0, native_results.SUBTITLE_PACKAGE, native_results.CORE16_SUBTITLE_REQUIRED),
                 (2, native_results.APP_PACKAGE, native_results.CORE16_SERVICE_REQUIRED))
        self.assert_required_results_reject_mutations(cases,
            lambda index, package: native_results.APP_PACKAGE if index == 0 else native_results.ENGINE_PACKAGE,
            lambda index: 4)

    def test_core15_results_require_their_real_packages_and_selected_workflow_paths(self):
        workflow = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        complete = next(line for line in workflow.splitlines() if "tee native-media-tests.jsonl" in line)
        self.assertIn("./internal/edit", complete)
        service = next(line for line in workflow.splitlines() if "tee native-service-tests.jsonl" in line)
        service_selector = re.search(r"-run '([^']+)'", service).group(1)
        for name in native_results.CORE15_SERVICE_REQUIRED:
            self.assertIsNotNone(re.fullmatch(service_selector, name))
        cases = (
            (0, native_results.ENGINE_PACKAGE, native_results.CORE15_ENGINE_REQUIRED),
            (0, native_results.LOCALMEDIA_PACKAGE, native_results.CORE15_LOCALMEDIA_REQUIRED),
            (0, native_results.EDIT_PACKAGE, native_results.CORE15_EDIT_REQUIRED),
            (0, native_results.MEDIAEXPORT_PACKAGE, native_results.CORE15_MEDIAEXPORT_REQUIRED),
            (0, native_results.CLI_PACKAGE, native_results.CORE15_CLI_REQUIRED),
            (2, native_results.APP_PACKAGE, native_results.CORE15_SERVICE_REQUIRED),
        )
        self.assert_required_results_reject_mutations(cases,
            lambda index, package: native_results.APP_PACKAGE if index == 0 else native_results.ENGINE_PACKAGE,
            lambda index: 4 if index == 0 else 0)

    def test_bookworm_executes_and_requires_all_six_feature_runtime_paths(self):
        workflow = (SCRIPT.parent.parent / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        line = next(line for line in workflow.splitlines() if "tee bookworm-feature-tests.jsonl" in line)
        selector = re.search(r"-run '([^']+)'", line).group(1)
        self.assertIn("scripts/check-native-results.py --core15-bookworm bookworm-feature-tests.jsonl", workflow)
        path = Path(self.directory.name) / "bookworm-features.jsonl"
        names = native_results.CORE15_BOOKWORM_REQUIRED
        packages = {native_results.ENGINE_PACKAGE, native_results.LOCALMEDIA_PACKAGE, native_results.CLI_PACKAGE,
                    native_results.EDIT_PACKAGE, native_results.MEDIAEXPORT_PACKAGE}
        events = [{"Action": "pass", "Test": name, "Package": native_results.REQUIRED_TEST_PACKAGES[0][name]}
                  for name in sorted(names)]
        events += [{"Action": "pass", "Package": package} for package in sorted(packages)]
        command = [sys.executable, str(SCRIPT), "--core15-bookworm", str(path)]
        original = "".join(json.dumps(event) + "\n" for event in events)
        path.write_text(original, encoding="utf-8")
        self.assertEqual(subprocess.run(command, capture_output=True, timeout=10).returncode, 0)
        for name in names:
            self.assertIsNotNone(re.fullmatch(selector, name))
            for mutation in ("missing", "skip", "wrong-package"):
                with self.subTest(name=name, mutation=mutation):
                    changed = [event for event in events if event.get("Test") != name]
                    if mutation != "missing":
                        changed.append({"Action": "skip" if mutation == "skip" else "pass", "Test": name,
                                        "Package": native_results.APP_PACKAGE})
                    path.write_text("".join(json.dumps(event) + "\n" for event in changed), encoding="utf-8")
                    self.assertNotEqual(subprocess.run(command, capture_output=True, timeout=10).returncode, 0)
        path.write_text(original + json.dumps({"Action": "fail", "Package": native_results.ENGINE_PACKAGE,
                                              "Test": "TestRenderCanvasAndExternalAudioTimelineActualMedia/fault"}) + "\n", encoding="utf-8")
        self.assertNotEqual(subprocess.run(command, capture_output=True, timeout=10).returncode, 0)

    def test_each_required_report_is_mandatory(self):
        for path in self.reports:
            original = path.read_text(encoding="utf-8")
            with self.subTest(path=path.name):
                path.write_text("", encoding="utf-8")
                self.assertNotEqual(self.run_gate().returncode, 0)
            path.write_text(original, encoding="utf-8")

    def test_new_sdk_geometry_test_is_mandatory_in_complete_media_report(self):
        path = self.reports[0]
        events = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]
        events = [event for event in events if event.get("Test") != "TestEngineInspectSeparatesCodedAndDisplayDimensions"]
        path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
        result = self.run_gate()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("TestEngineInspectSeparatesCodedAndDisplayDimensions", result.stderr)

    def test_preview_cache_upgrade_is_mandatory_in_service_report(self):
        path = self.reports[2]
        events = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]
        events = [event for event in events if event.get("Test") != "TestPreviewMediaPolicyUpgradeDoesNotReuseLegacyCache"]
        path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
        result = self.run_gate()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("TestPreviewMediaPolicyUpgradeDoesNotReuseLegacyCache", result.stderr)

    def test_native_disk_admission_tests_are_mandatory_in_storage_report(self):
        path = self.reports[3]
        original = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]
        names = ("TestStorageAdmissionChecksFilesystemSpaceIndependentlyOfLogicalCap",
                 "TestStorageAdmissionChecksNearestExistingFilesystem",
                 "TestStorageAdmissionIncludesOutstandingReservationsAndSafetyHeadroom")
        for name in names:
            self.assertIn(name, native_results.REQUIRED[3])
            for action in ("missing", "skip"):
                with self.subTest(name=name, action=action):
                    events = [event for event in original if event.get("Test") != name]
                    if action == "skip":
                        events.append({"Action": "skip", "Test": name, "Package": native_results.APP_PACKAGE})
                    path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                    result = self.run_gate()
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(name, result.stderr)

    def test_source_pin_audit_budget_tests_are_mandatory_in_storage_report(self):
        names = ("TestSourcePinAuditOwnedBudgetKeepsContinuationAndAcknowledgedRepairs",
                 "TestSourcePinAuditOwnedBudgetPreservesGenuineFailures",
                 "TestSourcePinAuditParentCancellationHasPrecedence",
                 "TestSourcePinAuditUnknownCommitResumesDurableState",
                 "TestSourcePinAuditBlockedAfterMediaCommitKeepsDrainablePaths",
                 "TestSourcePinAuditParentCanceledAfterMediaCommitKeepsDrainablePaths")
        self.assertEqual({name for name in native_results.REQUIRED[3] if name.startswith("TestSourcePinAudit")}, set(names))
        workflow = SCRIPT.parent.parent / ".github" / "workflows" / "ci.yml"
        commands = [line for line in workflow.read_text(encoding="utf-8").splitlines()
                    if "tee native-storage-tests.jsonl" in line]
        self.assertEqual(len(commands), 1)
        selector = re.search(r"-run '([^']+)'", commands[0])
        self.assertIsNotNone(selector)
        for name in names:
            self.assertIsNotNone(re.fullmatch(selector.group(1), name))
        path = self.reports[3]
        original = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]
        for name in names:
            for action in ("missing", "skip", "failed-subtest"):
                with self.subTest(name=name, action=action):
                    events = [event for event in original if action == "failed-subtest" or event.get("Test") != name]
                    if action == "skip":
                        events.append({"Action": "skip", "Test": name, "Package": native_results.APP_PACKAGE})
                    elif action == "failed-subtest":
                        events.append({"Action": "fail", "Test": name + "/fault", "Package": native_results.APP_PACKAGE})
                    path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                    result = self.run_gate()
                    self.assertNotEqual(result.returncode, 0)
                    if action != "failed-subtest":
                        self.assertIn(name, result.stderr)

    def test_scan_batch_correctness_is_mandatory_on_every_native_target(self):
        names = ("TestStorageScanSessionDeadlineBatchesPreserveAcknowledgedPrefix",
                 "TestStorageScanSchedulingBatchFailuresPreserveClocks",
                 "TestStorageScanSessionReadBatchPreservesNonISODateStyle",
                 "TestStorageScanSessionReadBatchLookupFailureRollsBackAndRecovers")
        self.assertEqual(len(native_results.REQUIRED[3]), 48 + len(native_results.UNIVERSAL_STORAGE_REQUIRED) + len(native_results.AGENT_PROJECT_STORAGE_REQUIRED) + len(native_results.EXTERNAL_AUDIO_STORAGE_REQUIRED))
        self.assertEqual(sum(map(len, native_results.REQUIRED)), 124 + sum(map(len, (native_results.UNIVERSAL_ENGINE_REQUIRED, native_results.UNIVERSAL_SERVICE_REQUIRED, native_results.UNIVERSAL_STORAGE_REQUIRED, native_results.UNIVERSAL_MEDIA_REQUIRED, native_results.PREFLIGHT_ENGINE_REQUIRED, native_results.PREFLIGHT_CLI_REQUIRED, native_results.PREFLIGHT_SERVICE_REQUIRED, native_results.PREFLIGHT_MEDIA_REQUIRED, native_results.POOL_SERVICE_REQUIRED, native_results.DRAFT_CACHE_TRANSFER_REQUIRED, native_results.DRAFT_CACHE_DATABASE_REQUIRED, native_results.COLLECTION_ENGINE_REQUIRED, native_results.COLLECTION_CLI_REQUIRED, native_results.COLLECTION_SERVICE_REQUIRED, native_results.AGENT_PROJECT_SERVICE_REQUIRED, native_results.AGENT_PROJECT_STORAGE_REQUIRED, native_results.SUBTITLE_SERVICE_REQUIRED, native_results.SUBTITLE_CLI_REQUIRED, native_results.AUDIT_MEDIA_REQUIRED, native_results.AUDIT_SERVICE_REQUIRED, native_results.PROJECT_CONTRACT_SERVICE_REQUIRED, native_results.EDIT_MODEL_SERVICE_REQUIRED, native_results.EDIT_MODEL_ENGINE_REQUIRED, native_results.SUBTITLE_MODULE_REQUIRED, native_results.MEDIA_BOUNDARY_ENGINE_REQUIRED, native_results.TOOLRUN_MODULE_REQUIRED, native_results.MEDIAINFO_MODULE_REQUIRED, native_results.MEDIAINFO_ADAPTER_REQUIRED, native_results.MEDIAEXPORT_MODULE_REQUIRED, native_results.MEDIAEXPORT_ADAPTER_REQUIRED, native_results.JOINED_MEDIAEXPORT_REQUIRED, native_results.MEDIAPREFLIGHT_MODULE_REQUIRED, native_results.MEDIAPREFLIGHT_ADAPTER_REQUIRED, native_results.MEDIASESSION_MODULE_REQUIRED, native_results.SOURCE_SUBTITLE_MODULE_REQUIRED, native_results.MEDIASESSION_ADAPTER_REQUIRED, native_results.COREMODEL_MODULE_REQUIRED, native_results.MEDIAERROR_MODULE_REQUIRED, native_results.LOCALMEDIA_MODULE_REQUIRED, native_results.COREMODEL_MEDIAERROR_ADAPTER_REQUIRED, native_results.ENGINE_DEPENDENCY_REQUIRED, native_results.TOOLRUN_LIFECYCLE_REQUIRED, native_results.PLATFORM_ORIGINAL_AUDIO_REQUIRED, native_results.CORE15_BOOKWORM_REQUIRED, native_results.CORE15_SERVICE_REQUIRED, native_results.EXTERNAL_AUDIO_STORAGE_REQUIRED, native_results.CORE16_ENGINE_REQUIRED, native_results.CORE16_CLI_REQUIRED, native_results.CORE16_SUBTITLE_REQUIRED, native_results.CORE16_SERVICE_REQUIRED))))
        workflow = SCRIPT.parent.parent / ".github" / "workflows" / "ci.yml"
        commands = [line for line in workflow.read_text(encoding="utf-8").splitlines()
                    if "tee native-storage-tests.jsonl" in line]
        self.assertEqual(len(commands), 1)
        selector = re.search(r"-run '([^']+)'", commands[0])
        self.assertIsNotNone(selector)
        self.assertIsNone(re.fullmatch(selector.group(1), "TestStorageScanRepeatedFiveMillisecondCyclesMakeDurableProgress"))
        path = self.reports[3]
        original = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]
        for name in names:
            self.assertIn(name, native_results.REQUIRED[3])
            self.assertIsNotNone(re.fullmatch(selector.group(1), name))
            for action in ("missing", "skip", "failed-subtest"):
                with self.subTest(name=name, action=action):
                    events = [event for event in original if action == "failed-subtest" or event.get("Test") != name]
                    if action == "skip":
                        events.append({"Action": "skip", "Test": name, "Package": native_results.APP_PACKAGE})
                    elif action == "failed-subtest":
                        events.append({"Action": "fail", "Test": name + "/fault", "Package": native_results.APP_PACKAGE})
                    path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                    result = self.run_gate()
                    self.assertNotEqual(result.returncode, 0)
                    if action != "failed-subtest":
                        self.assertIn(name, result.stderr)

    def test_false_preview_pins_are_mandatory_in_storage_report(self):
        name = "TestSourceRetentionFalsePreviewZerosPreservePinsAndBatchLimit"
        self.assertIn(name, native_results.REQUIRED[3])
        workflow = SCRIPT.parent.parent / ".github" / "workflows" / "ci.yml"
        commands = [line for line in workflow.read_text(encoding="utf-8").splitlines()
                    if "tee native-storage-tests.jsonl" in line]
        self.assertEqual(len(commands), 1)
        selector = re.search(r"-run '([^']+)'", commands[0])
        self.assertIsNotNone(selector)
        self.assertIsNotNone(re.fullmatch(selector.group(1), name))
        path = self.reports[3]
        original = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]
        for action in ("missing", "skip", "failed-subtest"):
            with self.subTest(action=action):
                events = [event for event in original if action == "failed-subtest" or event.get("Test") != name]
                if action == "skip":
                    events.append({"Action": "skip", "Test": name, "Package": native_results.APP_PACKAGE})
                elif action == "failed-subtest":
                    events.append({"Action": "fail", "Test": name + "/fault", "Package": native_results.APP_PACKAGE})
                path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
                result = self.run_gate()
                self.assertNotEqual(result.returncode, 0)
                if action != "failed-subtest":
                    self.assertIn(name, result.stderr)

    def test_passed_parent_cannot_hide_failed_or_skipped_subtest(self):
        path = self.reports[-1]
        original = path.read_text(encoding="utf-8")
        for action in ("fail", "skip"):
            with self.subTest(action=action):
                event = {"Action": action, "Test": "TestMediaContractHDRToneMappingAndCopy/pq", "Package": "native/package"}
                path.write_text(original + json.dumps(event) + "\n", encoding="utf-8")
                self.assertNotEqual(self.run_gate().returncode, 0)

    def test_old_four_report_invocation_is_rejected(self):
        self.assertNotEqual(self.run_gate(self.reports[:-1]).returncode, 0)

    def test_passed_tops_cannot_hide_truncated_package_reports(self):
        for path in self.reports:
            original = path.read_text(encoding="utf-8")
            events = [json.loads(line) for line in original.splitlines()]
            with self.subTest(path=path.name):
                path.write_text("".join(json.dumps(event) + "\n" for event in events if event.get("Test")), encoding="utf-8")
                result = self.run_gate()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("incomplete_packages", result.stderr)
            path.write_text(original, encoding="utf-8")

    def test_bookworm_requires_complete_unskipped_nineteen_test_report(self):
        path = Path(self.directory.name) / "bookworm.jsonl"
        events = [{"Action": "pass", "Test": name, "Package": native_results.APP_PACKAGE}
                  for name in sorted(native_results.BOOKWORM_REQUIRED)]
        events.append({"Action": "pass", "Package": native_results.APP_PACKAGE})
        original = "".join(json.dumps(event) + "\n" for event in events)
        path.write_text(original, encoding="utf-8")
        command = [sys.executable, str(SCRIPT), "--bookworm", str(path)]
        self.assertEqual(subprocess.run(command, capture_output=True, timeout=10).returncode, 0)
        for absent in ("TestCopyPreservesFirstKeyframeAndAudioTimeline", "TestHLSMediaContractHDRTags"):
            with self.subTest(absent=absent):
                path.write_text("".join(json.dumps(event) + "\n" for event in events if event.get("Test") != absent), encoding="utf-8")
                self.assertNotEqual(subprocess.run(command, capture_output=True, timeout=10).returncode, 0)
        for action in ("fail", "skip"):
            with self.subTest(action=action):
                event = {"Action": action, "Test": "TestHLSMediaContractHDRTags/pq", "Package": "native/package"}
                path.write_text(original + json.dumps(event) + "\n", encoding="utf-8")
                self.assertNotEqual(subprocess.run(command, capture_output=True, timeout=10).returncode, 0)
        path.write_text("".join(json.dumps(event) + "\n" for event in events if event.get("Test")), encoding="utf-8")
        self.assertNotEqual(subprocess.run(command, capture_output=True, timeout=10).returncode, 0)


if __name__ == "__main__":
    unittest.main()
