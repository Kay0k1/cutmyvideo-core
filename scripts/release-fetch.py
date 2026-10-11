#!/usr/bin/env python3
"""Fetch one run-bound GitHub artifact and verify its ZIP before extraction."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import struct
import tempfile
import urllib.error
import urllib.parse
import urllib.request
import zipfile

import release_manifest as release

MAX_DOWNLOAD = 256 * 1024 * 1024
MAX_MEMBER = 128 * 1024 * 1024
MAX_TOTAL = 512 * 1024 * 1024


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, newurl):
        return None


def bounded_read(response, limit):
    result = bytearray()
    while True:
        chunk = response.read(min(1024 * 1024, limit + 1 - len(result)))
        if not chunk:
            return bytes(result)
        result.extend(chunk)
        release.require(len(result) <= limit, 'Artifact response exceeds its byte limit.')


def preflight_zip(path, count):
    """Bound the central directory before ZipFile allocates its member list."""
    with Path(path).open('rb') as stream:
        size = path.stat().st_size
        release.require(22 <= size <= MAX_DOWNLOAD, 'Invalid artifact ZIP size.')
        stream.seek(-22, 2)
        end = stream.read(22)
        values = struct.unpack('<4s4H2IH', end)
        signature, disk, directory_disk, disk_count, total_count, length, offset, comment = values
        release.require(signature == b'PK\x05\x06' and comment == 0 and disk == directory_disk == 0
                        and disk_count == total_count == count and length <= 65536
                        and offset + length == size - 22, 'Unsupported artifact ZIP directory.')
        stream.seek(offset)
        directory = stream.read(length)
    position = 0
    for _ in range(count):
        release.require(position + 46 <= length and directory[position:position + 4] == b'PK\x01\x02',
                        'Malformed artifact ZIP member header.')
        name_length, extra_length, comment_length = struct.unpack_from('<3H', directory, position + 28)
        release.require(0 < name_length <= 256 and extra_length <= 1024 and comment_length <= 1024,
                        'Artifact ZIP member metadata is too large.')
        position += 46 + name_length + extra_length + comment_length
        release.require(position <= length, 'Truncated artifact ZIP directory.')
    release.require(position == length, 'Artifact ZIP contains extra central-directory entries.')


def extract_bundle(archive, destination, source):
    expected = {release.archive_name(source, os_, arch) for os_, arch in release.TARGETS}
    expected |= {'SOURCE.json', 'MANIFEST.json', 'SHA256SUMS'}
    preflight_zip(archive, len(expected))
    destination = Path(destination)
    release.require(not destination.exists(), 'Artifact destination must not already exist.')
    destination.parent.mkdir(parents=True, exist_ok=True)
    stage = Path(tempfile.mkdtemp(prefix='.cutmy-download-', dir=destination.parent))
    try:
        with zipfile.ZipFile(archive) as zipped:
            members = zipped.infolist()
            release.require(len(members) == len(expected) and {item.filename for item in members} == expected,
                            'Artifact has missing, duplicate, unsafe or extra files.')
            release.require(sum(item.file_size for item in members) <= MAX_TOTAL, 'Artifact expanded size is too large.')
            for item in members:
                mode = item.external_attr >> 16
                release.require(not item.is_dir() and stat.S_IFMT(mode) in (0, stat.S_IFREG)
                                and not item.flag_bits & 1 and item.file_size <= MAX_MEMBER
                                and item.compress_type in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED),
                                'Artifact contains an unsupported file.')
                # No extract/extractall: names were checked against a flat exact set.
                data = zipped.read(item)
                release.require(len(data) == item.file_size, 'Artifact member size mismatch.')
                (stage / item.filename).write_bytes(data)
        release.require((stage / 'SOURCE.json').read_bytes() == release.canonical(source),
                        'Downloaded source differs from the resolver.')
        stage.rename(destination)
    finally:
        if stage.exists():
            shutil.rmtree(stage)


def validate_metadata(metadata, artifact_id, run_id, name, expected_digest):
    release.require(isinstance(metadata, dict) and metadata.get('id') == artifact_id
                    and metadata.get('workflow_run', {}).get('id') == run_id
                    and metadata.get('name') == name and metadata.get('expired') is False
                    and metadata.get('digest') == 'sha256:' + expected_digest,
                    'GitHub artifact identity or digest differs from the packaging job.')
    size = metadata.get('size_in_bytes')
    release.require(type(size) is int and 0 < size <= MAX_DOWNLOAD, 'Invalid GitHub artifact size.')


def fetch(repo, artifact_id, run_id, name, expected_digest, source, directory, evidence):
    release.require(re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repo), 'Invalid GitHub repository.')
    release.require(type(artifact_id) is int and artifact_id > 0 and type(run_id) is int and run_id > 0,
                    'Artifact and workflow run IDs must be positive integers.')
    release.require(release.HASH.fullmatch(expected_digest), 'Expected artifact SHA256 is required.')
    token = os.environ.get('GH_TOKEN') or os.environ.get('GITHUB_TOKEN')
    release.require(token, 'A read-only GitHub token is required.')
    directory, evidence = Path(directory).resolve(), Path(evidence).resolve()
    release.require(evidence != directory and not evidence.is_relative_to(directory),
                    'Download evidence must be outside the strict bundle directory.')
    release.require(not evidence.exists(), 'Download evidence directory must be new.')
    evidence.mkdir(parents=True)
    api = f'https://api.github.com/repos/{repo}/actions/artifacts/{artifact_id}'
    headers = {'Authorization': 'Bearer ' + token, 'Accept': 'application/vnd.github+json',
               'X-GitHub-Api-Version': '2022-11-28', 'User-Agent': 'cutmy-release-verifier'}
    opener = urllib.request.build_opener(NoRedirect)
    with opener.open(urllib.request.Request(api, headers=headers), timeout=60) as response:
        metadata = json.loads(bounded_read(response, 2 * 1024 * 1024))
    validate_metadata(metadata, artifact_id, run_id, name, expected_digest)
    try:
        response = opener.open(urllib.request.Request(api + '/zip', headers=headers), timeout=60)
    except urllib.error.HTTPError as error:
        release.require(error.code == 302, 'GitHub artifact download did not return its expected redirect.')
        location = error.headers.get('Location', '')
        parsed = urllib.parse.urlsplit(location)
        release.require(parsed.scheme == 'https' and parsed.hostname and not parsed.username and not parsed.password,
                        'Artifact redirect must be an HTTPS URL without user information.')
        error.close()
        # Signed blob URLs are fetched with no GitHub Authorization header.
        response = urllib.request.urlopen(location, timeout=60)
    with response:
        raw = bounded_read(response, MAX_DOWNLOAD)
    release.require(len(raw) == metadata['size_in_bytes'] and hashlib.sha256(raw).hexdigest() == expected_digest,
                    'Downloaded artifact ZIP differs from its expected size or SHA256.')
    archive = evidence / 'artifact.zip'
    archive.write_bytes(raw)
    receipt = {'schema_version': 1, 'repository': repo, 'run_id': run_id, 'artifact_id': artifact_id,
               'artifact_name': name, 'artifact_sha256': expected_digest, 'artifact_bytes': len(raw),
               'source_commit': source['commit'], 'source_sha256': release.digest(release.canonical(source))}
    (evidence / 'download.json').write_bytes(release.canonical(receipt))
    extract_bundle(archive, directory, source)
    return receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--repo', required=True)
    parser.add_argument('--artifact-id', required=True, type=int)
    parser.add_argument('--run-id', required=True, type=int)
    parser.add_argument('--artifact-name', required=True)
    parser.add_argument('--expected-artifact-sha256', required=True)
    parser.add_argument('--source', required=True, type=Path)
    parser.add_argument('--expected-source-sha256', required=True)
    parser.add_argument('--directory', required=True, type=Path)
    parser.add_argument('--evidence-dir', required=True, type=Path)
    args = parser.parse_args()
    source = release.load_source(args.source, expected_sha256=args.expected_source_sha256)
    receipt = fetch(args.repo, args.artifact_id, args.run_id, args.artifact_name,
                    args.expected_artifact_sha256, source, args.directory, args.evidence_dir)
    print(json.dumps(receipt, sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, urllib.error.URLError, zipfile.BadZipFile, struct.error) as error:
        # Do not expose signed download URLs, tokens or HTTP request headers.
        print('Artifact download verification failed: ' + type(error).__name__, file=__import__('sys').stderr)
        raise SystemExit(1)
