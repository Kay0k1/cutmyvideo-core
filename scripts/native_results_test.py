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
                if name.startswith("TestEngine") or name in native_results.PREFLIGHT_ENGINE_REQUIRED | native_results.COLLECTION_ENGINE_REQUIRED:
                    package = native_results.ENGINE_PACKAGE
                elif name.startswith("TestCLI"):
                    package = native_results.CLI_PACKAGE
                elif name == "TestConcurrentPublicationHasExactlyOneCompleteWinner":
                    package = native_results.FS_PACKAGE
                events.append({"Action": "pass", "Test": name, "Package": package})
            events.extend({"Action": "pass", "Package": package} for package in sorted(native_results.REQUIRED_PACKAGES[index]))
            path.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
            self.reports.append(path)

    def run_gate(self, reports=None):
        return subprocess.run([sys.executable, str(SCRIPT), *map(str, self.reports if reports is None else reports)],
                              capture_output=True, text=True, timeout=10)

    def test_new_release_features_are_required_and_selected_on_all_native_targets(self):
        workflow = SCRIPT.parent.parent / ".github" / "workflows" / "ci.yml"
        lines = workflow.read_text(encoding="utf-8").splitlines()
        for index, names, report in ((2, native_results.NEW_SERVICE_REQUIRED | native_results.EFFICIENT_CUTTING_SERVICE_REQUIRED | native_results.UNIVERSAL_SERVICE_REQUIRED, "native-service-tests.jsonl"),
                                     (3, native_results.EFFICIENT_CUTTING_STORAGE_REQUIRED | native_results.UNIVERSAL_STORAGE_REQUIRED, "native-storage-tests.jsonl"),
                                     (4, native_results.NEW_MEDIA_REQUIRED | native_results.EFFICIENT_CUTTING_MEDIA_REQUIRED | native_results.UNIVERSAL_MEDIA_REQUIRED | {"TestAdvancedCropPlanPublishesActualCroppedOutput"}, "native-media-contract-tests.jsonl")):
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

    def test_each_required_report_is_mandatory(self):
        for path in self.reports:
            original = path.read_text(encoding="utf-8")
            with self.subTest(path=path.name):
                path.write_text("", encoding="utf-8")
                self.assertNotEqual(self.run_gate().returncode, 0)
            path.write_text(original, encoding="utf-8")

    def test_new_sdk_geometry_test_is_mandatory_in_contract_report(self):
        path = self.reports[-1]
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
        self.assertEqual(len(native_results.REQUIRED[3]), 48 + len(native_results.UNIVERSAL_STORAGE_REQUIRED) + len(native_results.AGENT_PROJECT_STORAGE_REQUIRED))
        self.assertEqual(sum(map(len, native_results.REQUIRED)), 122 + sum(map(len, (native_results.UNIVERSAL_ENGINE_REQUIRED, native_results.UNIVERSAL_SERVICE_REQUIRED, native_results.UNIVERSAL_STORAGE_REQUIRED, native_results.UNIVERSAL_MEDIA_REQUIRED, native_results.PREFLIGHT_ENGINE_REQUIRED, native_results.PREFLIGHT_CLI_REQUIRED, native_results.PREFLIGHT_SERVICE_REQUIRED, native_results.PREFLIGHT_MEDIA_REQUIRED, native_results.COLLECTION_ENGINE_REQUIRED, native_results.COLLECTION_CLI_REQUIRED, native_results.COLLECTION_SERVICE_REQUIRED, native_results.AGENT_PROJECT_SERVICE_REQUIRED, native_results.AGENT_PROJECT_STORAGE_REQUIRED, native_results.SUBTITLE_SERVICE_REQUIRED, native_results.SUBTITLE_CLI_REQUIRED))))
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
