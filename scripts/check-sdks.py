#!/usr/bin/env python3
"""Build, install and exercise SDK distributions outside the source checkout."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tarfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
APP_PACKAGE = "github.com/Kay0k1/cutmyvideo-core/internal/app"
PROJECT_CONTRACTS = "api/testdata/project-contracts.json"
MINIMUM_FREE = 2 * 1024 * 1024 * 1024


def require_output_capacity(output):
    free = shutil.disk_usage(output).free
    if free < MINIMUM_FREE:
        raise RuntimeError(f"SDK checks require at least 2 GiB free on the output filesystem; "
                           f"only {free // (1024 * 1024)} MiB available at {output}")


def cleanup_disposable(output, owned_output):
    """Keep release evidence; remove only this successful run's scratch paths."""
    current = output.lstat()
    if (current.st_dev, current.st_ino) != (owned_output.st_dev, owned_output.st_ino):
        raise RuntimeError("SDK evidence directory changed; refusing scratch cleanup")
    directories = [output / name for name in ("python-consumer", "node-consumer", "python-source", "python-rebuilt")]
    binary = output / ("cutmy.exe" if os.name == "nt" else "cutmy")
    # Validate every direct child before removing any. rmtree unlinks nested
    # symlinks (and Windows junctions), without traversing their targets.
    for path, expected_type in [(output, stat.S_ISDIR), *[(path, stat.S_ISDIR) for path in directories],
                                (binary, stat.S_ISREG)]:
        entry = path.lstat()
        if (not expected_type(entry.st_mode) or
                getattr(entry, "st_file_attributes", 0) & getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0)):
            raise RuntimeError(f"Unsafe SDK scratch path; refusing cleanup: {path}")
    for path in directories:
        shutil.rmtree(path)
    binary.unlink()


def verify_go_report(path, required):
    passed, skipped, failed, complete = set(), set(), set(), False
    for raw in path.read_text(encoding="utf-8").splitlines():
        event = json.loads(raw)
        if event.get("Package") != APP_PACKAGE:
            continue
        name, action = event.get("Test"), event.get("Action")
        if name:
            if action == "pass":
                passed.add(name)
            elif action == "skip":
                skipped.add(name)
            elif action == "fail":
                failed.add(name)
        elif action == "pass":
            complete = True
        elif action == "fail":
            failed.add("package")
    if not complete or failed or required - passed or required & skipped:
        raise RuntimeError("Installed SDK media tests must complete without missing, skipped or failed checks")


def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def collection_inputs():
    paths = [ROOT / name for name in (
        "go.mod", "go.sum", PROJECT_CONTRACTS, "scripts/check-sdks.py", "scripts/sdk_checks_test.py",
        "scripts/sdk-smoke.py", "scripts/sdk-smoke.mjs",
        "scripts/sdk-media-acceptance.py", "scripts/sdk-media-acceptance.mjs",
        "scripts/sdk-delivery-acceptance.py", "scripts/sdk-delivery-acceptance.mjs",
        "scripts/sdk-collection-acceptance.py", "scripts/sdk-collection-acceptance.mjs",
        "scripts/sdk-agent-project-acceptance.py", "scripts/sdk-agent-project-acceptance.mjs",
        "scripts/sdk-collection-acceptance-verify.py", "scripts/sdk-collection-acceptance-verify-test.py",
        "internal/app/sdk_integration_test.go", "internal/app/sdk_collections_integration_test.go",
        "internal/app/sdk_agent_projects_integration_test.go", "internal/app/sdk_media_integration_test.go",
        "internal/app/sdk_delivery_integration_test.go",
        "sdk/python/README.md", "sdk/python/pyproject.toml", "sdk/python/src/cutmyvideo/py.typed",
        "sdk/typescript/README.md", "sdk/typescript/package.json", "sdk/typescript/package-lock.json",
        "sdk/typescript/tsconfig.json")]
    for folder, pattern in (("sdk/python/src/cutmyvideo", "*.py"), ("sdk/python/tests", "*.py"),
                            ("sdk/typescript/src", "*.ts"), ("sdk/typescript/test", "*.mjs")):
        paths.extend((ROOT / folder).rglob(pattern))
    return {path.relative_to(ROOT).as_posix(): sha256(path) for path in sorted(set(paths))}


def copy_project_contracts(output, expected_sha256):
    """Installed tests use frozen test input, never a runtime package dependency."""
    data = (ROOT / PROJECT_CONTRACTS).read_bytes()
    if hashlib.sha256(data).hexdigest() != expected_sha256:
        raise RuntimeError("Project contract examples changed before SDK verification")
    destination = output / "testdata" / "project-contracts.json"
    destination.parent.mkdir()
    with destination.open("xb") as stream:
        stream.write(data)
    return destination


def run(argv, *, cwd, env, log, timeout=300):
    with log.open("wb") as output:
        process = subprocess.run(argv, cwd=cwd, env=env, stdout=output,
                                 stderr=subprocess.STDOUT, timeout=timeout, check=False)
    if process.returncode:
        raise RuntimeError(f"SDK check failed ({process.returncode}); see {log}")


def check_wheel(path):
    with zipfile.ZipFile(path) as archive:
        names = archive.namelist()
        required_modules = {"cutmyvideo/__init__.py", "cutmyvideo/py.typed", "cutmyvideo/collections.py",
                            "cutmyvideo/_collection_protocol.py", "cutmyvideo/_collection_state.py", "cutmyvideo/media.py"}
        if not required_modules.issubset(names):
            raise RuntimeError("Python wheel is missing its public module or typing marker")
        if not any(name.endswith("/LICENSE") for name in names):
            raise RuntimeError("Python wheel is missing its license")
        if any(name.startswith(("tests/", "test/")) or ".." in Path(name).parts for name in names):
            raise RuntimeError("Python wheel contains an unsafe or unintended payload")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True, help="new evidence directory outside checkout")
    parser.add_argument("--http", action="store_true", help="also require isolated PostgreSQL HTTP/worker acceptance")
    args = parser.parse_args()
    output = args.output.resolve()
    if output.is_relative_to(ROOT):
        parser.error("SDK consumer evidence must be outside the checkout")
    output.mkdir(parents=True, exist_ok=False)
    owned_output = output.lstat()
    require_output_capacity(output)
    env = dict(os.environ)
    source_inputs = collection_inputs()
    project_contracts = copy_project_contracts(output, source_inputs[PROJECT_CONTRACTS])
    env["CUTMY_PROJECT_CONTRACTS"] = str(project_contracts)
    python, node, npm = sys.executable, shutil.which("node"), shutil.which("npm")
    if not node or not npm:
        parser.error("Node.js and npm are required")
    run([python, str(ROOT / "scripts/sdk-collection-acceptance-verify-test.py")],
        cwd=output, env=env, log=output / "collection-content-controls.log", timeout=180)
    # npm is a command script on Windows. Invoke its actual JS entry with Node
    # so arguments never pass through cmd.exe or shell quoting.
    if os.name == "nt":
        npm_entry = Path(npm).parent / "node_modules" / "npm" / "bin" / "npm-cli.js"
        if not npm_entry.is_file():
            parser.error("cannot locate npm's installed JavaScript entry")
        npm_command = [node, str(npm_entry)]
    else:
        npm_command = [npm]
    wheel_dir = output / "python-dist"
    run([python, "-m", "build", "--no-isolation", "--outdir", str(wheel_dir), str(ROOT / "sdk/python")],
        cwd=ROOT, env=env, log=output / "python-build.log")
    wheels, sdists = list(wheel_dir.glob("*.whl")), list(wheel_dir.glob("*.tar.gz"))
    if len(wheels) != 1 or len(sdists) != 1:
        raise RuntimeError("Expected exactly one Python wheel and source distribution")
    check_wheel(wheels[0])
    installed = output / "python-consumer"
    run([python, "-m", "venv", str(installed)], cwd=output, env=env, log=output / "python-consumer-env.log")
    consumer_python = str(installed / ("Scripts/python.exe" if os.name == "nt" else "bin/python"))
    env.pop("PYTHONPATH", None)
    run([consumer_python, "-m", "pip", "install", "--disable-pip-version-check", "--no-deps", "--no-index",
         str(wheels[0])], cwd=output, env=env, log=output / "python-install.log")
    run([consumer_python, "-m", "unittest", "discover", "-s", str(ROOT / "sdk/python/tests"), "-v"],
        cwd=output, env=env, log=output / "python-tests.log")
    sdist_root = output / "python-source"
    sdist_root.mkdir()
    with tarfile.open(sdists[0]) as archive:
        for member in archive.getmembers():
            if member.issym() or member.islnk() or not (sdist_root / member.name).resolve().is_relative_to(sdist_root):
                raise RuntimeError("Unsafe Python source distribution")
        archive.extractall(sdist_root)
    source_dirs = [p for p in sdist_root.iterdir() if p.is_dir()]
    if len(source_dirs) != 1:
        raise RuntimeError("Source distribution must contain one package root")
    rebuilt_dir = output / "python-rebuilt"
    run([python, "-m", "build", "--no-isolation", "--wheel", "--outdir", str(rebuilt_dir), str(source_dirs[0])],
        cwd=output, env=env, log=output / "python-sdist-build.log")
    rebuilt = list(rebuilt_dir.glob("*.whl"))
    if len(rebuilt) != 1:
        raise RuntimeError("Source distribution did not build a wheel")
    check_wheel(rebuilt[0])
    with zipfile.ZipFile(wheels[0]) as a, zipfile.ZipFile(rebuilt[0]) as b:
        source_payload = {n: a.read(n) for n in a.namelist() if n.startswith("cutmyvideo/")}
        rebuilt_payload = {n: b.read(n) for n in b.namelist() if n.startswith("cutmyvideo/")}
        if source_payload != rebuilt_payload:
            raise RuntimeError("Source distribution changes the installed Python module payload")
    ts_root = ROOT / "sdk/typescript"
    run(npm_command + ["ci", "--ignore-scripts", "--no-audit", "--no-fund"], cwd=ts_root, env=env, log=output / "typescript-install.log")
    run(npm_command + ["test"], cwd=ts_root, env=env, log=output / "typescript-tests.log")
    run(npm_command + ["pack", "--pack-destination", str(output)], cwd=ts_root, env=env, log=output / "typescript-pack.log")
    tarballs = list(output.glob("*.tgz"))
    if len(tarballs) != 1:
        raise RuntimeError("Expected exactly one npm package")
    consumer = output / "node-consumer"
    consumer.mkdir()
    (consumer / "package.json").write_text('{"private":true,"type":"module"}\n', encoding="utf-8")
    run(npm_command + ["install", "--ignore-scripts", "--no-audit", "--no-fund", str(tarballs[0])],
        cwd=consumer, env=env, log=output / "typescript-consumer-install.log")
    package = consumer / "node_modules/@cutmyvideo/sdk"
    metadata = json.loads((package / "package.json").read_text(encoding="utf-8"))
    if metadata.get("dependencies"):
        raise RuntimeError("SDK unexpectedly introduced runtime dependencies")
    env["CUTMY_SDK_JS_ENTRY"] = str(package / "dist/index.js")
    env["CUTMY_SDK_LOCAL_JS_ENTRY"] = str(package / "dist/local.js")
    env["CUTMY_SDK_COLLECTION_JS_ENTRY"] = str(package / "dist/collections.js")
    env["CUTMY_SDK_SOURCE_JS_ENTRY"] = str(package / "dist/sources.js")
    run([node, "--test", "--test-timeout=120000", *map(str, sorted((ts_root / "test").glob("*.test.mjs")))],
        cwd=consumer, env=env, log=output / "typescript-installed-tests.log")
    (consumer / "types.ts").write_text('''import { CutmyClient, type CollectionRequest } from "@cutmyvideo/sdk";
import { LocalCutmy } from "@cutmyvideo/sdk/local";
import { runCollection, resumeCollection } from "@cutmyvideo/sdk/collections";
import { prepareSource, resumeSource } from "@cutmyvideo/sdk/sources";
const client = new CutmyClient({baseUrl: "http://127.0.0.1:8080"});
const local = new LocalCutmy({binary: "cutmy"});
void client.preflight({source_id: "src", ranges: [{start_ms: 0, end_ms: 1000}], format: "mp4", quality: "best", cut_mode: "accurate"});
const request: CollectionRequest = {source_id: "src", plan: {version: 1, options: {format: "mp4", quality: "best", cut_mode: "accurate"}, clips: [{id: "answer", ranges: [{start_ms: 0, end_ms: 1000}]}]}};
void client.analyzeSilence("src", {threshold_db: -40, min_silence_ms: 500});
void client.analyzeVideo("src", {scene_threshold: .3});
void client.analyzeLoudness("src");
void client.importCutList({format: "csv", text: "0,1,Scene"});
void client.exportCutList({format: "csv", cut_list: {version: 1, entries: [{start_ms: 0, end_ms: null, label: "Marker"}]}});
void local.silencePlan("input.mp4", {threshold_db: -40});
void local.planCutList({format: "csv", text: "0,,Scene"}, {media: "input.mp4", durationMs: 1000});
void client.preflight({source_id: "src", ranges: [{start_ms: 0, end_ms: 1000}], format: "mp4", quality: "best", cut_mode: "accurate", audio_normalize: {integrated_lufs: -16, true_peak_dbtp: -1, loudness_range_lu: 11}, canvas: {width: 1080, height: 1920, fit: "contain", background: "#000000"}, audio_replace: {source_id: "owned-upload", offset_ms: 1000}});
void client.preflight({source_id: "src", ranges: [{start_ms: 0, end_ms: 1000}], format: "wav", quality: "best", cut_mode: "accurate"});
void client.preflight({source_id: "src", ranges: [{start_ms: 0, end_ms: 1000}], format: "mp4", quality: "best", cut_mode: "accurate", speed: {numerator: 5, denominator: 4}, target_size_bytes: 262144});
void client.retimeSubtitles({version: 2, format: "srt", text: "", outputs: [{clip_id: "speed", segments: []}]});
void client.createPoster("artifact", {timestamp_ms: 1250, format: "png"});
void client.getImageArtifact("image"); void client.downloadImageArtifact("image"); void client.deleteImageArtifact("image");
void local.generatePoster("finished.mp4", "poster.jpg", {timestamp_ms: 1250});
void client.validateCollection({...request, plan: {...request.plan, options: {speed: {numerator: 5, denominator: 4}, target_size_bytes: 262144}, clips: [{id: "reset", ranges: [{start_ms: 0, end_ms: 1000}], options: {clear_speed: true, clear_target_size: true}}]}});
void client.validateCollection(request);
void client.submitCollection(request, {idempotencyKey: "saved-key"});
void runCollection(client, request, "/private/project", {timeoutMs: 1800000, onProgress: report => { console.log(report.outputs.map(output => output.clip_id)); }});
void resumeCollection(client, "/private/project", {request});
void prepareSource(client, {kind: "upload", path: "/media/interview.mp4"}, "/private/source");
void resumeSource(client, "/private/source");
void local.validateCollection({...request.plan, input: "/media/interview.mp4"}, "/media/clips");
void local.executeCollection({...request.plan, input: "/media/interview.mp4"}, "/media/clips", {clipIds: ["answer"], rendition: "draft"});
''', encoding="utf-8")
    run([node, str(ts_root / "node_modules/typescript/bin/tsc"), "--noEmit", "--strict", "--module", "NodeNext",
         "--moduleResolution", "NodeNext", "--target", "ES2022", "--lib", "ES2022,DOM",
         "--typeRoots", str(ts_root / "node_modules/@types"), "types.ts"],
        cwd=consumer, env=env, log=output / "typescript-consumer-types.log")
    binary = output / ("cutmy.exe" if os.name == "nt" else "cutmy")
    run(["go", "build", "-trimpath", "-o", str(binary), "./cmd/cutmy"], cwd=ROOT, env=env, log=output / "core-build.log")
    env.update(CUTMY_REQUIRE_SDK="1", CUTMY_SDK_PYTHON=consumer_python, CUTMY_SDK_NODE=node, CUTMY_SDK_BINARY=str(binary))
    top_level = {"TestInstalledSDKLocalWorkflow", "TestInstalledSDKCollectionLocalWorkflow", "TestInstalledSDKMediaLocalWorkflow", "TestInstalledSDKDeliveryLocalWorkflow"}
    if args.http:
        if not env.get("TEST_DATABASE_URL"):
            parser.error("--http requires an isolated TEST_DATABASE_URL")
        top_level.add("TestInstalledSDKHTTPWorkflow")
        top_level.add("TestInstalledSDKCollectionHTTPWorkflow")
        top_level.add("TestInstalledSDKAgentProjectWorkflow")
        top_level.add("TestInstalledSDKMediaHTTPWorkflow")
        top_level.add("TestInstalledSDKDeliveryHTTPWorkflow")
    required = top_level | {f"{name}/{language}" for name in top_level for language in ("python", "typescript")}
    selector = "^(" + "|".join(sorted(top_level)) + ")$"
    run(["go", "test", "-json", "-race", "-count=1", "-timeout=15m" if args.http else "-timeout=3m", "./internal/app", "-run", selector],
        cwd=ROOT, env=env, log=output / "installed-sdk-tests.jsonl", timeout=960 if args.http else 240)
    verify_go_report(output / "installed-sdk-tests.jsonl", required)
    if collection_inputs() != source_inputs or sha256(project_contracts) != source_inputs[PROJECT_CONTRACTS]:
        raise RuntimeError("SDK or acceptance inputs changed during verification; rerun from frozen source")
    receipt = {"schema": 1, "required_tests": sorted(required), "python_wheel_sha256": sha256(wheels[0]),
               "python_sdist_sha256": sha256(sdists[0]), "npm_tarball_sha256": sha256(tarballs[0]),
               "cli_sha256": sha256(binary), "installed_consumers": True, "source_inputs_sha256": source_inputs}
    (output / "SDK-CHECKS.json").write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
    cleanup_disposable(output, owned_output)
    print("Built, installed and verified both SDKs against real media and the core.")


if __name__ == "__main__":
    main()
