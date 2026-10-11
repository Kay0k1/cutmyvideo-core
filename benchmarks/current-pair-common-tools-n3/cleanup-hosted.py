#!/usr/bin/env python3
"""Always reconcile this one pair's journalled ownership; never daemon-wide prune."""
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import sys

HERE = Path(__file__).resolve().parent


def main():
    if os.geteuid() != 0 or os.environ.get('GITHUB_ACTIONS') != 'true':
        raise RuntimeError('Only root cleanup on the disposable hosted job')
    root = Path(os.environ['RUNNER_TEMP'])
    if not root.is_absolute() or root.resolve() != root or not root.is_dir():
        raise RuntimeError('Existing actual RUNNER_TEMP required')
    raw = root / 'current-pair-hosted'
    if not raw.is_dir() or raw.is_symlink(): raise RuntimeError('Existing owned evidence root required')
    spec = importlib.util.spec_from_file_location('pair_preparation', HERE / 'prepare-common-tools.py')
    prep = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(prep)
    m = prep.load_driver()
    prepared = root / 'current-pair-prepared'
    evidence = m.Evidence(raw / 'cleanup')
    commands = m.Commands(evidence)
    owner_path = raw / 'pair-owner.json'
    failures = []
    resources_removed = False
    try:
        # Export failures are sticky, but cannot bypass journalled resource cleanup.
        try:
            # Only preparation's direct evidence files; context/tools stay private.
            if prepared.exists():
                if prepared.resolve() != prepared or not prepared.is_dir(): raise RuntimeError('Prepared root ownership path changed')
                exported = raw / 'preparation'
                exported.mkdir(mode=0o755)
                for path in sorted(prepared.iterdir()):
                    if path.is_file() and not path.is_symlink() and (path.suffix in ('.json', '.jsonl', '.stdout', '.stderr') or path.name == 'image-id.txt'):
                        if path.stat().st_size > 8 << 20: raise RuntimeError('Preparation evidence exceeds file cap')
                        shutil.copyfile(path, exported / path.name)
                        (exported / path.name).chmod(0o644)
        except BaseException as error:
            failures.append(type(error).__name__ + ': ' + str(error))
        try:
            shutil.copyfile(HERE / 'ATTRIBUTION.md', raw / 'ATTRIBUTION.md')
        except BaseException as error:
            failures.append(type(error).__name__ + ': ' + str(error))
        if owner_path.exists():
            owner = m.read_json(owner_path)
            m.require(set(owner) == {'token', 'work'} and re.fullmatch('cutmy-capacity-[0-9a-f]{16}', owner['token']), 'Pair ownership journal differs')
            work = Path(owner['work'])
            scratch = root / 'current-pair-scratch'
            m.require(work.is_absolute() and work.resolve() == work and work.parent == scratch
                      and work.name.startswith(owner['token'] + '-'), 'Pair private work differs from journal')
            docker = m.Docker(commands, '/usr/bin/docker', owner['token'])
            docker.cleanup()
            if work.exists(): shutil.rmtree(work)
        receipt = prepared / 'runtime-image-receipt.json'
        if prepared.exists():
            m.require(prepared.resolve() == prepared and prepared.is_dir(), 'Prepared root ownership path changed')
        if receipt.exists():
            value = m.read_json(receipt)
            token = value['token']
            m.require(re.fullmatch('cutmy-common-tools-[0-9a-f]{16}', token), 'Preparation ownership journal differs')
            docker = m.Docker(commands, '/usr/bin/docker', token)
            docker.cleanup()
            images = docker.run('image', 'ls', '--no-trunc', '--quiet', '--filter', f'label={m.LABEL}={token}', timeout=15).decode().splitlines()
            m.require(len(set(images)) <= 1, 'Unexpected multiple owned prepared images')
            for image in set(images):
                facts = prep.image_facts(docker, image)
                m.require(facts['Id'] == image and facts['Labels'].get(m.LABEL) == token, 'Prepared image cleanup ownership differs')
                docker.run('image', 'rm', image, timeout=30)
        # Remove private staging/context only after all journalled resources are gone.
        for name in ('current-pair-inputs', 'current-pair-prepared', 'current-pair-scratch'):
            path = root / name
            if path.exists():
                m.require(path.resolve() == path and path.is_dir() and path.stat().st_uid == 0,
                          'Private cleanup root differs')
                shutil.rmtree(path)
        resources_removed = True
    except BaseException as error:
        failures.append(type(error).__name__ + ': ' + str(error))
    finally:
        # Raw evidence was selected above; private env/database/context are never exported.
        try:
            for directory in [raw, *[p for p in raw.rglob('*') if p.is_dir() and not p.is_symlink()]]:
                directory.chmod(0o755)
            for path in raw.rglob('*'):
                if path.is_file() and not path.is_symlink(): path.chmod(0o644)
        except BaseException as error:
            failures.append(type(error).__name__ + ': ' + str(error))
        result = raw / 'cleanup-result.json'
        result.write_bytes(m.canonical({'ok': not failures, 'error': failures[0] if failures else None,
            'errors': failures, 'owned_resources_removed': resources_removed,
            'scope': 'journalled pair/preparation only; unresolved backend bounded by disposable job termination'}))
        result.chmod(0o644)
    m.require(not failures, 'Hosted cleanup failed: ' + (failures[0] if failures else ''))


if __name__ == '__main__': main()
