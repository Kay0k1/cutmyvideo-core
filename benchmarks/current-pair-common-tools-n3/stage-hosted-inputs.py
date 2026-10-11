#!/usr/bin/env python3
"""One finite hosted input transport, four frozen downloads, no tool/media execution."""
import concurrent.futures
import gzip
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import tarfile
import time
import urllib.parse
import urllib.request
import zipfile

HERE = Path(__file__).resolve().parent
DATASET_URL = 'https://github.com/Kay0k1/cutmyvideo-core/releases/download/bench-inputs-main1080-n3-20261011/cutmyvideo-main1080-inputs-20261011.tar.gz'
DATASET_SHA = '6a56a257d5055b63fca6cdd6348bb280c0c7ec85ab15e89dc7d7da69d8d992ae'
CANDIDATE = 'cutmyvideo-core_v0.0.0-candidate.g3f0102d915b292401d7880bed3c34a8ac671d060_linux_amd64.tar.gz'
BASELINE = 'cutmyvideo-core_v0.2.1_linux_amd64.tar.gz'
ZIP_URL = 'https://api.github.com/repos/Kay0k1/cutmyvideo-core/actions/artifacts/11705968161/zip'
DOWNLOADS = (
    ('dataset.tar.gz', DATASET_URL, 25465863, DATASET_SHA, 32 << 20),
    ('tools/ffmpeg-master-latest-linux64-gpl.tar.xz', 'https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-linux64-gpl.tar.xz', 156191208, '0a8d7a93f8a53aacf09a81b6d31bfab790f28de5ea707a08c84d04c85eaec5fa', 192 << 20),
    ('baseline/' + BASELINE, 'https://github.com/Kay0k1/cutmyvideo-core/releases/download/v0.2.1/' + BASELINE, 4655111, '89e7832aaa29e25e6794a9d6e9e14eb9027549bdfc92270eecfd983aac1a8a21', 8 << 20),
    ('candidate.zip', ZIP_URL, 23358899, '18efc340a2885b81f1002b03e39e8c4a4d96eaf32c0e1222b85b7c8d967a5647', 32 << 20),
)


class PublicRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, url):
        parsed = urllib.parse.urlsplit(url)
        host = parsed.hostname or ''
        azure = re.fullmatch(r'productionresultssa[0-9]+\.blob\.core\.windows\.net', host)
        if parsed.scheme != 'https' or parsed.port not in (None, 443) or parsed.username or parsed.password or not (host in ('github.com', 'api.github.com', 'release-assets.githubusercontent.com', 'objects.githubusercontent.com') or azure):
            raise RuntimeError('Unexpected official transport redirect host')
        redirected = super().redirect_request(request, fp, code, message, headers, url)
        if redirected is not None and host != 'api.github.com':
            redirected.remove_header('Authorization')
        return redirected


def digest(path):
    value = hashlib.sha256()
    with path.open('rb') as source:
        while block := source.read(1 << 20): value.update(block)
    return value.hexdigest()


def fetch(root, item):
    relative, url, expected_bytes, expected_sha, cap = item
    target = root / relative
    target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    request = urllib.request.Request(url, headers={'User-Agent': 'cutmyvideo-frozen-benchmark-inputs', 'Accept': 'application/octet-stream'})
    if url == ZIP_URL:
        token = os.environ.get('CUTMY_ARTIFACT_TOKEN')
        if not token: raise RuntimeError('Existing Actions artifact read token required')
        request.add_header('Authorization', 'Bearer ' + token)
    start = time.monotonic()
    sha = hashlib.sha256()
    total = 0
    try:
        with urllib.request.build_opener(PublicRedirects()).open(request, timeout=10) as response, target.open('xb') as out:
            target.chmod(0o600)
            if response.status != 200: raise RuntimeError('Transport HTTP status differs')
            while True:
                if time.monotonic() - start >= 180: raise RuntimeError('Transport180s deadline')
                block = response.read(1 << 20)
                if not block: break
                total += len(block)
                if total > cap or total > expected_bytes: raise RuntimeError('Transport compressed size cap')
                out.write(block)
                sha.update(block)
        if total != expected_bytes or sha.hexdigest() != expected_sha:
            raise RuntimeError('First exact transport size/SHA mismatch; no retry/substitution')
        return {'path': relative, 'bytes': total, 'sha256': sha.hexdigest(), 'elapsed_seconds': time.monotonic() - start}
    except Exception as error:
        # Never emit credentials or temporary signed redirect URL/query in exceptions.
        raise RuntimeError(relative + ': ' + type(error).__name__ + '; original partial bytes retained, no retry') from None


def extract_dataset(root, dataset):
    start = time.monotonic()
    expanded = 0
    selected = []
    class Reader(io.RawIOBase):
        def readable(self): return True
        def read(self, size=-1):
            nonlocal expanded
            if time.monotonic() - start >= 60: raise RuntimeError('Dataset parser60s deadline')
            block = decoded.read(1 << 20 if size < 0 else min(size, 1 << 20))
            expanded += len(block)
            if expanded > 32 << 20: raise RuntimeError('Dataset expanded32MiB cap')
            return block
    with gzip.open(root / 'dataset.tar.gz', 'rb') as decoded:
        reader = Reader()
        with tarfile.open(fileobj=reader, mode='r|') as tar:
            for member in tar:
                if len(selected) >= 10 or member.name != list(dataset)[len(selected)] or not member.isfile() or member.pax_headers or member.mode != 0o644 or member.uid != 0 or member.gid != 0 or member.mtime != 0 or member.uname or member.gname:
                    raise RuntimeError('Unsafe/changed dataset regular member inventory')
                size, sha = dataset[member.name]
                if member.size != size: raise RuntimeError('Dataset member size differs')
                destination = root / member.name
                destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                actual = hashlib.sha256()
                with tar.extractfile(member) as source, destination.open('xb') as out:
                    destination.chmod(0o644)
                    while block := source.read(1 << 20):
                        out.write(block)
                        actual.update(block)
                if destination.stat().st_size != size or actual.hexdigest() != sha:
                    raise RuntimeError('Dataset member SHA differs')
                selected.append(member.name)
        while reader.read(1 << 20): pass  # Verify gzip CRC and complete compressed EOF.
    if selected != list(dataset): raise RuntimeError('Incomplete dataset')
    return {'members': selected, 'expanded_bytes': expanded, 'gzip_crc_and_eof_verified': True}


def extract_candidate(root):
    with zipfile.ZipFile(root / 'candidate.zip') as archive:
        members = archive.infolist()
        if len(members) != 8 or sum(member.file_size for member in members) != 23532242:
            raise RuntimeError('Frozen candidate ZIP inventory differs')
        seen = set()
        for member in members:
            path = PurePosixPath(member.filename)
            mode = member.external_attr >> 16
            if member.filename in seen or path.is_absolute() or len(path.parts) != 1 or path.name in ('.', '..') or member.flag_bits & 1 or member.is_dir() or stat.S_ISLNK(mode) or (stat.S_IFMT(mode) not in (0, stat.S_IFREG)):
                raise RuntimeError('Unsafe/duplicate candidate ZIP member')
            seen.add(member.filename)
        member = archive.getinfo(CANDIDATE)
        if member.file_size != 4796560: raise RuntimeError('Candidate archive size differs')
        target = root / 'candidate' / CANDIDATE
        target.parent.mkdir(mode=0o700)
        with archive.open(member) as source, target.open('xb') as out:
            total = 0
            while block := source.read(1 << 20):
                total += len(block)
                if total > 8 << 20: raise RuntimeError('Candidate archive extraction cap')
                out.write(block)
        target.chmod(0o600)
        if digest(target) != '90cfa595b2dd650db10ceea27fd4339a6162935081881de058792462081d0ae2':
            raise RuntimeError('Candidate archive SHA differs')


def main():
    if os.geteuid() != 0 or os.environ.get('GITHUB_ACTIONS') != 'true':
        raise RuntimeError('Only root staging on admitted disposable GitHub host')
    if DATASET_URL != 'https://github.com/Kay0k1/cutmyvideo-core/releases/download/bench-inputs-main1080-n3-20261011/cutmyvideo-main1080-inputs-20261011.tar.gz':
        raise RuntimeError('Public dataset URL differs from the ROOT-frozen transport')
    runner_temp = Path(os.environ['RUNNER_TEMP'])
    if not runner_temp.is_absolute() or runner_temp.resolve() != runner_temp or not runner_temp.is_dir():
        raise RuntimeError('Actual existing RUNNER_TEMP required')
    root = runner_temp / 'current-pair-inputs'
    root.mkdir(mode=0o700)
    spec = importlib.util.spec_from_file_location('pair_preparation', HERE / 'prepare-common-tools.py')
    preparation = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(preparation)
    results = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        futures = [pool.submit(fetch, root, item) for item in DOWNLOADS]
        for future in futures: results.append(future.result())
    extracted = extract_dataset(root, preparation.DATASET)
    extract_candidate(root)
    driver = preparation.load_driver()
    template = driver.read_json(HERE / 'CONFIG.template.json', preparation.TEMPLATE_SHA)
    preparation.validate_inputs(driver, template, root)
    print(json.dumps({'ok': True, 'scope': 'frozen inputs only; no app/media/tool/Docker execution',
                      'input_root': str(root), 'downloads': results, 'dataset': extracted}))


if __name__ == '__main__': main()
