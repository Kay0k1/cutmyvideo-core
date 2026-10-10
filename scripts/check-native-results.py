#!/usr/bin/env python3
"""Require real, unskipped native tests, including the complete API/worker path."""
import json
import sys
from pathlib import Path

if len(sys.argv) != 4:
    raise SystemExit("expected media, process and service JSON test reports")

required = [
    {"TestEngineInspectAndExportActualMedia", "TestConcurrentPublicationHasExactlyOneCompleteWinner", "TestCLIInspectAndClipActualMedia"},
    {"TestProcessCancellationTerminatesDescendants", "TestProcessOutputStopsUnboundedProducer"},
    {"TestNativeServiceUploadExportAndDownload"},
]
for filename, names in zip(sys.argv[1:], required):
    events = [json.loads(line) for line in Path(filename).read_text(encoding="utf-8").splitlines() if line.strip()]
    failures = [event for event in events if event.get("Action") in {"fail", "skip"}]
    passed = {event.get("Test") for event in events if event.get("Action") == "pass"}
    if failures or not names <= passed:
        raise SystemExit(f"native verification incomplete: {filename}; missing={sorted(names - passed)}; failed/skipped={len(failures)}")
print("Native media, filesystem, CLI, process tree and API/worker checks passed without skips.")
