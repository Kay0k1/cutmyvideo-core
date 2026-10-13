#!/usr/bin/env python3
"""Finite supplemental slow-HLS/local pipeline proof, separate from the main matrix."""
import argparse
import datetime
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import shutil
import statistics

ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('core_performance', ROOT / 'core-performance.py')
perf = importlib.util.module_from_spec(spec)
spec.loader.exec_module(perf)


def register(args):
    frozen = json.loads(args.manifest.read_text())
    if perf.sha256(args.manifest) != args.manifest.with_suffix('.sha256').read_text().strip():
        raise RuntimeError('Original manifest digest mismatch')
    metadata = {'id': 'SlowNet0001', 'title': 'Fixed slow HLS fixture', 'duration': 12,
                'extractor_key': 'Youtube', '_type': 'video', 'live_status': 'not_live',
                'formats': [{'format_id': 'fixed-hls', 'url': 'https://media.example/index.m3u8',
                             'protocol': 'm3u8_native', 'ext': 'mp4', 'vcodec': 'h264',
                             'acodec': 'aac', 'width': 160, 'height': 90, 'tbr': 1000}]}
    metadata_path = args.output.with_name(args.output.stem + '-metadata.json')
    perf.write_json(metadata_path, metadata, exclusive=True)
    supplement = {'schema': 1, 'registered_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                  'manifest_sha256': perf.sha256(args.manifest), 'metadata': perf.metadata(metadata_path),
                  'helper': perf.metadata(ROOT / 'testdata/core-performance-mixed_test.go'),
                  'tool_wrapper': perf.metadata(ROOT / 'testdata/core-performance-tool.go'),
                  'workload': {'ordered_jobs': ['remote-owner-0', 'remote-owner-1', 'local-owner-2', 'local-owner-3'],
                               'owners': 4, 'jobs_per_owner': 1, 'worker_concurrency': 2, 'ffmpeg_threads': 2,
                               'remote_ranges_ms': [[1450, 2550], [3650, 4750], [5850, 6950], [8050, 9150]],
                               'local_ranges_ms': [[2400, 8400], [14200, 20200]],
                               'local_fixture': frozen['fixtures']['complex'], 'hls_fixture': frozen['fixtures']['hls'],
                               'latency_ms_per_request': 250, 'profile': 'accurate-mp4 separate',
                               'poll_ms': 20, 'outputs': 12,
                               'storage': '2 GiB global / 512 MiB owner / 64 MiB filesystem safety / 128 MiB source+network / 64 MiB output'},
                  'cohorts': {'baseline_p0_references': 3, 'candidate_p0_p1_pairs': 5,
                              'ordering': 'baseline P0 references first; then P0/P1, P1/P0, P0/P1, P1/P0, P0/P1',
                              'operations': 13},
                  'oracles': ['Independent full-output probe/decode and exact packet-clock/content fingerprints against baseline reference.',
                              'Every run: at most two overlapping actual FFmpeg/FFprobe process intervals; no command arguments retained.',
                              'Candidate P1: local FFmpeg input interval overlaps a remote HTTPS wait by at least 50 ms in every run.',
                              'Observed running claims never exceed 2+preparation; all work reservations zero after worker shutdown.'],
                  'scope': 'Supplemental in-process real HTTP sessions/imports/uploads/jobs/downloads with separate API/worker Stores and pools. Fixed guarded HTTPS fixture transport and synthetic extractor JSON adapter. No real yt-dlp execution/public network or per-role process/cgroup inference. Main 65+6 scenarios unchanged.',
                  'adapters': {'network': 'Replace constructor client: safeClient() with the fixed fixture client; retain guarded URI validation, budgets, HLS staging and exports.',
                               'metadata': 'Rename original platformMetadata and supply fixed validated platformInfo decoded from registered JSON for exactly two known page URLs.',
                               'config': 'Baseline rejects P1 and keeps P0; candidate changes only WorkerPreparationConcurrency to 0/1.'},
                  'timing': 'Coordinator idle window only. Primary worker-start to all downloaded outputs excludes imports/uploads/queued admission. Outer process resources include setup, schema cleanup and Go wrapper overhead. No production throughput claim.'}
    perf.write_json(args.output, supplement, exclusive=True)
    args.output.with_suffix('.sha256').write_text(perf.sha256(args.output) + '\n')
    print(json.dumps({'supplement': str(args.output), 'sha256': perf.sha256(args.output)}))


def load(args):
    frozen = perf.load_manifest(args.manifest)
    if perf.sha256(args.supplement) != args.supplement.with_suffix('.sha256').read_text().strip():
        raise RuntimeError('Mixed supplement digest mismatch')
    supplement = json.loads(args.supplement.read_text())
    if supplement['manifest_sha256'] != perf.sha256(args.manifest):
        raise RuntimeError('Mixed supplement belongs to another manifest')
    for name in ('metadata', 'helper', 'tool_wrapper'):
        value = supplement[name]
        if perf.sha256(value['path']) != value['sha256']:
            raise RuntimeError('Mixed registered input changed: ' + name)
    return frozen, supplement


def build(args):
    frozen, supplement = load(args)
    root = args.output.resolve()
    root.mkdir(parents=True, exist_ok=False)
    perf.ensure_space(root, 256 << 20)
    products, builds = {}, []
    wrapper = root / 'media-tool-witness'
    invocation = ['go', 'build', '-trimpath', '-o', str(wrapper), supplement['tool_wrapper']['path']]
    perf.command(invocation, timeout=300, cwd=args.candidate_root)
    builds.append({'kind': 'tool_wrapper', 'command': invocation, 'cwd': str(args.candidate_root),
                   'go': perf.command(['go', 'version'], cwd=args.candidate_root).decode().strip()})
    for side, checkout in (('baseline', args.baseline_root), ('candidate', args.candidate_root)):
        checkout = checkout.resolve()
        revision = perf.command(['git', '-C', checkout, 'rev-parse', 'HEAD']).decode().strip()
        if side == 'baseline':
            if revision != frozen['baseline']['revision']:
                raise RuntimeError('Mixed baseline revision differs from immutable baseline')
            perf.command(['git', '-C', checkout, 'diff', '--quiet', 'HEAD', '--', '*.go', 'go.mod', 'go.sum',
                          'internal/app/migrations/*.sql', 'internal/app/migrations/*.json'])
        before = perf.build_source_catalog(checkout)
        network = (checkout / 'internal/app/network.go').read_text()
        media = (checkout / 'internal/app/media.go').read_text()
        if network.count('client: safeClient()') != 1 or media.count('func platformMetadata(') != 1:
            raise RuntimeError('Reviewed mixed fixture injection location changed')
        network_path, media_path = root / (side + '-network.go'), root / (side + '-media.go')
        network_path.write_text(network.replace('client: safeClient()', 'client: corePerfMixedHTTPClient()'))
        media_path.write_text(media.replace('func platformMetadata(', 'func corePerfMixedOriginalPlatformMetadata('))
        adapter = root / (side + '-mixed-config_test.go')
        body = 'if preparation != 0 { panic("baseline only supports P0") }; return c' if side == 'baseline' else 'c.WorkerPreparationConcurrency = preparation; return c'
        adapter.write_text('package app\nfunc corePerfMixedConfig(c Config, preparation int) Config { ' + body + ' }\n')
        overlay = root / (side + '-mixed-overlay.json')
        perf.write_json(overlay, {'Replace': {str(checkout / 'internal/app/network.go'): str(network_path),
                                             str(checkout / 'internal/app/media.go'): str(media_path),
                                             str(checkout / 'internal/app/core_performance_mixed_test.go'): supplement['helper']['path'],
                                             str(checkout / 'internal/app/core_performance_mixed_config_test.go'): str(adapter)}})
        binary = root / (side + '-mixed.test')
        invocation = ['go', 'test', '-p=2', '-overlay', str(overlay), '-c', '-o', str(binary), './internal/app']
        perf.command(invocation, timeout=300, cwd=checkout)
        if before != perf.build_source_catalog(checkout):
            raise RuntimeError('Runtime or embedded schema changed during mixed compilation')
        products[side] = perf.metadata(binary)
        builds.append({'kind': side, 'command': invocation, 'cwd': str(checkout), 'revision': revision,
                       'go': perf.command(['go', 'version'], cwd=checkout).decode().strip(),
                       'source_sha256': before, 'overlay': perf.metadata(overlay),
                       'fixture_adaptations': [perf.metadata(path) for path in (network_path, media_path, adapter)]})
    receipt = {'manifest_sha256': perf.sha256(args.manifest), 'supplement_sha256': perf.sha256(args.supplement),
               'runner': perf.metadata(__file__), 'measurement_harness': perf.metadata(ROOT / 'core-performance.py'),
               'helper': supplement['helper'], 'products': products, 'tool_wrapper': perf.metadata(wrapper), 'builds': builds}
    perf.write_json(root / 'provenance.json', receipt, exclusive=True)
    print(json.dumps({'provenance': str(root / 'provenance.json')}))


def interval_proof(payload, tool_events):
    begin, end = payload['burst_start_ns'], payload['burst_end_ns']
    events = [event for event in tool_events if event['start_ns'] >= begin and event['end_ns'] <= end]
    boundaries = [(event['start_ns'], 1) for event in events] + [(event['end_ns'], -1) for event in events]
    active = peak = 0
    for _, delta in sorted(boundaries):
        active += delta
        peak = max(peak, active)
    local = [event for event in events if event['tool'] == 'ffmpeg' and event['local_input']]
    if not local or peak > 2 or any(event['exit'] != 0 for event in events):
        raise RuntimeError('Mixed actual subprocess bound/success/local-encoder witness failed')
    waits = [event for event in payload['network'] if event['StartNS'] >= begin]
    overlap = max([max(0, min(tool['end_ns'], wait['EndNS']) - max(tool['start_ns'], wait['StartNS']))
                   for tool in local for wait in waits] or [0])
    if payload['preparation'] == 1 and overlap < 50_000_000:
        raise RuntimeError('Candidate P1 did not overlap local export with at least 50ms of remote preparation')
    return {'actual_tool_peak_overlap': peak, 'local_encoder_intervals': len(local),
            'maximum_local_export_network_wait_overlap_seconds': overlap / 1e9,
            'tool_events': events, 'network_waits': waits}


def run(args):
    frozen, supplement = load(args)
    if not args.idle_window:
        raise RuntimeError('Coordinator --idle-window required')
    receipt = json.loads(args.provenance.read_text())
    if receipt['supplement_sha256'] != perf.sha256(args.supplement):
        raise RuntimeError('Mixed build receipt belongs to another supplement')
    for product in [*receipt['products'].values(), receipt['tool_wrapper']]:
        if perf.sha256(product['path']) != product['sha256']:
            raise RuntimeError('Mixed witness binary changed')
    root = args.output.resolve()
    root.mkdir(parents=True, exist_ok=False)
    with (root / '.measurement.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        perf.write_json(root / 'environment.json', {'manifest_sha256': perf.sha256(args.manifest),
                        'supplement_sha256': perf.sha256(args.supplement), 'provenance': perf.metadata(args.provenance),
                        'runner': perf.metadata(__file__), 'measurement_harness': perf.metadata(ROOT / 'core-performance.py'),
                        'idle_window': args.idle_window, 'utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                        'scope': supplement['scope']}, exclusive=True)
        cohorts = [('baseline', 0, index) for index in range(3)]
        for pair in range(5):
            cohorts += [('candidate', preparation, pair) for preparation in ((0, 1) if pair % 2 == 0 else (1, 0))]
        records, reference = [], None
        for side, preparation, pair in cohorts:
            perf.ensure_space(root, 512 << 20)
            case = root / f'{side}-p{preparation}-pair{pair:02}'
            case.mkdir()
            media, tools = case / 'media', case / 'tools'
            media.mkdir(); tools.mkdir()
            for name, product in frozen['tools'].items():
                path = tools / name
                shutil.copy2(receipt['tool_wrapper']['path'], path)
                perf.write_json(path.with_name(path.name + '.json'), {'Tool': product['path'], 'Label': name,
                                 'Log': str(case / 'actual-tool-events.jsonl'), 'LocalInputs': []})
            environment = os.environ.copy()
            environment.update({'FFMPEG_PATH': str(tools / 'ffmpeg'), 'FFPROBE_PATH': str(tools / 'ffprobe'),
                                'GOMAXPROCS': '2', 'GOMEMLIMIT': '128MiB',
                                'CUTMY_CORE_PERF_MIXED_WORK': str(media),
                                'CUTMY_CORE_PERF_MIXED_RESULT': str(case / 'mixed-result.json'),
                                'CUTMY_CORE_PERF_MIXED_PREPARATION': str(preparation),
                                'CUTMY_CORE_PERF_MIXED_METADATA': supplement['metadata']['path'],
                                'CUTMY_CORE_PERF_MIXED_HLS': frozen['fixtures']['hls']['path'],
                                'CUTMY_CORE_PERF_MIXED_LOCAL': frozen['fixtures']['complex']['path'],
                                'CUTMY_CORE_PERF_IDLE_WINDOW': args.idle_window})
            invocation = [receipt['products'][side]['path'], '-test.run=^TestCorePerformanceMixedPipelineObservation$',
                          '-test.count=1', '-test.timeout=170s']
            metrics = perf.measured_process(invocation, environment, case)
            payload = json.loads((case / 'mixed-result.json').read_text())
            if len(payload['outputs']) != 12:
                raise RuntimeError('Mixed witness lost an admitted output')
            tools_observed = [json.loads(line) for line in (case / 'actual-tool-events.jsonl').read_text().splitlines()]
            proof = interval_proof(payload, tools_observed)
            outputs = [perf.output_fingerprint(item, frozen['tools']) for item in payload['outputs']]
            perf.verify_shape({'count': 12, 'options': perf.PROFILES['accurate-mp4'], 'output_mode': 'separate'}, outputs)
            if reference is None:
                reference = outputs
            else:
                perf.compare_fingerprints(reference, outputs)
            record = {'side': side, 'preparation': preparation, 'pair': pair, **metrics,
                      'burst_seconds': payload['burst_seconds'], 'jobs_per_minute': payload['jobs_per_minute'],
                      'proof': proof, 'outputs': outputs, 'case_path': str(case), 'verified_media_deleted': False}
            records.append(record)
            perf.write_json(case / 'metrics.json', record, exclusive=True)
            if len(records) > 1:
                for prior in records:
                    if not prior['verified_media_deleted']:
                        shutil.rmtree(Path(prior['case_path']) / 'media')
                        prior['verified_media_deleted'] = True
                        perf.write_json(Path(prior['case_path']) / 'metrics.json', prior)
            perf.write_json(root / 'raw-results.json', records)
            print(json.dumps({key: record[key] for key in ('side', 'preparation', 'pair', 'burst_seconds')}), flush=True)
        summary = {'operations': len(records), 'verified': True,
                   'scope': supplement['scope'], 'cohorts': {}}
        for side, preparation in [('baseline', 0), ('candidate', 0), ('candidate', 1)]:
            selected = [r for r in records if r['side'] == side and r['preparation'] == preparation]
            summary['cohorts'][f'{side}-P{preparation}'] = {'operations': len(selected),
                'median_burst_seconds': statistics.median(r['burst_seconds'] for r in selected),
                'max_actual_tools': max(r['proof']['actual_tool_peak_overlap'] for r in selected),
                'min_observed_overlap_seconds': min(r['proof']['maximum_local_export_network_wait_overlap_seconds'] for r in selected)}
        summary['paired_candidate_P0_over_P1'] = [next(r['burst_seconds'] for r in records if r['side'] == 'candidate' and r['preparation'] == 0 and r['pair'] == pair) /
                                                   next(r['burst_seconds'] for r in records if r['side'] == 'candidate' and r['preparation'] == 1 and r['pair'] == pair) for pair in range(5)]
        perf.write_json(root / 'summary.json', summary, exclusive=True)
        print(json.dumps(summary))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    actions = parser.add_subparsers(dest='action', required=True)
    for action in ('register', 'build', 'run'):
        sub = actions.add_parser(action)
        sub.add_argument('--manifest', type=Path, required=True)
        sub.add_argument('--output', type=Path, required=True)
        if action != 'register':
            sub.add_argument('--supplement', type=Path, required=True)
        if action == 'build':
            sub.add_argument('--baseline-root', type=Path, required=True)
            sub.add_argument('--candidate-root', type=Path, required=True)
        if action == 'run':
            sub.add_argument('--provenance', type=Path, required=True)
            sub.add_argument('--idle-window', required=True)
    args = parser.parse_args()
    {'register': register, 'build': build, 'run': run}[args.action](args)


if __name__ == '__main__':
    main()
