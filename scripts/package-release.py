#!/usr/bin/env python3
"""Deterministic release archives: no host paths, owners or wall-clock metadata."""
from datetime import datetime, timezone
import gzip
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import zipfile

source, destination = Path(sys.argv[1]), Path(sys.argv[2])
version, revision, target_os, target_arch = sys.argv[3:]
epoch = int(os.environ['SOURCE_DATE_EPOCH'])
binary = source / ('cutmy.exe' if target_os == 'windows' else 'cutmy')
metadata = {
    'version': version, 'commit': revision, 'os': target_os, 'arch': target_arch,
    'build_time': datetime.fromtimestamp(epoch, timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'),
    'toolchain': subprocess.check_output(['go', 'env', 'GOVERSION'], text=True).strip(),
    'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(),
    'dependencies': 'FFmpeg and ffprobe are external; server platform sources also require yt-dlp and Node.js.',
}
(source / 'BUILD.json').write_text(json.dumps(metadata, indent=2, sort_keys=True) + '\n', encoding='utf-8')
destination.mkdir(parents=True, exist_ok=True)
extension = '.zip' if target_os == 'windows' else '.tar.gz'
archive = destination / (source.name + extension)
with archive.open('xb') as out:
    if target_os == 'windows':
        stamp = datetime.fromtimestamp(max(epoch, 315532800), timezone.utc).timetuple()[:6]
        with zipfile.ZipFile(out, 'w', compression=zipfile.ZIP_DEFLATED, compresslevel=9) as zipped:
            for path in sorted(source.iterdir()):
                info = zipfile.ZipInfo(f'{source.name}/{path.name}', date_time=stamp)
                info.create_system = 3
                info.external_attr = (0o100755 if path == binary else 0o100644) << 16
                info.compress_type = zipfile.ZIP_DEFLATED
                zipped.writestr(info, path.read_bytes(), compresslevel=9)
    else:
        with gzip.GzipFile(filename='', mode='wb', fileobj=out, mtime=epoch, compresslevel=9) as compressed:
            with tarfile.open(fileobj=compressed, mode='w', format=tarfile.PAX_FORMAT) as tar:
                for path in sorted(source.iterdir()):
                    info = tarfile.TarInfo(f'{source.name}/{path.name}')
                    info.size = path.stat().st_size
                    info.mtime = epoch
                    info.mode = 0o755 if path == binary else 0o644
                    with path.open('rb') as data:
                        tar.addfile(info, data)
