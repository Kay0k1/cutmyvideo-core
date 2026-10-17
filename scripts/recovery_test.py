"""Negative controls for the historical upgrade's record-preservation gate."""
import copy
from contextlib import redirect_stderr
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch


module = importlib.util.spec_from_file_location("recovery", Path(__file__).with_name("recovery-acceptance.py"))
recovery = importlib.util.module_from_spec(module)
module.loader.exec_module(recovery)


class UpgradeSnapshotChecks(unittest.TestCase):
    def setUp(self):
        self.before = {
            "sources": [{"id": "src_old", "owner": "owner_a", "path": "/fixture/source", "duration_ms": 8000}],
            "jobs": [{"id": "job_old", "source_id": "src_old", "owner": "owner_a", "status": "running", "attempts": 1,
                      "lease_token": "lease_old", "lease_until": "2026-10-10T22:15:00+00:00",
                      "items": [{"id": "item_old", "status": "succeeded", "artifact_id": "art_old"}]}],
            "artifacts": [{"id": "art_old", "job_id": "job_old", "path": "/fixture/artifact", "size_bytes": 32, "expires_at": None}],
            "storage_files": [
                {"path": "/fixture/source", "owner": "owner_a", "kind": "source", "resource_id": "src_old", "size_bytes": 100, "delete_pending": False},
                {"path": "/fixture/pending", "owner": "owner_a", "kind": "tombstone", "resource_id": "", "size_bytes": 8, "delete_pending": True},
            ],
            "storage_reservations": [{"id": "reservation_old", "job_id": "src_old", "kind": "preview", "owner": "owner_a", "size_bytes": 99, "token": "lease_old"}],
            "storage_counters": [{"id": 1, "stored_bytes": 108, "reserved_bytes": 99}],
            "queue_owners": [{"owner": "owner_a", "pending": 1}],
        }
        self.after = copy.deepcopy(self.before)
        for row in self.after["storage_files"]:
            row.update(delete_retry_at="-infinity", delete_failures=0)
        for row in self.after["sources"]:
            row.update(retention_job_pins=1, retention_preview_pins=1, retention_pins_valid=True, retention_pin_revision=0, media_removed=False)
        for row in self.after["jobs"]:
            row["audio_source_id"] = None

    def rejects(self, mutate):
        changed = copy.deepcopy(self.after)
        mutate(changed)
        with self.assertRaises(RuntimeError):
            recovery.verify_upgrade_snapshot(self.before, changed)

    def test_exact_additions_preserve_all_historical_records(self):
        before, after = copy.deepcopy(self.before), copy.deepcopy(self.after)
        self.assertEqual(recovery.verify_upgrade_snapshot(before, after), 2)
        self.assertEqual(before, self.before)
        self.assertEqual(after, self.after)

    def test_row_order_does_not_hide_or_invent_changes(self):
        self.after["storage_files"].reverse()
        self.assertEqual(recovery.verify_upgrade_snapshot(self.before, self.after), 2)

    def test_empty_ledger_is_valid(self):
        self.before["storage_files"] = []
        self.after["storage_files"] = []
        self.assertEqual(recovery.verify_upgrade_snapshot(self.before, self.after), 0)

    def test_changed_historical_fields_are_rejected(self):
        for table, field, value in (
                ("sources", "owner", "owner_b"), ("jobs", "lease_token", "new_lease"),
                ("jobs", "attempts", 2), ("jobs", "lease_until", None),
                ("artifacts", "size_bytes", 31), ("storage_files", "size_bytes", 101),
                ("storage_files", "delete_pending", True), ("storage_reservations", "size_bytes", 0),
                ("storage_counters", "stored_bytes", 0), ("queue_owners", "pending", 0)):
            with self.subTest(table=table, field=field):
                self.rejects(lambda s: s[table][0].update({field: value}))

    def test_changed_nested_item_is_rejected(self):
        self.rejects(lambda s: s["jobs"][0]["items"][0].update(artifact_id="different_artifact"))

    def test_dropped_historical_field_is_rejected(self):
        self.rejects(lambda s: s["storage_files"][0].pop("resource_id"))

    def test_unknown_additive_column_is_rejected(self):
        for table in ("storage_files", "jobs", "sources"):
            with self.subTest(table=table):
                self.rejects(lambda s: s[table][0].update(unknown_column=0))

    def test_missing_new_column_is_rejected(self):
        for field in ("delete_retry_at", "delete_failures"):
            with self.subTest(field=field):
                self.rejects(lambda s: s["storage_files"][0].pop(field))

    def test_wrong_defaults_are_rejected_on_every_row(self):
        for index in (0, 1):
            for field, value in (("delete_retry_at", None), ("delete_retry_at", "infinity"),
                                 ("delete_retry_at", "2026-10-10T22:15:00+00:00"),
                                 ("delete_failures", 1), ("delete_failures", "0"),
                                 ("delete_failures", False), ("delete_failures", 0.0)):
                with self.subTest(index=index, field=field, value=value):
                    self.rejects(lambda s: s["storage_files"][index].update({field: value}))

    def test_missing_row_is_rejected_in_every_table(self):
        for table in self.after:
            with self.subTest(table=table):
                self.rejects(lambda s: s[table].pop())

    def test_added_row_is_rejected(self):
        self.rejects(lambda s: s["storage_files"].append({**s["storage_files"][0], "path": "/fixture/new"}))

    def test_duplicate_count_is_preserved(self):
        self.before["storage_files"].append(copy.deepcopy(self.before["storage_files"][0]))
        with self.assertRaises(RuntimeError):
            recovery.verify_upgrade_snapshot(self.before, self.after)

    def test_table_set_changes_are_rejected(self):
        self.rejects(lambda s: s.pop("queue_owners"))
        self.rejects(lambda s: s.update(unexpected_table=[]))

    def test_source_pins_are_required_typed_and_exact_on_every_source(self):
        self.before["sources"].append({"id": "src_empty", "owner": "owner_b", "path": "/fixture/empty", "duration_ms": 9000})
        self.after["sources"].append({**self.before["sources"][-1], "retention_job_pins": 0, "retention_preview_pins": 0,
                                       "retention_pins_valid": True, "retention_pin_revision": 0, "media_removed": False})
        self.assertEqual(recovery.verify_upgrade_snapshot(self.before, self.after), 2)
        for index in (0, 1):
            for field in ("retention_job_pins", "retention_preview_pins", "retention_pins_valid", "retention_pin_revision"):
                with self.subTest(index=index, field=field, operation="missing"):
                    self.rejects(lambda s: s["sources"][index].pop(field))
                for value in (None, False, 1.0, "1", -1, 2):
                    with self.subTest(index=index, field=field, value=value):
                        self.rejects(lambda s: s["sources"][index].update({field: value}))

    def test_pins_count_all_jobs_and_only_exact_preview_membership(self):
        self.before["jobs"].append({**self.before["jobs"][0], "id": "job_done", "status": "succeeded", "owner": "owner_b"})
        self.before["storage_reservations"].extend([
            {**self.before["storage_reservations"][0], "id": "expired_foreign", "owner": "owner_b", "expires_at": "2000-01-01T00:00:00Z"},
            {**self.before["storage_reservations"][0], "id": "cached", "kind": "cached_preview"},
            {**self.before["storage_reservations"][0], "id": "artifact", "kind": "artifact"},
            {**self.before["storage_reservations"][0], "id": "other", "job_id": "src_elsewhere"},
        ])
        self.after["jobs"] = copy.deepcopy(self.before["jobs"])
        for row in self.after["jobs"]:
            row["audio_source_id"] = None
        self.after["storage_reservations"] = copy.deepcopy(self.before["storage_reservations"])
        self.after["sources"][0].update(retention_job_pins=2, retention_preview_pins=2)
        self.assertEqual(recovery.verify_upgrade_snapshot(self.before, self.after), 2)
        self.rejects(lambda s: s["sources"][0].update(retention_job_pins=1))
        self.rejects(lambda s: s["sources"][0].update(retention_preview_pins=4))

    def test_historical_source_fields_still_match_after_valid_backfill(self):
        self.rejects(lambda s: s["sources"][0].update(id="src_elsewhere"))
        self.rejects(lambda s: s["sources"][0].pop("path"))
        self.rejects(lambda s: s["sources"].append(copy.deepcopy(s["sources"][0])))

    def test_cleanup_default_must_preserve_original_media(self):
        self.rejects(lambda s: s["sources"][0].pop("media_removed"))
        for value in (True, None, 0, "false"):
            with self.subTest(value=value):
                self.rejects(lambda s: s["sources"][0].update(media_removed=value))

    def test_fixture_must_represent_pre_005_schema(self):
        for field in ("retention_job_pins", "retention_preview_pins", "retention_pins_valid", "retention_pin_revision"):
            before = copy.deepcopy(self.before)
            before["sources"][0][field] = 0
            with self.subTest(field=field), self.assertRaises(RuntimeError):
                recovery.verify_upgrade_snapshot(before, self.after)

    def test_external_audio_addition_is_exact_null_on_every_historical_job(self):
        self.before["jobs"].append({**self.before["jobs"][0], "id": "job_other"})
        self.after["jobs"].append({**self.after["jobs"][0], "id": "job_other"})
        self.after["sources"][0]["retention_job_pins"] = 2
        self.assertEqual(recovery.verify_upgrade_snapshot(self.before, self.after), 2)
        for index in (0, 1):
            with self.subTest(index=index, operation="missing"):
                self.rejects(lambda s: s["jobs"][index].pop("audio_source_id"))
            for value in ("src_old", "src_other", "", False, 0, []):
                with self.subTest(index=index, value=value):
                    self.rejects(lambda s: s["jobs"][index].update(audio_source_id=value))
        self.rejects(lambda s: s["jobs"][1].update(lease_token="changed"))

    def test_fixture_must_represent_pre_009_jobs(self):
        before = copy.deepcopy(self.before)
        before["jobs"][0]["audio_source_id"] = None
        with self.assertRaises(RuntimeError):
            recovery.verify_upgrade_snapshot(before, self.after)


class ExternalCurrentChecks(unittest.TestCase):
    """Real temporary Git plus a synthetic native version executable; no Go/PG."""

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="cutmy-recovery-controls-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.protocol = recovery.release_protocol()
        self.git("init", "-q")
        self.git("config", "user.name", "Recovery controls")
        self.git("config", "user.email", "recovery-controls@example.invalid")
        files = {"go.mod": "module example.invalid/cutmy\n\ngo 1.26.9\ntoolchain go1.27.2\n",
                 "go.sum": "", "LICENSE": "fixture license\n", "THIRD_PARTY_NOTICES.md": "fixture notices\n",
                 "CHANGELOG.md": "# Changelog\n", "docs/release-artifact.txt": "fixture archive docs\n",
                 "scripts/release.sh": "# fixture\n", "scripts/package-release.py": "# fixture\n",
                 "scripts/release_manifest.py": "# fixture\n", ".github/workflows/release.yml": "# fixture\n"}
        for name, data in files.items():
            path = self.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(data)
        self.git("add", ".")
        self.git("commit", "-qm", "fixture")
        commit = self.git("rev-parse", "HEAD")
        self.source = self.protocol.immutable_source(self.repo, "candidate", commit)
        self.source_path = self.root / "SOURCE.json"
        self.source_path.write_bytes(self.protocol.canonical(self.source))
        self.version = {"version": self.source["version"], "commit": commit,
                        "build_time": self.source["build_time"], "go_version": "go1.27.2",
                        "os": "linux", "arch": "amd64"}
        self.binary = self.root / "supplied-cutmy"
        self.write_binary(self.version)
        self.args = SimpleNamespace(current_binary=self.binary, expected_current_binary_sha256=recovery.digest(self.binary),
                                    source=self.source_path, expected_source_sha256=recovery.digest(self.source_path),
                                    archive_receipt=None, go="forbidden-current-Go-build")
        self.patch_root = patch.object(recovery, "ROOT", self.repo)
        self.patch_protocol = patch.object(recovery, "release_protocol", return_value=self.protocol)
        self.patch_root.start()
        self.patch_protocol.start()
        self.addCleanup(self.patch_root.stop)
        self.addCleanup(self.patch_protocol.stop)

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.repo), *args], stderr=subprocess.PIPE).decode().strip()

    def write_binary(self, version, extra=""):
        # This executable only tests the consumer's identity boundary. Its bytes
        # are synthetic and do not prove a Go build or the archive media gate.
        self.binary.write_text(f"#!{sys.executable}\nimport json,sys\nassert sys.argv[1:]==['version']\n"
                               + extra + "\nprint(" + repr(json.dumps(version)) + ")\n")
        self.binary.chmod(0o700)

    def prepare(self, label="work", args=None):
        work = self.root / label
        work.mkdir()
        report = {}
        current = recovery.prepare_current_binary(args or self.args, work, report)
        return current, report

    def test_external_flags_are_all_or_none_and_default_is_unchanged(self):
        flags = [("--current-binary", "/fixture/cutmy"), ("--expected-current-binary-sha256", "a" * 64),
                 ("--source", "/fixture/SOURCE.json"), ("--expected-source-sha256", "b" * 64)]
        self.assertIsNone(recovery.parse_args([]).current_binary)
        for bits in range(1, 15):
            argv = [value for index, pair in enumerate(flags) if bits & (1 << index) for value in pair]
            with self.subTest(bits=bits), redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                recovery.parse_args(argv)
        self.assertEqual(recovery.parse_args([value for pair in flags for value in pair]).current_binary, Path("/fixture/cutmy"))
        with redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            recovery.parse_args(["--archive-receipt", "/fixture/download.json"])

    def test_real_capture_version_and_private_copy_survive_supplied_path_replacement(self):
        original = self.binary.read_bytes()
        current, report = self.prepare()
        self.assertNotEqual(current, self.binary)
        self.assertEqual(current.read_bytes(), original)
        self.binary.write_bytes(b"replaced after capture")
        self.assertEqual(json.loads(recovery.run([str(current), "version"])), self.version)
        recovery.verify_current_binary(self.args, current, report)
        proof = report["current_external_archive"]
        self.assertEqual(proof["captured_binary_sha256_before"], self.args.expected_current_binary_sha256)
        self.assertEqual(proof["captured_binary_sha256_after"], self.args.expected_current_binary_sha256)
        self.assertIs(proof["current_application_built"], False)

    def test_external_binary_path_and_hash_fail_before_execution(self):
        link = self.root / "linked-cutmy"
        link.symlink_to(self.binary)
        for index, (path, expected) in enumerate(((Path("relative-cutmy"), self.args.expected_current_binary_sha256),
                                                (self.root / "missing", self.args.expected_current_binary_sha256),
                                                (self.root, self.args.expected_current_binary_sha256),
                                                (link, self.args.expected_current_binary_sha256),
                                                (self.binary, "0" * 64))):
            args = copy.copy(self.args)
            args.current_binary, args.expected_current_binary_sha256 = path, expected
            with self.subTest(path=str(path)), patch.object(recovery, "run", side_effect=AssertionError("executed before identity check")), \
                    self.assertRaises(RuntimeError):
                self.prepare("invalid-path-" + str(index), args)

    def test_wrong_actual_version_fields_and_duplicates_are_rejected(self):
        for index, key in enumerate((*self.version, "extra")):
            changed = {**self.version, key: "wrong"}
            self.write_binary(changed)
            self.args.expected_current_binary_sha256 = recovery.digest(self.binary)
            with self.subTest(field=key), self.assertRaisesRegex(RuntimeError, "version/source/native-target"):
                self.prepare("version-" + str(index))
        self.binary.write_text(f"#!{sys.executable}\nprint('{{\"commit\":\"first\",\"commit\":\"second\"}}')\n")
        self.binary.chmod(0o700)
        self.args.expected_current_binary_sha256 = recovery.digest(self.binary)
        with self.assertRaisesRegex(RuntimeError, "Duplicate"):
            self.prepare("duplicate-version")

    def test_non_native_runner_is_rejected_before_binary_execution(self):
        for index, (system, machine) in enumerate((("darwin", "x86_64"), ("linux", "aarch64"))):
            with self.subTest(system=system, machine=machine), patch.object(recovery.sys, "platform", system), \
                    patch.object(recovery.platform, "machine", return_value=machine), \
                    patch.object(recovery, "run", side_effect=AssertionError("executed on non-native runner")), \
                    self.assertRaisesRegex(RuntimeError, "native Linux amd64"):
                self.prepare("non-native-" + str(index))

    def test_source_digest_checkout_changes_and_wrong_commit_fail_before_execution(self):
        args = copy.copy(self.args)
        args.expected_source_sha256 = "0" * 64
        with patch.object(recovery, "run", side_effect=AssertionError("binary executed for foreign source")), self.assertRaises(ValueError):
            self.prepare("wrong-source-hash", args)
        (self.repo / "LICENSE").write_text("changed tracked source\n")
        with patch.object(recovery, "run", side_effect=AssertionError("binary executed from dirty checkout")), self.assertRaises(ValueError):
            self.prepare("dirty-source")
        self.git("add", ".")
        self.git("commit", "-qm", "different source")
        with patch.object(recovery, "run", side_effect=AssertionError("binary executed for wrong checkout")), self.assertRaises(ValueError):
            self.prepare("wrong-source-commit")

    def test_external_build_branch_never_builds_current_or_falls_back(self):
        runner = object.__new__(recovery.Acceptance)
        runner.args, runner.report, runner.work = self.args, {}, self.root / "build-current"
        runner.work.mkdir()
        real_run = recovery.run
        calls = []

        def external_only(command, **kwargs):
            calls.append(command)
            self.assertEqual(command[1:], ["version"])
            self.assertEqual(Path(command[0]), runner.work / "cutmy-current")
            return real_run(command, **kwargs)

        with patch.object(recovery, "run", side_effect=external_only):
            self.assertEqual(runner.build_current(), runner.work / "cutmy-current")
        self.assertEqual(len(calls), 1)
        # Invalid bytes remain terminal rather than invoking the source builder.
        runner.work = self.root / "invalid-build-current"
        runner.work.mkdir()
        self.args.expected_current_binary_sha256 = "0" * 64
        with patch.object(recovery, "run", side_effect=AssertionError("current build fallback")), self.assertRaises(RuntimeError):
            runner.build_current()

    def test_default_current_builder_preserves_original_protocol(self):
        runner = object.__new__(recovery.Acceptance)
        runner.args = SimpleNamespace(current_binary=None, go="fixture-go")
        runner.work = self.root / "source-current"
        with patch.object(recovery, "run", side_effect=[(self.source["commit"] + "\n").encode(), b""]) as command:
            self.assertEqual(runner.build_current(), runner.work / "cutmy-current")
        built = command.call_args_list[1].args[0]
        self.assertEqual(built[:2], ["fixture-go", "build"])
        self.assertIn("-X main.version=recovery-acceptance -X main.commit=" + self.source["commit"], built)
        self.assertEqual(command.call_args_list[1].kwargs["cwd"], self.repo)

    def test_capture_change_during_version_or_after_recovery_is_rejected(self):
        self.write_binary(self.version, "from pathlib import Path\nPath(sys.argv[0]).write_bytes(b'changed during version')")
        self.args.expected_current_binary_sha256 = recovery.digest(self.binary)
        with self.assertRaisesRegex(RuntimeError, "changed while reporting"):
            self.prepare("during-version")
        self.write_binary(self.version)
        self.args.expected_current_binary_sha256 = recovery.digest(self.binary)
        current, report = self.prepare("after-version")
        current.write_bytes(b"changed after scenarios")
        with self.assertRaisesRegex(RuntimeError, "changed during recovery"):
            recovery.verify_current_binary(self.args, current, report)
        self.assertEqual(report["current_external_archive"]["captured_binary_sha256_after"], recovery.digest(current))
        current.write_bytes(self.binary.read_bytes())
        (self.repo / "LICENSE").write_text("checkout changed during recovery\n")
        with self.assertRaises(ValueError):
            recovery.verify_current_binary(self.args, current, report)

    def test_archive_receipt_is_bound_before_execution_to_source_and_current_run(self):
        receipt = {"schema_version": 1, "repository": "owner/repo", "run_id": 123, "artifact_id": 456,
                   "artifact_name": "release-bundle-123-1", "artifact_sha256": "c" * 64, "artifact_bytes": 100,
                   "source_commit": self.source["commit"], "source_sha256": self.args.expected_source_sha256}
        self.args.archive_receipt = self.root / "download.json"
        self.args.archive_receipt.write_text(json.dumps(receipt))
        with patch.dict(recovery.os.environ, {"GITHUB_REPOSITORY": "owner/repo", "GITHUB_RUN_ID": "123"}):
            _, report = self.prepare("valid-receipt")
            self.assertEqual(report["current_external_archive"]["archive_receipt"]["sha256"], recovery.digest(self.args.archive_receipt))
            for index, (key, value) in enumerate((("source_commit", "0" * 40), ("source_sha256", "0" * 64),
                                                ("artifact_sha256", "invalid"), ("artifact_bytes", 0),
                                                ("run_id", 124), ("repository", "other/repo"), ("extra", True))):
                self.args.archive_receipt.write_text(json.dumps({**receipt, key: value}))
                with self.subTest(field=key), patch.object(recovery, "run", side_effect=AssertionError("executed before receipt binding")), \
                        self.assertRaises(RuntimeError):
                    self.prepare("bad-receipt-" + str(index))


if __name__ == "__main__":
    unittest.main()
