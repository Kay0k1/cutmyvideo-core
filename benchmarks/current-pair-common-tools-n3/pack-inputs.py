#!/usr/bin/env python3
"""One local copy-only frozen dataset pack, with no child/network/tool execution."""
import argparse
import ast
import gzip
import hashlib
import json
import os
from pathlib import Path
import resource
import stat
import sys
import tarfile


def deny_runtime(event, arguments):
    if event.startswith(('subprocess.', 'socket.', 'urllib.')) or event in ('os.system', 'os.fork', 'os.forkpty', 'os.posix_spawn', 'os.exec'):
        raise RuntimeError('Dataset copy denied process/network audit: ' + event)


sys.addaudithook(deny_runtime)
os.sched_setaffinity(0, {min(os.sched_getaffinity(0))})
resource.setrlimit(resource.RLIMIT_AS, (512 << 20, 512 << 20))
resource.setrlimit(resource.RLIMIT_FSIZE, (32 << 20, 32 << 20))
resource.setrlimit(resource.RLIMIT_CPU, (60, 60))
os.nice(15)
here = Path(__file__).resolve().parent
tree = ast.parse((here / 'prepare-common-tools.py').read_bytes())
constants = {}
for node in tree.body:
    if isinstance(node, ast.Assign) and len(node.targets) == 1 and isinstance(node.targets[0], ast.Name):
        if node.targets[0].id == 'GENERATOR_MANIFEST_SHA':
            constants[node.targets[0].id] = ast.literal_eval(node.value)
        if node.targets[0].id == 'DATASET':
            dataset = {}
            for key, value in zip(node.value.keys, node.value.values):
                size, digest = value.elts
                dataset[ast.literal_eval(key)] = (ast.literal_eval(size),
                    constants[digest.id] if isinstance(digest, ast.Name) else ast.literal_eval(digest))

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--output-directory', type=Path, required=True)
args = parser.parse_args()
output = args.output_directory
assert output.is_absolute() and output.resolve() == output and output.is_dir()
assert output.stat().st_uid == os.geteuid() and stat.S_IMODE(output.stat().st_mode) == 0o700
base = Path('/root/video_cut/reports/mixed-media-capacity-2026-10-11')
mapping = {
    'fixture/main.mp4': base / 'fixtures-1080-frozen/main.mp4',
    'proof/input-pcm.f32le': base / 'input-complete-audio-proof/command-00001.stdout',
    'FIXTURES-complete-audio.json': base / 'FIXTURES-complete-audio.json',
    'provenance/FIXTURES-original.json': base / 'fixtures-1080-frozen/FIXTURES.json',
    'proof/input-audio-span-receipt.json': base / 'input-complete-audio-proof/input-audio-span-receipt.json',
    'provenance/actual-generation.json': base / 'fixtures-1080-frozen/actual-generation.json',
    'provenance/input-audio-commands.jsonl': base / 'input-complete-audio-proof/commands.jsonl',
    'provenance/natural-source.json': base / 'natural-source/provenance.json',
    'proof/main-proof.json': base / 'fixtures-1080-frozen/main-proof.json',
    'ATTRIBUTION.md': here / 'ATTRIBUTION.md',
}
assert set(mapping) == set(dataset) and len(dataset) == 10
assert sum(size for size, digest in dataset.values()) == 26240784


def sha(path):
    digest = hashlib.sha256()
    with path.open('rb') as source:
        while block := source.read(1 << 20): digest.update(block)
    return digest.hexdigest()


def check_sources():
    for name, source in mapping.items():
        size, digest = dataset[name]
        assert source.resolve() == source and source.is_file() and not source.is_symlink()
        assert source.stat().st_size == size and sha(source) == digest, name


check_sources()
archive = output / 'cutmyvideo-main1080-inputs-20261011.tar.gz'
with archive.open('xb') as destination:
    archive.chmod(0o600)
    with gzip.GzipFile(filename='', mode='wb', fileobj=destination, mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode='w|', format=tarfile.USTAR_FORMAT) as tar:
            for name, (size, digest) in dataset.items():
                member = tarfile.TarInfo(name)
                member.size, member.mode, member.uid, member.gid, member.mtime = size, 0o644, 0, 0, 0
                member.uname = member.gname = ''
                with mapping[name].open('rb') as source: tar.addfile(member, source)
check_sources()
assert archive.stat().st_size <= 32 << 20
print(json.dumps({'ok': True, 'archive': str(archive), 'bytes': archive.stat().st_size,
                  'sha256': sha(archive), 'payload_bytes': 26240784, 'members': list(dataset),
                  'limits': {'cpu_affinity_count': 1, 'address_space_bytes': 512 << 20,
                             'output_bytes': 32 << 20, 'cpu_seconds': 60, 'nice': os.getpriority(os.PRIO_PROCESS, 0)},
                  'process_and_network_audit': 'denied'}))
