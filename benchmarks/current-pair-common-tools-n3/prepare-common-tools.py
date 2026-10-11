#!/usr/bin/env python3
"""One finite hosted preparation; no application/media/DB run, pull, save or publish.

Uses the already-reviewed driver's evidence/command helpers, not a new runner.
This script is SOURCE-only until a separate review authorizes its execution.
"""
import argparse
import copy
import hashlib
import importlib.util
import io
import json
import lzma
import os
from pathlib import Path, PurePosixPath
import shutil
import signal
import stat
import subprocess
import tarfile
import time
import uuid

HERE = Path(__file__).resolve().parent
ARCHIVE_BYTES = 156191208
ARCHIVE_SHA = '0a8d7a93f8a53aacf09a81b6d31bfab790f28de5ea707a08c84d04c85eaec5fa'
DESCRIPTOR_SHA = '00fe35e1f0938a51105fd5ffb89b068a32e7288907cb0285e614bfa4ca810705'
TEMPLATE_SHA = '40268ba921b7c9b0bcb117928a79a38de191e4403a903034c7ae6d9361160a28'
DOCKERFILE_SHA = 'b9d61772acc751a333ec8041af8316095c9387d261af69eadd0cd6c755ee311b'
GENERATOR_MANIFEST_SHA = '3b1e14da1a6abe9409d85eb7ec0d6ada33652b838a139dc14af53500fcf60c05'
BASE = 'ubuntu@sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55'
PG = 'postgres@sha256:3645570cccdfa447589da9f57dd740faa29b30938e861289a5574b6ca6b03826'
BASE_LAYER = 'sha256:d81fb2a7c6ee3be05fdefaa7fd223be219c1ab172083ec1b994c3ecda33ac0a8'
PREFIX = 'ffmpeg-master-latest-linux64-gpl/'
MEMBERS = {
    PREFIX + 'bin/ffmpeg': ('ffmpeg', 175824712, 'bd3eda8dfc184ef91f13dc32d5f67dd365b29021013d7cc3f4d48390d2481804', 0o755),
    PREFIX + 'bin/ffprobe': ('ffprobe', 175570696, 'ce9bfa4ff51ebac283a2909ee880bd828873349764ebef5bb1237479955f69a5', 0o755),
    PREFIX + 'LICENSE.txt': ('LICENSE.txt', 35147, '8ceb4b9ee5adedde47b31e975c1d90c73ad27b6b165a1dcd80c7c545eb65b903', 0o644),
}
DATASET = {
    'fixture/main.mp4': (18495443, 'dbcce2e3a87a05e104be167e3075c7c98cd23da867f8b858ac3edb724580364d'),
    'proof/input-pcm.f32le': (7680000, '36a0aa34d4c83a0f890e9d60288c0870ab9f76d77ef1a31c28f588f8ff4d81e2'),
    'FIXTURES-complete-audio.json': (3640, GENERATOR_MANIFEST_SHA),
    'provenance/FIXTURES-original.json': (3417, 'a677c4b8e37055075a97183679634fdb1d375a2bc8158208f583f0ca8a55805f'),
    'proof/input-audio-span-receipt.json': (44431, 'ea569097946623aa50a2f8cdd3fc00cf98ab4de75e1bfdb4c2bfbfd6ad15f9f5'),
    'provenance/actual-generation.json': (1440, 'c516c78afb08b6a55ad852b90357952d0fa8d1b8ccbb7c757460e46c9e6a6731'),
    'provenance/input-audio-commands.jsonl': (333, '6108ef1b50ae2e56dda9b91b9d29204a714884d359ee271ccfe23fdf71f27d19'),
    'provenance/natural-source.json': (1084, 'a3757fa351c2cc7509edbacd09004e03c06d0f59dec90ec7d9e660aa45e30d4b'),
    'proof/main-proof.json': (7951, '72d8299a371c6dd6dc435206c39a6a2c2f39943b6a017e25ae50b62a52d0dfa5'),
    'ATTRIBUTION.md': (3045, 'f380812e49877aede12e37b43f7acb7f7917078a763c0286b241052b64b88262'),
}


def load_driver():
    spec = importlib.util.spec_from_file_location('common_pair_capacity', HERE / 'media-capacity.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def existing_directory(path):
    path = Path(path)
    if not path.is_absolute() or path.resolve() != path or not path.is_dir():
        raise RuntimeError('Existing absolute nonsymlink directory required')
    return path


def memory_available():
    fields = dict(line.split(':', 1) for line in Path('/proc/meminfo').read_text().splitlines())
    return int(fields['MemAvailable'].split()[0]) * 1024


def reserves(roots, floor=8 << 30):
    sample = {'memory_available_bytes': memory_available(),
              'free_bytes': {str(root): shutil.disk_usage(root).free for root in roots}}
    if sample['memory_available_bytes'] < 8 << 30 or min(sample['free_bytes'].values()) < floor:
        raise RuntimeError('Preparation RAM/disk reserve exhausted; refuse later stages')
    return sample


def extract_context(m, archive, context):
    # Fixed official archive hash is checked before the streaming parser is entered.
    m.require(archive.stat().st_size == ARCHIVE_BYTES and m.sha(archive) == ARCHIVE_SHA,
              'First compressed archive size/hash mismatch; no retry/substitution')
    context.mkdir(mode=0o700)
    started = time.monotonic()
    expanded = 0
    seen = set()
    selected = set()

    class CappedReader(io.RawIOBase):
        def readable(self): return True
        def read(self, size=-1):
            nonlocal expanded
            m.require(time.monotonic() - started < 180, 'Extractor180s deadline')
            block = decoded.read(1 << 20 if size < 0 else min(size, 1 << 20))
            expanded += len(block)
            m.require(expanded <= 768 << 20, 'Expanded archive exceeds768MiB')
            return block

    with lzma.LZMAFile(archive, 'rb') as decoded:
        with tarfile.open(fileobj=CappedReader(), mode='r|') as tar:
            for member in tar:
                name = member.name
                path = PurePosixPath(name)
                m.require(len(seen) < 73 and name not in seen and not path.is_absolute()
                          and all(part not in ('', '.', '..') for part in name.split('/'))
                          and (member.isdir() or member.isfile()) and member.mode in (0o644, 0o755),
                          'Unexpected/unsafe/duplicate/link/special tar member')
                m.require(name == PREFIX[:-1] or name.startswith(PREFIX), 'Unexpected archive prefix')
                seen.add(name)
                m.require(0 <= member.size <= 768 << 20, 'Member size exceeds bound')
                if name not in MEMBERS:
                    continue
                basename, size, expected, mode = MEMBERS[name]
                m.require(member.isfile() and member.size == size and member.mode == mode,
                          'Listed regular member identity changed')
                digest = hashlib.sha256()
                total = 0
                with tar.extractfile(member) as source, (context / basename).open('xb') as destination:
                    os.chmod(context / basename, 0o600)
                    while True:
                        m.require(time.monotonic() - started < 180, 'Extractor180s deadline')
                        block = source.read(1 << 20)
                        if not block: break
                        total += len(block)
                        m.require(total <= size, 'Listed member exceeds exact size')
                        destination.write(block)
                        digest.update(block)
                m.require(total == size and digest.hexdigest() == expected, 'Listed member SHA/size mismatch')
                (context / basename).chmod(mode)
                selected.add(name)
    m.require(len(seen) == 73 and selected == set(MEMBERS), 'Frozen archive inventory changed')
    dockerfile = m.regular(HERE / 'Dockerfile.tools')
    m.require(m.sha(dockerfile) == DOCKERFILE_SHA, 'Reviewed COPY-only Dockerfile changed')
    shutil.copyfile(dockerfile, context / 'Dockerfile')
    (context / 'Dockerfile').chmod(0o644)
    total = sum(path.stat().st_size for path in context.iterdir())
    m.require(total == 351430776 and total <= 352 << 20, 'Fixed build context changed')
    return {'expanded_bytes': expanded, 'context_bytes': total,
            'members': {name: {'bytes': size, 'sha256': digest} for _, (name, size, digest, _) in MEMBERS.items()}}


def image_facts(docker, reference):
    return docker.json('image', 'inspect', '--format',
        '{"Id":{{json .Id}},"Os":{{json .Os}},"Architecture":{{json .Architecture}},'
        '"RootFS":{{json .RootFS}},"RepoDigests":{{json .RepoDigests}},"Size":{{json .Size}},'
        '"Labels":{{json .Config.Labels}}}', reference)


def copy_only_build(m, commands, docker, context, iid, tag, roots):
    # Purpose-specific copy of the existing Commands watchdog, with host-reserve checks.
    # CLI cancellation does not prove immediate backend stop. Failure ends the disposable job.
    argv = [*docker.argv, 'build', '--pull=false', '--network=none', '--iidfile', str(iid),
            '--label', f'{m.LABEL}={docker.token}', '--tag', tag, str(context)]
    env = {**docker.environment, 'DOCKER_BUILDKIT': '0'}
    label = commands.evidence.identifier('command')
    out = commands.evidence.root / (label + '.stdout')
    err = commands.evidence.root / (label + '.stderr')
    record = {'id': label, 'argv': argv, 'start_ns': time.monotonic_ns(), 'timeout_seconds': 120}
    process = None
    failed = None
    try:
        with out.open('xb') as stdout, err.open('xb') as stderr:
            process = subprocess.Popen(argv, stdout=stdout, stderr=stderr, stdin=subprocess.DEVNULL,
                                       start_new_session=True, env=env)
            record['owned_process_group'] = process.pid
            deadline = time.monotonic() + 120
            while process.poll() is None:
                m.require(time.monotonic() < deadline, 'COPY build120s client deadline')
                m.require(out.stat().st_size <= 8 << 20 and err.stat().st_size <= 8 << 20,
                          'COPY build output exceeds8MiB per stream')
                commands.evidence.event('build-reserves', reserves(roots))
                time.sleep(.2)
        m.require(process.returncode == 0, 'COPY build failed; no build fallback/retry')
        m.require(out.stat().st_size <= 8 << 20 and err.stat().st_size <= 8 << 20,
                  'COPY build output exceeds8MiB per stream')
        reserves(roots)
        record['exit'] = process.returncode
    except BaseException as error:
        failed = error
        record['error'] = type(error).__name__ + ': ' + str(error)
        raise
    finally:
        if process is not None and process.poll() is None:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait(timeout=5)
        record['end_ns'] = time.monotonic_ns()
        record['ok'] = failed is None
        commands.evidence.event('commands', record)


def validate_inputs(m, template, input_root):
    # Validate the finite staged dataset and immutable core archives before Docker work.
    for relative, (size, digest) in DATASET.items():
        path = m.regular(input_root / relative)
        m.require(path.resolve() == path and path.stat().st_size == size and m.sha(path) == digest,
                  'Staged historical input/proof/attribution bytes changed')
    fixtures = m.read_json(input_root / 'FIXTURES-complete-audio.json', GENERATOR_MANIFEST_SHA)
    m.fixture_shape(fixtures)
    fixture = fixtures['fixtures']['main']
    m.require(set(fixtures['fixtures']) == {'main'}, 'Only the frozen main SDR input is staged')
    audio = m.read_json(input_root / 'proof/input-audio-span-receipt.json', DATASET['proof/input-audio-span-receipt.json'][1])
    generation = m.read_json(input_root / 'provenance/actual-generation.json', DATASET['provenance/actual-generation.json'][1])
    m.require(audio['ok'] is True and audio['fixture_sha256'] == fixture['sha256']
              and audio['pcm_sha256'] == DATASET['proof/input-pcm.f32le'][1]
              and audio['full_pcm_bytes'] == DATASET['proof/input-pcm.f32le'][0]
              and audio['original_manifest_sha256'] == DATASET['provenance/FIXTURES-original.json'][1]
              and audio['commands_sha256'] == DATASET['provenance/input-audio-commands.jsonl'][1]
              and generation['argv'] == fixture['provenance']['generator_argv']
              and fixture['provenance']['generator_argv_receipt_sha256'] == DATASET['provenance/actual-generation.json'][1],
              'Historical generator/audio proof linkage changed')
    config = copy.deepcopy(template)
    for label, subdir in (('before', 'baseline'), ('after', 'candidate')):
        identity = config['binaries'][label]
        path = input_root / subdir / Path(identity['archive']).name
        m.require(m.regular(path).resolve() == path and path.stat().st_size <= 32 << 20
                  and m.sha(path) == identity['archive_sha256'],
                  'Staged immutable executable archive changed')
        identity['archive'] = str(path)
    return fixtures, config


def freeze_inputs(m, fixtures, config, input_root, image, pg_image, actual_tools, output):
    # No fixture is regenerated; only actual hosted paths change in a new manifest.
    fixtures, config = copy.deepcopy(fixtures), copy.deepcopy(config)
    fixture = fixtures['fixtures']['main']
    fixture['path'] = str(input_root / 'fixture/main.mp4')
    fixture['provenance']['complete_audio_proof']['path'] = str(input_root / 'proof/input-audio-span-receipt.json')
    m.fixture_shape(fixtures)
    manifest = output / 'FIXTURES.hosted.json'
    manifest.write_bytes(m.canonical(fixtures))
    config['fixture_manifest'] = {'path': str(manifest), 'sha256': m.sha(manifest)}
    config['runtime_image'] = image
    config['postgres_image'] = pg_image
    for name, actual in actual_tools.items(): config['tools'][name].update(actual)
    m.config_shape(config)
    frozen = output / 'CONFIG.frozen.json'
    frozen.write_bytes(m.canonical(config))
    return {'config_path': str(frozen), 'config_sha256': m.sha(frozen),
            'fixture_manifest_path': str(manifest), 'fixture_manifest_sha256': m.sha(manifest)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--tool-archive', type=Path, required=True)
    parser.add_argument('--input-root', type=Path, required=True)
    parser.add_argument('--output-dir', type=Path, required=True)
    parser.add_argument('--docker', type=Path, required=True)
    args = parser.parse_args()
    # The whole daemon build envelope is the exclusive disposable4CPU/16GiB hosted VM.
    # No misleading RUN-container limits and no daemon/cgroup reconfiguration are used.
    if os.geteuid() != 0 or os.environ.get('GITHUB_ACTIONS') != 'true':
        raise RuntimeError('Only root preparation on a separately admitted disposable GitHub host')
    runner_temp = existing_directory(os.environ.get('RUNNER_TEMP', ''))
    output = args.output_dir
    if not output.is_absolute() or output.exists() or output.resolve() != output or output.parent != runner_temp:
        raise RuntimeError('Output must be a new direct private child of RUNNER_TEMP')
    input_root = existing_directory(args.input_root)
    if input_root.parent != runner_temp or input_root.stat().st_uid != os.geteuid() or stat.S_IMODE(input_root.stat().st_mode) != 0o700:
        raise RuntimeError('Input root must be an owned private direct child of RUNNER_TEMP')
    m = load_driver()
    descriptor = m.read_json(HERE / 'UPSTREAM-TOOLS.json', DESCRIPTOR_SHA)
    template = m.read_json(HERE / 'CONFIG.template.json', TEMPLATE_SHA)
    archive = m.regular(args.tool_archive)
    m.require(archive.resolve() == archive and archive.stat().st_size == ARCHIVE_BYTES
              and m.sha(archive) == ARCHIVE_SHA, 'Exact staged tool archive required; no retry/substitution')
    fixtures, staged_config = validate_inputs(m, template, input_root)
    evidence = m.Evidence(output)
    output.chmod(0o700)
    commands = m.Commands(evidence)
    token = 'cutmy-common-tools-' + uuid.uuid4().hex[:16]
    docker = m.Docker(commands, str(m.regular(args.docker)), token)
    started = time.monotonic()
    # The disposable provider job enforces the outer15-minute preparation deadline.
    # Every local subprocess/extractor has its own finite watchdog as well.
    complete = False
    try:
        # Reuse the final receipt path for early ownership; timeout cleanup must know the label.
        (output / 'runtime-image-receipt.json').write_bytes(m.canonical({'schema_version': 1,
            'ok': False, 'token': token, 'scope': 'preparation started; no frozen runtime yet'}))
        host = docker.json('info', '--format',
            '{"cpus":{{.NCPU}},"memory":{{.MemTotal}},"os":{{json .OSType}},'
            '"arch":{{json .Architecture}},"root":{{json .DockerRootDir}},"security":{{json .SecurityOptions}}}')
        m.require(host['cpus'] == 4 and 12 << 30 <= host['memory'] <= 16 << 30 and host['os'] == 'linux'
                  and host['arch'] in ('amd64', 'x86_64') and not any('rootless' in x for x in host['security']),
                  'Exclusive4CPU/16GiB Linux/amd64 daemon envelope required')
        docker_root = existing_directory(host['root'])
        roots = (runner_temp, docker_root)
        reserves(roots, floor=16 << 30)
        m.require(not docker.run('ps', '--format', '{{.ID}}', timeout=15).strip(),
                  'Other running containers prevent exclusive preparation')
        base = image_facts(docker, BASE)
        pg = image_facts(docker, PG)
        m.require(BASE in base['RepoDigests'] and base['Os'] == 'linux' and base['Architecture'] == 'amd64'
                  and base['RootFS']['Layers'] == [BASE_LAYER] and base['Size'] <= 256 << 20,
                  'Already-local pinned Ubuntu base differs; no pull fallback')
        m.require(any(x in pg['RepoDigests'] for x in (PG, 'docker.io/library/' + PG))
                  and pg['Os'] == 'linux' and pg['Architecture'] == 'amd64'
                  and pg['Id'] in (descriptor['postgres_registry_index_digest'], descriptor['postgres_execution_config_id']),
                  'Already-local pinned PG platform/config differs; no pull fallback')
        context = output / 'context'
        extracted = extract_context(m, archive, context)
        iid = output / 'image-id.txt'
        copy_only_build(m, commands, docker, context, iid, token + ':prepared', roots)
        image = iid.read_text().strip()
        m.require(image.startswith('sha256:') and m.HASH.fullmatch(image[7:]), 'Build did not return immutable image ID')
        facts = image_facts(docker, image)
        m.require(facts['Id'] == image and facts['Os'] == 'linux' and facts['Architecture'] == 'amd64'
                  and facts['Labels'].get(m.LABEL) == token
                  and facts['RootFS']['Layers'][:len(base['RootFS']['Layers'])] == base['RootFS']['Layers']
                  and len(facts['RootFS']['Layers']) == len(base['RootFS']['Layers']) + 3
                  and facts['Size'] <= base['Size'] + (512 << 20), 'Prepared image identity/size/layers differ')
        actual_tools = {}
        common = ['run', '--pull=never', '--rm', '--label', f'{m.LABEL}={token}', '--network', 'none', '--read-only',
                  '--user', '65532:65532', '--cpus', '.5', '--memory', str(512 << 20), '--memory-swap', str(512 << 20),
                  '--pids-limit', '32', '--security-opt', 'no-new-privileges', '--cap-drop', 'ALL']
        for name in ('ffmpeg', 'ffprobe'):
            tool = template['tools'][name]
            m.require(tool['path'] == '/usr/local/bin/' + name, 'Only the two reviewed entrypoints are allowed')
            cid = output / (name + '-cid')
            body = 'sha256sum ' + tool['path'] + '; exec ' + tool['path'] + ' -version'
            stdout, stderr = commands.run([*docker.argv, *common, '--name', token + '-' + name,
                '--cidfile', str(cid), '--entrypoint', '/bin/sh', image, '-ec', body], timeout=30, env=docker.environment)
            lines = stdout.decode().splitlines()
            m.require(len(lines) >= 2 and lines[0].split() == [tool['sha256'], tool['path']]
                      and lines[1] == tool['version_line'], 'Actual image tool hash/version differs from frozen upstream bytes')
            actual_tools[name] = {'sha256': lines[0].split()[0], 'version_line': lines[1]}
        docker.cleanup()
        reserves(roots)
        m.require(time.monotonic() - started < 15 * 60, 'Preparation15min outer deadline')
        binding = freeze_inputs(m, fixtures, staged_config, input_root, image, pg['Id'], actual_tools, output)
        receipt = {'schema_version': 1, 'ok': True, 'scope': 'common-tool preparation only; no app/media/DB execution',
            'token': token, 'host': host, 'base': base, 'postgres': pg, 'image': facts, 'runtime_image': image,
            'tools': template['tools'], 'actual_tools': actual_tools, 'archive_sha256': ARCHIVE_SHA,
            'upstream_descriptor_sha256': DESCRIPTOR_SHA, 'dockerfile_sha256': DOCKERFILE_SHA,
            'extractor': extracted, 'binding': binding, 'owned_probe_containers_removed': True,
            'limits_scope': 'COPY daemon uses exclusive disposable4CPU/16GiB host; CLI120s/output8MiB/reserve guards; probes .5CPU/512MiB/swap0/30s. CLI cancellation is not a backend-stop claim.'}
        (output / 'runtime-image-receipt.json').write_bytes(m.canonical(receipt))
        complete = True
        print(json.dumps({'ok': True, **binding, 'runtime_image': image,
                          'receipt_sha256': m.sha(output / 'runtime-image-receipt.json')}))
    finally:
        if not complete:
            # Keep bounded failed context/raw output. No tests/media/retry after a failure.
            docker.cleanup()


if __name__ == '__main__':
    main()
