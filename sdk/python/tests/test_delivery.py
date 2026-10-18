import copy
import tempfile
from pathlib import Path
import unittest
from unittest.mock import patch

from cutmyvideo import APIError, CutmyClient, export_portable_project, import_portable_project

IMAGE = {"id": "img_123", "parent_artifact_id": "art_123", "filename": "poster.png", "size_bytes": 3,
         "download_url": "/api/v1/image-artifacts/img_123/download", "width": 160, "height": 96,
         "timestamp_ms": 2000, "expires_at": None, "format": "png", "future": True}
REQUEST = {"source_id": "owned", "ranges": [{"start_ms": 0, "end_ms": 3000}], "format": "mp4", "quality": "best", "cut_mode": "accurate"}


class DeliverySDKTests(unittest.TestCase):
    def test_invalid_delivery_values_and_conflicting_resets_never_reach_transport(self):
        with CutmyClient("http://127.0.0.1") as client, patch.object(CutmyClient, "_json") as transport:
            for options in [{"speed": {"numerator": 1.5, "denominator": 1}}, {"speed": {"numerator": True, "denominator": 1}}, {"speed": {"numerator": 1, "denominator": 3}}, {"speed": {"numerator": 5, "denominator": 4, "secret": 1}}, {"target_size_bytes": None}, {"target_size_bytes": float("nan")}, {"target_size_bytes": 9007199254740992}, {"target_size_bytes": -1}, {"speed": {"numerator": 5, "denominator": 4}, "clear_speed": True}, {"target_size_bytes": 500, "clear_target_size": True}]:
                with self.subTest(options=options), self.assertRaises((ValueError, TypeError)):
                    client.create_job({**REQUEST, **options}, idempotency_key="one-attempt")
            transport.assert_not_called()

    def test_portable_delivery_values_and_inheritance_resets_roundtrip(self):
        source = {"id": "owned", "duration_ms": 4000}
        body = {"source_id": source["id"], "plan": {"version": 1, "options": {"format": "mp4", "speed": {"numerator": 5, "denominator": 4}, "target_size_bytes": 262144}, "clips": [{"id": "inherit", "ranges": [{"start_ms": 0, "end_ms": 1000}]}, {"id": "clear", "ranges": [{"start_ms": 0, "end_ms": 1000}], "options": {"format": "wav", "clear_speed": True, "clear_target_size": True}}]}}
        self.assertEqual(import_portable_project(export_portable_project(body, source), source), body)
        bad = copy.deepcopy(body); bad["plan"]["options"]["speed"]["denominator"] = 0
        with self.assertRaises(ValueError):
            export_portable_project(bad, source)

    def test_normalized_delivery_responses_reject_malformed_new_options(self):
        with CutmyClient("http://127.0.0.1") as client:
            for options in [{"speed": {"numerator": 5, "denominator": 0}}, {"speed": True}, {"target_size_bytes": None}, {"target_size_bytes": 9007199254740992}]:
                reply = {"version": 1, "valid": True, "checks": [], "warnings": [], "request": {**REQUEST, **options}}
                with self.subTest(options=options), patch.object(client, "_json", return_value=reply), self.assertRaises(APIError):
                    client.preflight(REQUEST)
            normalized = {**REQUEST, "speed": {"numerator": 5, "denominator": 4}, "target_size_bytes": 262144}
            with patch.object(client, "_json", return_value={"version": 1, "valid": True, "checks": [], "warnings": [], "request": normalized}):
                self.assertEqual(client.preflight(REQUEST)["request"], normalized)

    def test_poster_parent_clock_and_owned_image_routes_are_verified(self):
        with CutmyClient("http://127.0.0.1") as client:
            with patch.object(client, "_json", return_value=IMAGE) as transport:
                self.assertEqual(client.create_poster("art_123", {"timestamp_ms": 2000, "format": "png"}), IMAGE)
                self.assertEqual(transport.call_args.args[:3], ("POST", "/api/v1/artifacts/art_123/poster", {"timestamp_ms": 2000, "format": "png"}))
                self.assertEqual(client.get_image_artifact("img_123"), IMAGE)
            for mutation in [{"parent_artifact_id": "another"}, {"timestamp_ms": True}, {"timestamp_ms": 1999}, {"width": 0}, {"size_bytes": 16777217}, {"download_url": "https://foreign.invalid/private"}, {"filename": "../poster.png"}, {"format": "svg"}]:
                with self.subTest(mutation=mutation), patch.object(client, "_json", return_value={**IMAGE, **mutation}), self.assertRaises(APIError):
                    client.create_poster("art_123", {"timestamp_ms": 2000, "format": "png"})
            with patch.object(client, "_json") as transport:
                for options in [{}, {"timestamp_ms": -1}, {"timestamp_ms": True}, {"timestamp_ms": 0, "format": "svg"}]:
                    with self.assertRaises(ValueError):
                        client.create_poster("art_123", options)
                transport.assert_not_called()
                client.delete_image_artifact("img_123")
                self.assertEqual(transport.call_args.args, ("DELETE", "/api/v1/image-artifacts/img_123"))
            with tempfile.TemporaryDirectory() as directory, patch.object(client, "_exchange", return_value=Path(directory) / "poster.png") as transport:
                client.download_image({**IMAGE, "download_url": "https://foreign.invalid/private"}, Path(directory) / "poster.png")
                self.assertEqual(transport.call_args.args[:2], ("GET", "/api/v1/image-artifacts/img_123/download"))


if __name__ == "__main__":
    unittest.main()
