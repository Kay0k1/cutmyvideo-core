#!/usr/bin/env python3
"""Deterministic package primitive shared by release and nonpublishable candidate."""
import argparse
from datetime import datetime, timezone
import gzip
import io
from pathlib import Path
import stat
import subprocess
import tarfile
import tempfile
import zipfile

import release_manifest as manifest


def package(source, destination, identity, target_os, target_arch, expected_binary_sha256):
    source, destination = Path(source), Path(destination)
    manifest.validate_source_shape(identity)
    expected_prefix = manifest.prefix(identity, target_os, target_arch)
    manifest.require(source.name == expected_prefix, 'Package directory has wrong immutable identity.')
    binary_name = 'cutmy.exe' if target_os == 'windows' else 'cutmy'
    expected = {binary_name, *manifest.DOCUMENTS}
    manifest.require(source.is_dir() and not source.is_symlink()
                     and {p.name for p in source.iterdir()} == expected, 'Unexpected staging files.')
    manifest.require(all(p.is_file() and not p.is_symlink() for p in source.iterdir()), 'Nonregular staging file.')
    # Capture the bytes once. Receipt, embedded-info reader and archive all refer
    # to this same payload even if the original staging path later changes.
    contents = {p.name: p.read_bytes() for p in source.iterdir()}
    manifest.require(manifest.HASH.fullmatch(expected_binary_sha256)
                     and manifest.digest(contents[binary_name]) == expected_binary_sha256,
                     'Binary differs from the digest captured immediately after the build.')
    with tempfile.TemporaryDirectory(prefix='cutmy-package-info-') as temporary:
        inspected = Path(temporary) / binary_name
        inspected.write_bytes(contents[binary_name])
        manifest.binary_info(inspected, identity, target_os, target_arch)
    contents['BUILD.json'] = manifest.canonical(manifest.build_metadata(identity, target_os, target_arch, contents[binary_name]))
    manifest.validate_contents(contents, identity, target_os, target_arch)
    epoch = identity['source_epoch']
    destination.mkdir(parents=True, exist_ok=True)
    archive = destination / manifest.archive_name(identity, target_os, target_arch)
    with archive.open('xb') as out:
        if target_os == 'windows':
            stamp = datetime.fromtimestamp(max(epoch, 315532800), timezone.utc).timetuple()[:6]
            with zipfile.ZipFile(out, 'w', compression=zipfile.ZIP_DEFLATED, compresslevel=9) as zipped:
                for name, data in sorted(contents.items()):
                    info = zipfile.ZipInfo(f'{expected_prefix}/{name}', date_time=stamp)
                    info.create_system = 3
                    info.external_attr = (stat.S_IFREG | (0o755 if name == binary_name else 0o644)) << 16
                    info.compress_type = zipfile.ZIP_DEFLATED
                    zipped.writestr(info, data, compresslevel=9)
        else:
            with gzip.GzipFile(filename='', mode='wb', fileobj=out, mtime=epoch, compresslevel=9) as compressed:
                with tarfile.open(fileobj=compressed, mode='w', format=tarfile.PAX_FORMAT) as tar:
                    for name, data in sorted(contents.items()):
                        info = tarfile.TarInfo(f'{expected_prefix}/{name}')
                        info.size, info.mtime = len(data), epoch
                        info.mode = 0o755 if name == binary_name else 0o644
                        tar.addfile(info, io.BytesIO(data))
    manifest.read_archive(archive, identity, target_os, target_arch)
    return archive


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('source', type=Path)
    parser.add_argument('destination', type=Path)
    parser.add_argument('--source', dest='identity', type=Path, required=True)
    parser.add_argument('--os', required=True)
    parser.add_argument('--arch', required=True)
    parser.add_argument('--expected-binary-sha256', required=True)
    args = parser.parse_args()
    package(args.source, args.destination, manifest.load_source(args.identity), args.os, args.arch, args.expected_binary_sha256)


if __name__ == '__main__':
    try:
        main()
    except (manifest.InvalidRelease, OSError, subprocess.CalledProcessError, ValueError, KeyError, TypeError) as error:
        raise SystemExit(f'Package verification failed: {error}')
