#!/usr/bin/env python3
"""Build/run paired, frozen local HTTP API + worker observations.

No production connections or public media are used. TEST_DATABASE_URL must name
the coordinator's disposable database. The actual owner cap/refusals are kept.
"""
import argparse
import datetime
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import uuid


ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('core_performance', ROOT / 'core-performance.py')
perf = importlib.util.module_from_spec(spec)
spec.loader.exec_module(perf)


def source_map(checkout):
    paths = [*checkout.rglob('*.go'), checkout / 'go.mod', checkout / 'go.sum',
             *(checkout / 'internal/app/migrations').glob('*.sql'),
             *(checkout / 'internal/app/migrations').glob('*.json')]
    return {str(path.relative_to(checkout)): perf.sha256(path) for path in sorted(paths)
            if path.is_file() and '.git' not in path.parts and 'vendor' not in path.parts}


def build(args):
    manifest = perf.load_manifest(args.manifest)
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=True)
    helpers = {kind: ROOT / 'testdata' / ('core-performance-' + kind + '_test.go')
               for kind in ('server', 'roles', 'copy', 'storage')}
    harness_before = perf.metadata(__file__)
    measurement_before = perf.metadata(ROOT / 'core-performance.py')
    launcher_before = perf.metadata(ROOT / 'testdata/core-performance-cgroup.py')
    products, cli_products, builds = {}, {}, []
    for side, checkout in (('baseline', args.baseline_core), ('candidate', args.candidate_core)):
        checkout = checkout.resolve(strict=True)
        if side == 'baseline':
            revision = perf.command(['git', '-C', checkout, 'rev-parse', 'HEAD']).decode().strip()
            if revision != manifest['baseline']['revision']:
                raise RuntimeError('Server baseline checkout differs from frozen baseline revision')
            perf.command(['git', '-C', checkout, 'diff', '--quiet', 'HEAD', '--', '*.go', 'go.mod', 'go.sum'])
        adapter = out / (side + '-server-config_test.go')
        if side == 'baseline':
            body = 'if os.Getenv("CUTMY_CORE_PERF_SERVER_PREPARATION") != "0" { panic("baseline preparation must be zero") }; return c'
            imports = 'import "os"'
        else:
            body = ('value, err := strconv.Atoi(os.Getenv("CUTMY_CORE_PERF_SERVER_PREPARATION")); '
                    'if err != nil || value < 0 || value > 8 { panic("invalid recorded preparation setting") }; '
                    'c.WorkerPreparationConcurrency = value; return c')
            imports = 'import ("os"; "strconv")'
        adapter.write_text('package app\n' + imports + '\nfunc corePerformanceServerConfig(c Config) Config { ' + body + ' }\n')
        replacements = {str(checkout / 'internal/app' / ('core_performance_' + kind + '_observation_test.go')): str(path)
                        for kind, path in helpers.items()}
        replacements[str(checkout / 'internal/app/core_performance_server_config_test.go')] = str(adapter)
        overlay = out / (side + '-server-overlay.json')
        perf.write_json(overlay, {'Replace': replacements}, exclusive=True)
        binary = out / (side + '-server.test')
        invocation = ['go', 'test', '-p=2', '-overlay', str(overlay), '-c', '-o', str(binary), './internal/app']
        sources_before = source_map(checkout)
        helpers_before = {kind: perf.metadata(path) for kind, path in helpers.items()}
        adapter_before = perf.metadata(adapter)
        perf.command(invocation, timeout=300, cwd=checkout)
        cli = Path(manifest['baseline']['path']) if side == 'baseline' else (args.candidate_cli.resolve(strict=True) if args.candidate_cli else out / 'candidate-server-cli')
        cli_invocation = None
        if side == 'candidate':
            if args.candidate_cli:
                if not args.candidate_cli_receipt:
                    raise RuntimeError('An existing candidate CLI requires its exact source/build receipt')
                external = json.loads(args.candidate_cli_receipt.read_text())
                if (external.get('schema') != 'core-performance-candidate-cli-v1'
                        or external.get('binary') != perf.metadata(cli)
                        or external.get('source_sha256') != sources_before):
                    raise RuntimeError('External candidate CLI receipt does not match exact bytes and current source map')
                cli_invocation = external['build_command']
            else:
                identity = hashlib.sha256(json.dumps(sources_before,sort_keys=True,separators=(',',':')).encode()).hexdigest()
                cli_invocation = ['go', 'build', '-p=2', '-trimpath', '-buildvcs=false',
                                  '-ldflags', '-X main.version=perf-candidate -X main.commit=source-sha256-' + identity,
                                  '-o', str(cli), './cmd/cutmy']
                perf.command(cli_invocation, timeout=300, cwd=checkout)
        elif perf.sha256(cli) != manifest['baseline']['sha256']:
            raise RuntimeError('Frozen baseline CLI bytes changed')
        if sources_before != source_map(checkout):
            raise RuntimeError('Runtime source changed during server witness compilation; no successful receipt')
        if helpers_before != {kind: perf.metadata(path) for kind, path in helpers.items()} or adapter_before != perf.metadata(adapter):
            raise RuntimeError('Server helper/adapter changed during compilation; no successful receipt')
        builds.append({'side': side, 'checkout': str(checkout), 'command': invocation,
                       'go': perf.command(['go', 'version'], cwd=checkout).decode().strip(),
                       'source_sha256': sources_before, 'config_adapter': adapter_before,
                       'helpers': helpers_before, 'cli_command': cli_invocation})
        products[side] = perf.metadata(binary)
        cli_products[side] = perf.metadata(cli)
    if (harness_before != perf.metadata(__file__) or measurement_before != perf.metadata(ROOT / 'core-performance.py')
            or launcher_before != perf.metadata(ROOT / 'testdata/core-performance-cgroup.py')):
        raise RuntimeError('Orchestration/measurement/launcher source changed during compilation; no successful receipt')
    receipt = {'manifest_sha256': perf.sha256(args.manifest), 'harness': harness_before,
               'measurement_harness': measurement_before, 'builds': builds, 'products': products,
               'cli_products': cli_products, 'role_launcher': launcher_before,
               'registered_policy': 'Four initial requests/owner at a barrier; retain all real job_limit refusals and retry once only after observed owner completion. Baseline preparation=0; candidate=explicit labelled 0..8. Real independent CLI API/worker roles with per-role owned cgroups; outer helper tree and PostgreSQL observations recorded separately. In-process mode is explicitly supplemental.'}
    perf.write_json(out / 'provenance.json', receipt, exclusive=True)
    print(json.dumps({'products': {side: product['path'] for side, product in products.items()}, 'provenance': str(out / 'provenance.json')}))


def pg_snapshot(path):
    if not path:
        return None
    result = {}
    for name in ('memory.current', 'memory.peak', 'memory.events', 'cpu.stat', 'io.stat'):
        file = path / name
        result[name] = file.read_text() if file.exists() else None
    return result


def sample(args, manifest, scenario, side, pair, root, products, cli_products):
    perf.ensure_space(root, 512 * 1024 ** 2)
    case = root / f'{scenario["id"]}-p{pair:02}-{side}'
    case.mkdir()
    media = case / 'media'
    media.mkdir()
    tools = perf.create_wrappers(case, manifest['tools'])
    environment = os.environ.copy()
    for name in ('CUTMY_CORE_PERF_SERVER_CLI','CUTMY_CORE_PERF_ROLE_LAUNCHER','CUTMY_CORE_PERF_CGROUP_ROOT'):
        environment.pop(name, None)
    fixture = manifest['fixtures'][scenario['fixture']]
    variant = 0 if side == 'baseline' else args.preparation
    environment.update({'PATH': str(tools) + os.pathsep + environment.get('PATH', ''),
                        'GOMAXPROCS': '2', 'GOMEMLIMIT': '128MiB',
                        'FFMPEG_PATH': str(tools / 'ffmpeg'), 'FFPROBE_PATH': str(tools / 'ffprobe'),
                        'CUTMY_CORE_PERF_SERVER_SCENARIO': str(case / 'scenario.json'),
                        'CUTMY_CORE_PERF_SERVER_FIXTURE': fixture['path'],
                        'CUTMY_CORE_PERF_SERVER_FIXTURE_SHA256': fixture['sha256'],
                        'CUTMY_CORE_PERF_SERVER_WORK': str(media),
                        'CUTMY_CORE_PERF_SERVER_RESULT': str(case / 'server-result.json'),
                        'CUTMY_CORE_PERF_SERVER_PREPARATION': str(variant),
                        'CUTMY_CORE_PERF_IDLE_WINDOW': args.idle_window})
    perf.write_json(case / 'scenario.json', scenario, exclusive=True)
    launcher = ROOT / 'testdata/core-performance-cgroup.py'
    cgroup = Path('/sys/fs/cgroup/system.slice/cutmy-go-perf-' + uuid.uuid4().hex)
    before = pg_snapshot(args.pg_cgroup)
    if not args.in_process:
        environment.update({'CUTMY_CORE_PERF_SERVER_CLI':cli_products[side]['path'],
                            'CUTMY_CORE_PERF_ROLE_LAUNCHER':str(launcher), 'CUTMY_CORE_PERF_CGROUP_ROOT':str(cgroup)})
        subprocess.run(['python3', str(launcher), '--create-root'], env=environment, check=True, capture_output=True, timeout=10)
    binary = products[side]['path']
    try:
        metrics = perf.measured_process([binary, '-test.run', '^TestCorePerformanceServerObservation$', '-test.count=1', '-test.timeout=180s'], environment, case)
        if not args.in_process:
            subprocess.run(['python3', str(launcher), '--cleanup'], env=environment, check=True, capture_output=True, timeout=10)
    except BaseException:
        # CLI roles run in their own sessions/cgroups. An outer helper timeout
        # must drain those exact owned groups before removing their directories.
        # Keep the original failure and any kernel resource evidence.
        if not args.in_process:
            evidence = {'scope':'Failure-only drain of this sample owned role groups'}
            try:
                drain = subprocess.run(['python3', str(launcher), '--kill-owned'], env=environment,
                                       capture_output=True, timeout=15)
                evidence['exit_code'] = drain.returncode
                try:
                    evidence['snapshot'] = json.loads(drain.stdout)
                except (ValueError, UnicodeError):
                    evidence['snapshot_unavailable'] = True
            except BaseException as error:
                evidence['drain_error_type'] = type(error).__name__
            perf.write_json(case / 'cgroup-failure-drain.json', evidence, exclusive=True)
            try:
                subprocess.run(['python3', str(launcher), '--cleanup'], env=environment,
                               check=True, capture_output=True, timeout=10)
            except BaseException:
                pass  # Preserve the initial helper/cleanup failure.
        raise
    after = pg_snapshot(args.pg_cgroup)
    payload = json.loads((case / 'server-result.json').read_text())
    expected = scenario['owners'] * scenario['jobs_per_owner'] * scenario['cuts_per_job']
    if len(payload['outputs']) != expected or payload['admitted'] != scenario['owners'] * scenario['jobs_per_owner']:
        raise RuntimeError('Server witness silently omitted registered requests/outputs')
    outputs = [perf.output_fingerprint(item, manifest['tools']) for item in payload['outputs']]
    request_profile = perf.PROFILES[scenario['profile']]
    perf.verify_shape({'count': expected, 'options': request_profile, 'output_mode': 'separate'}, outputs)
    record = {'manifest_sha256': perf.sha256(args.manifest), 'scenario': scenario['id'], 'side': side, 'pair': pair,
              'preparation': variant, 'helper_sha256': products[side]['sha256'], 'case_path': str(case),
              'server': {key: value for key, value in payload.items() if key != 'outputs'}, 'outputs': outputs,
              'pg_cgroup_before': before, 'pg_cgroup_after': after,
              'pg_scope': 'Cumulative/endpoint cgroup captures include fixture setup, helper and prior work; not isolated per-service peaks. Media verification starts after the endpoint.',
              'subprocess_counts': {name: len((case / (name + '.log')).read_text().splitlines())
                                    if (case / (name + '.log')).exists() else 0 for name in manifest['tools']},
              'actual_download_bytes': sum(item['bytes'] for item in outputs), **metrics}
    perf.write_json(case / 'metrics.json', record, exclusive=True)
    return record


def run(args):
    if not args.idle_window or not os.environ.get('TEST_DATABASE_URL'):
        raise RuntimeError('An owned disposable TEST_DATABASE_URL and coordinator idle window are required')
    if not 0 <= args.preparation <= 8:
        raise RuntimeError('Preparation must be explicitly bounded 0..8')
    if args.pairs < (1 if args.smoke else 3):
        raise RuntimeError('Normal measurements require at least three pairs; a one-pair smoke needs --smoke')
    manifest = perf.load_manifest(args.manifest)
    receipt = json.loads(args.provenance.read_text())
    if receipt['manifest_sha256'] != perf.sha256(args.manifest):
        raise RuntimeError('Server build receipt belongs to another frozen manifest')
    products = receipt['products']
    cli_products = receipt.get('cli_products', {})
    if not args.in_process and set(cli_products) != {'baseline','candidate'}:
        raise RuntimeError('Real CLI roles require a matching build receipt with both exact CLI artifacts')
    if not args.in_process and receipt.get('role_launcher') != perf.metadata(ROOT / 'testdata/core-performance-cgroup.py'):
        raise RuntimeError('Role launcher changed after server build receipt')
    for product in [*products.values(), *cli_products.values()]:
        if perf.sha256(product['path']) != product['sha256']:
            raise RuntimeError('Compiled server helper bytes changed')
    scenarios = [scenario for scenario in manifest['server_scenarios'] if not args.scenario or scenario['id'] in args.scenario]
    if not scenarios or args.scenario and set(args.scenario) - {scenario['id'] for scenario in scenarios}:
        raise RuntimeError('Unknown/unselected frozen server scenario')
    root = args.output.resolve()
    root.mkdir(parents=True, exist_ok=True)
    records = []
    with (root / '.measurement.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        perf.write_json(root / 'environment.json', {'manifest_sha256': perf.sha256(args.manifest), 'harness': perf.metadata(__file__),
                                                   'build_receipt': perf.metadata(args.provenance), 'preparation': args.preparation,
                                                   'scenarios': [row['id'] for row in scenarios], 'pairs': args.pairs,
                                                   'idle_window': args.idle_window, 'platform': platform.platform(),
                                                   'affinity': sorted(os.sched_getaffinity(0)), 'load_average': os.getloadavg(),
                                                   'gomaxprocs': os.environ.get('GOMAXPROCS'), 'gomemlimit': os.environ.get('GOMEMLIMIT'),
                                                   'helper_runtime': {'GOMAXPROCS': '2', 'GOMEMLIMIT': '128MiB'}, 'smoke_only': args.smoke,
                                                   'service_mode': 'supplemental_in_process' if args.in_process else 'independent_cli_cgroups',
                                                   'utc': datetime.datetime.now(datetime.timezone.utc).isoformat()}, exclusive=True)
        for scenario in scenarios:
            for pair in range(1, args.pairs + 1):
                matched = {}
                for side in (('baseline', 'candidate') if pair % 2 else ('candidate', 'baseline')):
                    record = sample(args, manifest, scenario, side, pair, root, products, cli_products)
                    records.append(record)
                    matched[side] = record
                    print(json.dumps({'scenario': scenario['id'], 'pair': pair, 'side': side,
                                      'preparation': record['preparation'], 'burst_seconds': record['server']['burst_seconds'],
                                      'admitted': record['server']['admitted'], 'refusals': record['server']['initial_job_limit_refusals'],
                                      'counts': record['subprocess_counts']}), flush=True)
                perf.compare_fingerprints(matched['baseline']['outputs'], matched['candidate']['outputs'], copy_mode=scenario['profile'] == 'copy-mp4')
                for record in matched.values():
                    case = Path(record['case_path'])
                    shutil.rmtree(case / 'media')
                    record['verified_media_removed'] = True
                    perf.write_json(case / 'metrics.json', record)
                perf.write_json(root / 'records.json', records)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    actions = parser.add_subparsers(dest='action', required=True)
    built = actions.add_parser('build')
    built.add_argument('--manifest', type=Path, required=True)
    built.add_argument('--baseline-core', type=Path, required=True)
    built.add_argument('--candidate-core', type=Path, required=True)
    built.add_argument('--output', type=Path, required=True)
    built.add_argument('--candidate-cli', type=Path, help='Reuse the root frozen exact CLI instead of building a provisional artifact')
    built.add_argument('--candidate-cli-receipt', type=Path, help='Strict core-performance-candidate-cli-v1 source/build receipt')
    measured = actions.add_parser('run')
    measured.add_argument('--manifest', type=Path, required=True)
    measured.add_argument('--provenance', type=Path, required=True)
    measured.add_argument('--output', type=Path, required=True)
    measured.add_argument('--scenario', action='append')
    measured.add_argument('--pairs', type=int, default=3)
    measured.add_argument('--smoke', action='store_true', help='Explicitly label a one-pair functional pilot; no performance conclusion')
    measured.add_argument('--in-process', action='store_true', help='Supplemental same-process API/worker only; excludes per-role resource claims')
    measured.add_argument('--preparation', type=int, default=0)
    measured.add_argument('--idle-window', required=True)
    measured.add_argument('--pg-cgroup', type=Path)
    args = parser.parse_args()
    if args.action == 'build':
        build(args)
    else:
        run(args)


if __name__ == '__main__':
    main()
