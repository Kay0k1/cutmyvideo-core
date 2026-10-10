"""Negative controls for the historical upgrade's record-preservation gate."""
import copy
import importlib.util
from pathlib import Path
import unittest


module = importlib.util.spec_from_file_location("recovery", Path(__file__).with_name("recovery-acceptance.py"))
recovery = importlib.util.module_from_spec(module)
module.loader.exec_module(recovery)


class UpgradeSnapshotChecks(unittest.TestCase):
    def setUp(self):
        self.before = {
            "sources": [{"id": "src_old", "owner": "owner_a", "path": "/fixture/source", "duration_ms": 8000}],
            "jobs": [{"id": "job_old", "owner": "owner_a", "status": "running", "attempts": 1,
                      "lease_token": "lease_old", "lease_until": "2026-10-10T22:15:00+00:00",
                      "items": [{"id": "item_old", "status": "succeeded", "artifact_id": "art_old"}]}],
            "artifacts": [{"id": "art_old", "job_id": "job_old", "path": "/fixture/artifact", "size_bytes": 32, "expires_at": None}],
            "storage_files": [
                {"path": "/fixture/source", "owner": "owner_a", "kind": "source", "resource_id": "src_old", "size_bytes": 100, "delete_pending": False},
                {"path": "/fixture/pending", "owner": "owner_a", "kind": "tombstone", "resource_id": "", "size_bytes": 8, "delete_pending": True},
            ],
            "storage_reservations": [{"id": "reservation_old", "owner": "owner_a", "size_bytes": 99, "token": "lease_old"}],
            "storage_counters": [{"id": 1, "stored_bytes": 108, "reserved_bytes": 99}],
            "queue_owners": [{"owner": "owner_a", "pending": 1}],
        }
        self.after = copy.deepcopy(self.before)
        for row in self.after["storage_files"]:
            row.update(delete_retry_at="-infinity", delete_failures=0)

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
        for table in ("storage_files", "jobs"):
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


if __name__ == "__main__":
    unittest.main()
