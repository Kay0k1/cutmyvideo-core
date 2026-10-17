"""Negative controls: contract validation must reject real client breakages."""
import copy
from contextlib import contextmanager
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from jsonschema import ValidationError

module = importlib.util.spec_from_file_location("contract", Path(__file__).with_name("check-contract.py"))
contract = importlib.util.module_from_spec(module)
module.loader.exec_module(contract)

class ContractChecks(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.document = contract.load(contract.ROOT / "api/openapi.yaml")
        cls.baselines = dict(contract.frozen_baselines())
        cls.baseline = cls.baselines["compatibility-v1.4.1.json"]

    @contextmanager
    def checkout(self):
        # An isolated CLI checkout makes overwrite/bypass controls safe even if
        # the protection being tested regresses.
        with tempfile.TemporaryDirectory(prefix="cutmy-contract-") as directory:
            root = Path(directory)
            paths = ["scripts/check-contract.py", "api/openapi.yaml", "api/schema/openapi-3.1.json"]
            paths += ["api/" + name for name in contract.FROZEN_BASELINES]
            for path in paths:
                target = root / path
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(contract.ROOT / path, target)
            yield root

    def cli(self, root, *arguments):
        return subprocess.run([sys.executable, str(root / "scripts/check-contract.py"), *map(str, arguments)],
                              cwd=root, text=True, capture_output=True, timeout=30)

    def incompatible(self, mutate):
        changed = copy.deepcopy(self.baseline)
        mutate(changed)
        self.assertTrue(contract.compatibility(self.baseline, changed))

    def test_removed_route(self):
        self.incompatible(lambda s: s.pop("GET /api/v1/sources"))

    def test_v141_collection_and_project_operations_are_frozen(self):
        operations = [name for name in self.baseline if "/collections" in name or "/projects" in name]
        self.assertEqual(len(operations), 23)
        current = contract.snapshot(self.document)
        for name in operations:
            with self.subTest(operation=name):
                self.assertIn("default", self.baseline[name]["responses"])
                changed = copy.deepcopy(current)
                del changed[name]
                self.assertIn(name + ": operation removed", contract.compatibility(self.baseline, changed))
        self.assertEqual(len(self.baseline), 46)
        self.assertFalse(contract.check_compatibility(self.document))

    def test_cli_rejects_collection_and_project_breakages(self):
        def remove_collection(document):
            del document["paths"]["/api/v1/collections"]["post"]
        def remove_project(document):
            del document["paths"]["/api/v1/projects/{project}"]["put"]
        def require_collection_option(document):
            document["components"]["schemas"]["CollectionOptions"]["required"] = ["no_audio"]
        def narrow_collection_enum(document):
            document["components"]["schemas"]["CollectionOptions"]["properties"]["format"]["enum"].remove("mp3")
        def remove_nullable_audio(document):
            document["components"]["schemas"]["CollectionOptions"]["properties"]["audio_track"]["type"] = "integer"
        def remove_nullable_crop(document):
            document["components"]["schemas"]["CollectionOptions"]["properties"]["crop"]["oneOf"].pop()
        def narrow_clip_limit(document):
            document["components"]["schemas"]["CollectionPlan"]["properties"]["clips"]["maxItems"] = 127
        def remove_project_revision(document):
            del document["components"]["schemas"]["AgentProject"]["properties"]["revision"]
        def remove_default_response(document):
            del document["paths"]["/api/v1/collections"]["post"]["responses"]["default"]
        def remove_source_title(document):
            del document["components"]["schemas"]["Source"]["properties"]["title"]
        mutations = [remove_collection, remove_project, require_collection_option, narrow_collection_enum,
                     remove_nullable_audio, remove_nullable_crop, narrow_clip_limit, remove_project_revision,
                     remove_default_response, remove_source_title]
        with self.checkout() as root:
            spec = root / "api/openapi.yaml"
            for mutate in mutations:
                with self.subTest(mutation=mutate.__name__):
                    changed = copy.deepcopy(self.document)
                    mutate(changed)
                    spec.write_text(contract.yaml.safe_dump(changed, sort_keys=False), encoding="utf-8")
                    result = self.cli(root)
                    self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                    self.assertIn("compatibility-v1.4.1.json:", result.stderr)
                    self.assertIn("incompatible public contract changes", result.stderr)

    def test_cli_additional_baseline_cannot_replace_frozen_checks(self):
        with self.checkout() as root:
            changed = copy.deepcopy(self.document)
            del changed["paths"]["/api/v1/collections"]["post"]
            (root / "api/openapi.yaml").write_text(contract.yaml.safe_dump(changed, sort_keys=False), encoding="utf-8")
            exported = root / "review.json"
            result = self.cli(root, "--write-baseline", exported)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(exported.read_text(encoding="utf-8")), contract.snapshot(changed))
            result = self.cli(root, "--baseline", exported)
            self.assertEqual(result.returncode, 1)
            self.assertIn("compatibility-v1.4.1.json: POST /api/v1/collections: operation removed", result.stderr)

    def test_cli_refuses_regenerated_frozen_baselines(self):
        for name in contract.FROZEN_BASELINES:
            with self.subTest(baseline=name), self.checkout() as root:
                target = root / "api" / name
                if name == "compatibility-v1.4.1.json":
                    regenerated = json.loads(target.read_text(encoding="utf-8"))
                    regenerated["document"] = self.document
                else:
                    regenerated = contract.snapshot(self.document)
                target.write_text(json.dumps(regenerated), encoding="utf-8")
                result = self.cli(root)
                self.assertEqual(result.returncode, 1)
                self.assertIn(name + ": immutable baseline changed", result.stderr)

    def test_cli_export_cannot_overwrite_frozen_paths_or_aliases(self):
        with self.checkout() as root:
            for name in contract.FROZEN_BASELINES:
                target = root / "api" / name
                original = target.read_bytes()
                alias = root / (name + ".alias")
                alias.symlink_to(target)
                for output in (target, target.parent / "." / name, alias):
                    with self.subTest(baseline=name, output=output):
                        result = self.cli(root, "--write-baseline", output)
                        self.assertEqual(result.returncode, 1)
                        self.assertIn("cannot overwrite an immutable compatibility baseline", result.stderr)
                        self.assertEqual(target.read_bytes(), original)

    def test_annotations_and_compatible_additions_do_not_change_frozen_contract(self):
        changed = copy.deepcopy(self.document)
        changed["components"]["schemas"]["AgentProject"]["description"] = "Reviewed prose update"
        changed["components"]["schemas"]["AgentProject"]["properties"]["future_field"] = {"type": "string"}
        changed["paths"]["/api/v1/projects/{project}"]["get"]["parameters"] = [
            {"in": "query", "name": "future_filter", "required": False, "schema": {"type": "string"}}]
        self.assertFalse(contract.check_compatibility(changed))

    def test_annotation_keywords_do_not_hide_property_or_component_names(self):
        document = {"components": {"schemas": {"default": {
            "type": "object", "title": "Annotation", "description": "Annotation",
            "properties": {name: {"type": "string", "description": "Annotation", "default": "Example"}
                           for name in ("title", "default", "description", "example", "examples")},
        }}}}
        expanded = contract.expanded(document, {"$ref": "#/components/schemas/default"})
        self.assertEqual(expanded, {"type": "object", "properties": {
            name: {"type": "string"} for name in ("title", "default", "description", "example", "examples")}})
        for name in expanded["properties"]:
            with self.subTest(property=name):
                changed = copy.deepcopy(expanded)
                del changed["properties"][name]
                failures = []
                contract.compare_schema(expanded, changed, "response", "example", failures)
                self.assertIn("example/" + name + ": property removed", failures)
        source = self.baseline["GET /api/v1/sources/{id}"]["responses"]["200"]["application/json"]["schema"]
        self.assertIn("title", source["properties"])

    def test_collection_contract_examples_match_openapi_structure(self):
        corpus = json.loads((contract.ROOT / "api/testdata/project-contracts.json").read_text(encoding="utf-8"))
        self.assertEqual(corpus["schema_version"], 1)
        cases = corpus["collection_cases"]
        self.assertTrue(cases)
        self.assertEqual(len({case["id"] for case in cases}), len(cases))
        self.assertTrue(any(case["schema_valid"] for case in cases))
        self.assertTrue(any(not case["schema_valid"] for case in cases))
        # JSON Schema intentionally has a structural oracle: source timing,
        # selectors and cross-field normalization belong to the Go consumer.
        self.assertTrue(any(case["schema_valid"] and not case["valid"] for case in cases))
        schema = {"$ref": "#/components/schemas/CollectionRequest"}
        for case in cases:
            with self.subTest(case=case["id"]):
                self.assertIs(type(case["schema_valid"]), bool)
                if case["schema_valid"]:
                    contract.validate(self.document, schema, case["request"])
                else:
                    with self.assertRaises(ValidationError):
                        contract.validate(self.document, schema, case["request"])

    def test_utf8_contract_examples_do_not_depend_on_system_locale(self):
        env = dict(os.environ, LC_ALL="C", PYTHONUTF8="0", PYTHONCOERCECLOCALE="0")
        checked = subprocess.run(
            [sys.executable, "-B", "-m", "unittest",
             "contract_test.ContractChecks.test_collection_contract_examples_match_openapi_structure"],
            cwd=contract.ROOT / "scripts", env=env, capture_output=True,
            text=True, encoding="utf-8", errors="replace", timeout=30)
        self.assertEqual(checked.returncode, 0, checked.stdout + checked.stderr)

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
