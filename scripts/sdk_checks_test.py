"""Negative controls for installed SDK acceptance, including omitted prerequisites."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("sdk_checks", Path(__file__).with_name("check-sdks.py"))
CHECKS = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECKS)


class InstalledSDKResultControls(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.report = Path(self.directory.name) / "tests.jsonl"
        self.required = {"TestInstalledSDKLocalWorkflow", "TestInstalledSDKHTTPWorkflow", "TestInstalledSDKCollectionLocalWorkflow", "TestInstalledSDKCollectionHTTPWorkflow"}
        self.required.add("TestInstalledSDKAgentProjectWorkflow")
        self.required |= {f"{name}/{language}" for name in tuple(self.required) for language in ("python", "typescript")}
        self.events = [{"Package": CHECKS.APP_PACKAGE, "Test": name, "Action": "pass"} for name in sorted(self.required)]
        self.events.append({"Package": CHECKS.APP_PACKAGE, "Action": "pass"})

    def verify(self, events):
        self.report.write_text("\n".join(json.dumps(event) for event in events) + "\n", encoding="utf-8")
        CHECKS.verify_go_report(self.report, self.required)

    def test_requires_both_installed_consumers_and_complete_package(self):
        self.verify(self.events)
        for excluded in range(len(self.events)):
            with self.subTest(excluded=excluded), self.assertRaises(RuntimeError):
                self.verify(self.events[:excluded] + self.events[excluded + 1:])

    def test_skipped_or_failed_parent_subtest_or_package_cannot_pass(self):
        for name in [*self.required, "TestInstalledSDKLocalWorkflow/python", "TestInstalledSDKHTTPWorkflow/typescript", None]:
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                event = {"Package": CHECKS.APP_PACKAGE, "Action": "fail"}
                if name:
                    event["Test"] = name
                self.verify(self.events + [event])
        for name in self.required:
            with self.subTest(skipped=name), self.assertRaises(RuntimeError):
                self.verify(self.events + [{"Package": CHECKS.APP_PACKAGE, "Test": name, "Action": "skip"}])

    def test_other_package_and_malformed_records_do_not_certify_sdk(self):
        with self.assertRaises(RuntimeError):
            self.verify([{**event, "Package": "unrelated"} for event in self.events])
        self.report.write_text("{truncated", encoding="utf-8")
        with self.assertRaises(ValueError):
            CHECKS.verify_go_report(self.report, self.required)

    def test_shared_contract_examples_are_frozen_outside_the_checkout(self):
        inputs = CHECKS.collection_inputs()
        expected = inputs[CHECKS.PROJECT_CONTRACTS]
        copied = CHECKS.copy_project_contracts(Path(self.directory.name), expected)
        self.assertEqual(copied.read_bytes(), (CHECKS.ROOT / CHECKS.PROJECT_CONTRACTS).read_bytes())
        self.assertEqual(CHECKS.sha256(copied), expected)
        self.assertFalse(copied.is_relative_to(CHECKS.ROOT))
        with self.assertRaises(RuntimeError):
            CHECKS.copy_project_contracts(Path(self.directory.name), "0" * 64)


if __name__ == "__main__":
    unittest.main()
