"""Negative controls for installed SDK acceptance, including omitted prerequisites."""
import importlib.util
import json
from pathlib import Path, PureWindowsPath
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("sdk_checks", Path(__file__).with_name("check-sdks.py"))
CHECKS = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECKS)


class InstalledSDKResultControls(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.report = Path(self.directory.name) / "tests.jsonl"
        self.required = {"TestInstalledSDKLocalWorkflow", "TestInstalledSDKHTTPWorkflow", "TestInstalledSDKCollectionLocalWorkflow", "TestInstalledSDKCollectionHTTPWorkflow"}
        self.required.update({"TestInstalledSDKAgentProjectWorkflow", "TestInstalledSDKMediaLocalWorkflow", "TestInstalledSDKMediaHTTPWorkflow"})
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

    def test_source_inputs_use_git_paths_for_windows_relative_names(self):
        # Keep real filesystem access while presenting the relative names as
        # pathlib does on Windows. The inventory must use Git's slash paths.
        class WindowsRelativeNames(type(Path())):
            def relative_to(self, *other, **kwargs):
                return PureWindowsPath(super().relative_to(*other, **kwargs))

        with patch.object(CHECKS, "ROOT", WindowsRelativeNames(CHECKS.ROOT)):
            inputs = CHECKS.collection_inputs()
        self.assertIn(CHECKS.PROJECT_CONTRACTS, inputs)
        self.assertTrue(all("\\" not in name for name in inputs))
        self.assertEqual(inputs[CHECKS.PROJECT_CONTRACTS], CHECKS.sha256(CHECKS.ROOT / CHECKS.PROJECT_CONTRACTS))


class InstalledSDKScratchControls(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.output = Path(self.directory.name) / "acceptance"

    def create_scratch(self):
        self.output.mkdir()
        owned_output = self.output.lstat()
        for name in ("python-consumer", "node-consumer", "python-source", "python-rebuilt"):
            path = self.output / name
            path.mkdir()
            (path / "scratch").write_bytes(b"disposable")
        (self.output / ("cutmy.exe" if CHECKS.os.name == "nt" else "cutmy")).write_bytes(b"cli")
        return owned_output

    def symlink(self, link, target):
        try:
            link.symlink_to(target, target_is_directory=True)
        except (OSError, NotImplementedError) as error:
            self.skipTest(f"Directory symlinks unavailable: {error}")

    def test_capacity_refuses_before_builds_and_accepts_exact_threshold(self):
        with patch.object(CHECKS.shutil, "disk_usage") as usage, \
                patch.object(CHECKS, "run") as run, \
                patch.object(CHECKS, "collection_inputs") as inputs, \
                patch.object(CHECKS.sys, "argv", ["check-sdks.py", "--output", str(self.output)]):
            usage.return_value.free = CHECKS.MINIMUM_FREE - 1
            with self.assertRaisesRegex(RuntimeError, "at least 2 GiB.*output filesystem"):
                CHECKS.main()
            run.assert_not_called()
            inputs.assert_not_called()
            usage.assert_called_once_with(self.output.resolve())
            usage.return_value.free = CHECKS.MINIMUM_FREE
            CHECKS.require_output_capacity(self.output)

    def test_success_cleanup_preserves_release_artifacts_and_diagnostics(self):
        owned_output = self.create_scratch()
        retained = {
            "python-dist/sdk.whl": b"wheel", "python-dist/sdk.tar.gz": b"sdist",
            "sdk.tgz": b"npm", "testdata/project-contracts.json": b"frozen corpus",
            "python-build.log": b"build diagnostics", "installed-sdk-tests.jsonl": b"acceptance events",
            "SDK-CHECKS.json": b"receipt", "unrelated/keep": b"not runner scratch",
        }
        for name, data in retained.items():
            path = self.output / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
        CHECKS.cleanup_disposable(self.output, owned_output)
        actual = {path.relative_to(self.output).as_posix(): path.read_bytes()
                  for path in self.output.rglob("*") if path.is_file()}
        self.assertEqual(actual, retained)

    def test_cleanup_does_not_follow_nested_symlink(self):
        owned_output = self.create_scratch()
        outside = Path(self.directory.name) / "outside"
        outside.mkdir()
        sentinel = outside / "keep"
        sentinel.write_bytes(b"outside the run")
        self.symlink(self.output / "python-source" / "external", outside)
        CHECKS.cleanup_disposable(self.output, owned_output)
        self.assertEqual(sentinel.read_bytes(), b"outside the run")

    def test_cleanup_rejects_replaced_scratch_before_deleting_anything(self):
        owned_output = self.create_scratch()
        scratch = self.output / "python-source"
        scratch.rename(self.output / "renamed-source")
        outside = Path(self.directory.name) / "outside"
        outside.mkdir()
        self.symlink(scratch, outside)
        with self.assertRaisesRegex(RuntimeError, "Unsafe SDK scratch path"):
            CHECKS.cleanup_disposable(self.output, owned_output)
        self.assertTrue((self.output / "python-consumer" / "scratch").is_file())
        self.assertTrue((self.output / "node-consumer" / "scratch").is_file())
        self.assertTrue(scratch.is_symlink())

    def test_cleanup_rejects_replaced_output(self):
        owned_output = self.create_scratch()
        original = self.output.with_name("original")
        self.output.rename(original)
        self.output.mkdir()
        with self.assertRaisesRegex(RuntimeError, "evidence directory changed"):
            CHECKS.cleanup_disposable(self.output, owned_output)
        self.assertTrue((original / "python-consumer" / "scratch").is_file())

    def test_failed_acceptance_retains_scratch_and_diagnostics(self):
        def failed_run(*args, **kwargs):
            (self.output / "python-consumer").mkdir()
            (self.output / "python-consumer" / "scratch").write_bytes(b"debug this failure")
            kwargs["log"].write_bytes(b"acceptance failed")
            raise RuntimeError("injected acceptance failure")

        with patch.object(CHECKS.shutil, "disk_usage") as usage, \
                patch.object(CHECKS.shutil, "which", return_value="unused"), \
                patch.object(CHECKS, "run", side_effect=failed_run), \
                patch.object(CHECKS.sys, "argv", ["check-sdks.py", "--output", str(self.output)]):
            usage.return_value.free = CHECKS.MINIMUM_FREE
            with self.assertRaisesRegex(RuntimeError, "injected acceptance failure"):
                CHECKS.main()
        self.assertEqual((self.output / "python-consumer" / "scratch").read_bytes(), b"debug this failure")
        self.assertEqual((self.output / "collection-content-controls.log").read_bytes(), b"acceptance failed")
        self.assertTrue((self.output / "testdata" / "project-contracts.json").is_file())
        self.assertFalse((self.output / "SDK-CHECKS.json").exists())


if __name__ == "__main__":
    unittest.main()
