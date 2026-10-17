"""Shared edit documents exercise SDK structure, not media/budget validation."""
import copy
import json
import os
from pathlib import Path
import unittest

from cutmyvideo import APIError, export_portable_project, import_portable_project


class ProjectContractExamples(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = Path(os.environ["CUTMY_PROJECT_CONTRACTS"]) if "CUTMY_PROJECT_CONTRACTS" in os.environ else (
            Path(__file__).resolve().parents[3] / "api/testdata/project-contracts.json")
        cls.corpus = json.loads(path.read_text(encoding="utf-8"))
        if cls.corpus.get("schema_version") != 1:
            raise ValueError("Unsupported project contract examples")
        for group in ("collection_cases", "portable_cases"):
            cases = cls.corpus[group]
            if not cases or len({case["id"] for case in cases}) != len(cases):
                raise ValueError("Contract examples need distinct case IDs")

    def test_collection_examples_preserve_sdk_structure_without_claiming_media_validation(self):
        for case in self.corpus["collection_cases"]:
            with self.subTest(case=case["id"]):
                self.assertIs(type(case["sdk_valid"]), bool)
                original = copy.deepcopy(case)
                if case["sdk_valid"]:
                    document = export_portable_project(case["request"])
                    self.assertEqual(document["plan"], case["request"]["plan"])
                    for key in ("clip_ids", "rendition"):
                        if key in case["request"]:
                            self.assertEqual(document[key], case["request"][key])
                        else:
                            self.assertNotIn(key, document)
                else:
                    with self.assertRaises((ValueError, TypeError, APIError)):
                        export_portable_project(case["request"])
                self.assertEqual(case, original)

    def test_portable_examples_require_explicit_source_and_reject_unknown_edit_fields(self):
        for case in self.corpus["portable_cases"]:
            with self.subTest(case=case["id"]):
                original = copy.deepcopy(case)
                if "export_valid" in case:
                    self.assertIs(type(case["export_valid"]), bool)
                    if case["export_valid"]:
                        self.assertEqual(export_portable_project(case["request"], case["source"]), case["expected_document"])
                    else:
                        with self.assertRaises((ValueError, TypeError, APIError)):
                            export_portable_project(case["request"], case["source"])
                if "import_valid" in case:
                    self.assertIs(type(case["import_valid"]), bool)
                    if case["import_valid"]:
                        self.assertEqual(import_portable_project(case["document"], case["trusted_source"]), case["expected_request"])
                    else:
                        with self.assertRaises((ValueError, TypeError, APIError)):
                            import_portable_project(case["document"], case["trusted_source"])
                self.assertEqual(case, original)


if __name__ == "__main__":
    unittest.main()
