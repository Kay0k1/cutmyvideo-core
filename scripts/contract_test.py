"""Negative controls: contract validation must reject real client breakages."""
import copy
import importlib.util
from pathlib import Path
import unittest
from jsonschema import ValidationError

module = importlib.util.spec_from_file_location("contract", Path(__file__).with_name("check-contract.py"))
contract = importlib.util.module_from_spec(module)
module.loader.exec_module(contract)

class ContractChecks(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.document = contract.load(contract.ROOT / "api/openapi.yaml")
        cls.baseline = contract.snapshot(cls.document)

    def incompatible(self, mutate):
        changed = copy.deepcopy(self.baseline)
        mutate(changed)
        self.assertTrue(contract.compatibility(self.baseline, changed))

    def test_removed_route(self):
        self.incompatible(lambda s: s.pop("GET /api/v1/sources"))

    def test_removed_response_header(self):
        self.incompatible(lambda s: s["GET /api/v1/sources/{id}/preview"]["response_headers"]["200"].pop("x-preview-start-ms"))

    def test_required_preview_header_is_checked(self):
        sample = {"name": "preview", "route": "/api/v1/sources/{id}/preview", "method": "GET", "status": 200,
                  "headers": {"Content-Type": "video/mp4"}, "body": "bytes"}
        with self.assertRaises(ValueError):
            contract.sample_check(self.document, sample)

    def test_reference_validation_siblings_require_review(self):
        with self.assertRaises(ValueError):
            contract.walk(self.document, {"$ref": "#/components/schemas/Range", "properties": {"label": {"const": "fixed"}}})

    def test_removed_response_field(self):
        self.incompatible(lambda s: s["GET /api/v1/sources/{id}"]["responses"]["200"]["application/json"]["schema"]["properties"].pop("duration_ms"))

    def test_wrong_response_type(self):
        self.incompatible(lambda s: s["GET /api/v1/sources/{id}"]["responses"]["200"]["application/json"]["schema"]["properties"]["duration_ms"].update(type="string"))

    def test_new_required_parameter(self):
        self.incompatible(lambda s: s["GET /api/v1/sources"]["parameters"].update({"query:new": {"required": True, "schema": {"type": "string"}}}))

    def test_request_enum_narrowing(self):
        self.incompatible(lambda s: s["POST /api/v1/jobs"]["request"]["application/json"]["schema"]["properties"]["format"].update(enum=["mp4"]))

    def test_new_request_bound(self):
        self.incompatible(lambda s: s["POST /api/v1/jobs"]["request"]["application/json"]["schema"]["properties"]["source_id"].update(maxLength=20))

    def test_new_unsupported_constraints_require_review(self):
        for rule in ({"pattern": "^restricted$"}, {"const": "src_fixed"}, {"format": "uuid"}, {"exclusiveMinimum": 3}, {"allOf": [{"maxLength": 10}]}, {"oneOf": [{"const": "src_fixed"}]}):
            with self.subTest(rule=rule):
                self.incompatible(lambda s: s["POST /api/v1/jobs"]["request"]["application/json"]["schema"]["properties"]["source_id"].update(rule))

    def test_weakened_response_bound(self):
        self.incompatible(lambda s: s["GET /api/v1/sources/{id}"]["responses"]["200"]["application/json"]["schema"]["properties"]["duration_ms"].pop("minimum"))

    def test_documented_label_bound_remains_effective(self):
        self.incompatible(lambda s: s["POST /api/v1/jobs"]["request"]["application/json"]["schema"]["properties"]["ranges"]["items"]["properties"]["label"].update({"x-maxBytes": 100}))

    def test_new_response_enum_value_requires_review(self):
        self.incompatible(lambda s: s["GET /api/v1/jobs/{id}"]["responses"]["200"]["application/json"]["schema"]["properties"]["status"]["enum"].append("new_terminal_state"))

    def test_additive_fields_and_unknown_diagnostics(self):
        changed = copy.deepcopy(self.baseline)
        source = changed["GET /api/v1/sources/{id}"]["responses"]["200"]["application/json"]["schema"]
        source["properties"]["new_field"] = {"type": "string"}
        changed["GET /api/v1/sources"]["parameters"]["query:new"] = {"required": False, "schema": {"type": "string"}}
        self.assertFalse(contract.compatibility(self.baseline, changed))
        contract.validate(self.document, {"$ref": "#/components/schemas/Error"}, {"error": {"code": "future_code", "message": "Safe fallback"}})

    def test_utf8_byte_limits(self):
        schema = {"type": "string", "x-maxBytes": 128}
        contract.validate(self.document, schema, "界" * 42)
        with self.assertRaises(ValidationError):
            contract.validate(self.document, schema, "界" * 43)

    def test_rfc3339_fractional_precision(self):
        schema = {"type": "string", "format": "date-time"}
        for size in range(1, 10):
            for zone in ("Z", "+03:30", "-04:00"):
                timestamp = "2026-10-10T21:25:04." + "521190123"[:size] + zone
                with self.subTest(timestamp=timestamp):
                    contract.validate(self.document, schema, timestamp)
        for timestamp in ("2026-10-10T21:25:04Z", "2026-10-10t21:25:04.123456789123z", "2024-02-29T00:00:00.1Z"):
            contract.validate(self.document, schema, timestamp)

    def test_rfc3339_rejects_invalid_calendar_clock_and_zone(self):
        schema = {"type": "string", "format": "date-time"}
        for timestamp in ("2026-02-29T21:25:04.52119Z", "2026-02-30T21:25:04.1Z", "2026-13-10T21:25:04.12Z",
                          "2026-10-10T25:25:04.1234Z", "2026-10-10T21:60:04.12345Z", "2026-10-10T21:25:04.123456+24:00",
                          "2026-10-10T21:25:04.1234567+00:60", "2026-10-10T21:25:04.12345678", "2026-10-10T21:25:04.Z"):
            with self.subTest(timestamp=timestamp), self.assertRaises(ValidationError):
                contract.validate(self.document, schema, timestamp)

    def test_invalid_json_schema(self):
        with self.assertRaises(Exception):
            contract.Draft202012Validator.check_schema({"type": "imaginary"})

    def test_unresolved_reference(self):
        with self.assertRaises(KeyError):
            contract.walk(self.document, {"$ref": "#/components/schemas/Absent"})

    def test_wrong_503_vocabulary(self):
        sample = {"name": "bad-503", "route": "/api/v1/sources", "method": "GET", "status": 503,
                  "headers": {"Content-Type": "application/json"}, "body": {"error": {"code": "internal", "message": "failure"}}}
        with self.assertRaises(ValidationError):
            contract.sample_check(self.document, sample)

    def test_storage_initialization_is_retryable_and_bounded(self):
        sample = {"name": "initializing", "route": "/api/v1/uploads", "method": "POST", "status": 503,
                  "headers": {"Content-Type": "application/json", "Retry-After": "2"},
                  "body": {"error": {"code": "storage_initializing", "message": "Try again shortly"}}}
        contract.sample_check(self.document, sample)
        for code in ("storage_limit", "internal"):
            changed = copy.deepcopy(sample)
            changed["body"]["error"]["code"] = code
            with self.subTest(code=code), self.assertRaises(ValidationError):
                contract.sample_check(self.document, changed)
        sample["headers"]["Retry-After"] = "soon"
        with self.assertRaises(ValidationError):
            contract.sample_check(self.document, sample)

    def test_range_errors_are_text(self):
        sample = {"name": "range", "route": "/api/v1/sources/{id}/media", "method": "GET", "status": 416,
                  "headers": {"Content-Type": "text/plain; charset=utf-8", "Content-Range": "bytes */32"}, "body": "invalid range\n"}
        contract.sample_check(self.document, sample)
        sample["headers"]["Content-Type"] = "application/json"
        with self.assertRaises(ValueError):
            contract.sample_check(self.document, sample)

if __name__ == "__main__":
    unittest.main()
