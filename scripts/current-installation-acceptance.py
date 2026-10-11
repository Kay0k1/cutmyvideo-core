#!/usr/bin/env python3
"""Current 005 installation/upgrade controls; historical transition pins stay fixed.

The predecessor is immutable. The candidate is the exact hosted checkout and
its complete application inventory, never an earlier run's acceptance receipt.
Importing this module performs no host operations.
"""
from __future__ import annotations

from copy import deepcopy
import hashlib
from pathlib import Path
import re

MODE = 'current-installation'
BASELINE = '1d684b4b0918138190439fc4ce223ff1bc22de40'
BASELINE_TREE = 'c568f55433cd2be92e9e5dffc956e1f1dca6cadb'
BASELINE_APPLICATION_SHA256 = 'd1ae273e0d56212ef549f029970cd4ea1f33276faa7e4e11c88127e3aeeacef6'
BASELINE_FILES = 173


def groups(original):
    result = dict(original)
    result['identity'] = tuple('current_application_sources_bound' if name == 'application_sources_unchanged'
                               else name for name in original['identity'])
    return result


def application(source):
    return {name: value for name, value in source['files'].items()
            if name.startswith(('cmd/', 'internal/', 'pkg/')) or name in ('go.mod', 'go.sum')}


def schema(inventory):
    return {name: value for name, value in inventory.items()
            if name.startswith('internal/app/migrations/')}


def source_tree(files):
    """Reconstruct Git's root tree from the complete SOURCE blob inventory."""
    if not isinstance(files, dict) or not 0 < len(files) <= 4096:
        raise RuntimeError('complete source inventory missing or oversized')
    root = {}
    for name, item in files.items():
        if not isinstance(name, str) or not isinstance(item, dict):
            raise RuntimeError('invalid source inventory entry')
        parts = name.split('/')
        if (len(parts) > 64 or any(not part or part in ('.', '..') or '\0' in part for part in parts)
                or item.get('mode') not in ('100644', '100755')
                or not re.fullmatch('[0-9a-f]{40}', item.get('git_blob', ''))
                or not re.fullmatch('[0-9a-f]{64}', item.get('sha256', ''))
                or type(item.get('bytes')) is not int or item['bytes'] < 0):
            raise RuntimeError('invalid source inventory entry')
        node = root
        for part in parts[:-1]:
            node = node.setdefault(part, {})
            if not isinstance(node, dict):
                raise RuntimeError('source file/directory collision')
        if parts[-1] in node:
            raise RuntimeError('source file/directory collision')
        node[parts[-1]] = (item['mode'], item['git_blob'])

    def tree(node):
        entries = []
        for name, item in node.items():
            if isinstance(item, dict):
                mode, blob, sort_name = '40000', tree(item), name.encode() + b'/'
            else:
                mode, blob = item
                sort_name = name.encode()
            entries.append((sort_name, mode.encode() + b' ' + name.encode() + b'\0' + bytes.fromhex(blob)))
        raw = b''.join(value for _, value in sorted(entries))
        return hashlib.sha1(b'tree ' + str(len(raw)).encode() + b'\0' + raw).hexdigest()
    return tree(root)


def validate_current(report, directory):
    # Receipt mechanics and immutable Git inventory are shared with the pinned
    # transition verifier; its reviewed identities and evidence are untouched.
    from importlib import util
    spec = util.spec_from_file_location('current_source_receipts', Path(__file__).with_name('source-retention-transition.py'))
    proof = util.module_from_spec(spec)
    spec.loader.exec_module(proof)
    require = proof.require
    value = report.get('current_installation', {})
    require(report['cleanup'].get('private_state_backup_config_keys_removed') is True
            and report['cleanup'].get('private_workspace_retained') is False,
            'current installation private state cleanup incomplete')
    require(set(value) == {'baseline', 'admission', 'old_source', 'current_source'}, 'current installation source binding missing')
    require(value['baseline'] == {'commit': BASELINE, 'tree': BASELINE_TREE,
            'application_files': BASELINE_FILES, 'application_sha256': BASELINE_APPLICATION_SHA256},
            'current installation predecessor identity changed')
    admission = value['admission']
    require(set(admission) == {'provider', 'checkout', 'baseline_commit', 'baseline_tree',
            'candidate_commit', 'candidate_tree', 'candidate_application_files', 'candidate_application_sha256',
            'schema_sources_unchanged', 'captured_before_image_builds'}, 'current source admission fields differ')
    require(admission['provider'] == report['provider'] and admission['checkout'] == report['checkout']
            and admission['baseline_commit'] == BASELINE and admission['baseline_tree'] == BASELINE_TREE
            and admission['candidate_commit'] == report['expected_checkout']
            and admission['candidate_tree'] == report['checkout']['tree']
            and admission['schema_sources_unchanged'] is True and admission['captured_before_image_builds'] is True,
            'current source admission context differs')
    inventories = {}
    for label, receipt in (('old', value['old_source']), ('current', value['current_source'])):
        raw = proof.artifact(directory, receipt)
        source = proof.strict_json(raw)
        image = report['images'][label]
        require(source['commit'] == image['source_commit'] and source['tree'] == image['tree']
                and source_tree(source['files']) == source['tree']
                and all(source[key] == image[key] for key in ('version', 'build_time', 'toolchain'))
                and proof.digest(raw) == image['source_sha256'], 'current installation actual SOURCE identity differs')
        inventories[label] = application(source)
    old, current = inventories['old'], inventories['current']
    require(len(old) == BASELINE_FILES and proof.digest(proof.canonical(old)) == BASELINE_APPLICATION_SHA256,
            'current installation predecessor source changed')
    require(type(admission['candidate_application_files']) is int
            and 0 < admission['candidate_application_files'] <= 4096
            and len(current) == admission['candidate_application_files']
            and proof.digest(proof.canonical(current)) == admission['candidate_application_sha256'],
            'current application differs from admitted checkout inventory')
    require(schema(old) == schema(current), 'current installation requires an unchanged schema; use a reviewed transition mode')
    require(report.get('runtime_application_sources_unchanged') is (old == current)
            and report['groups']['backup_restore']['checks']['actual_same_project_image_upgrade']['evidence']
            .get('application_code_unchanged') is (old == current),
            'actual image upgrade application equality claim differs')
    item = report['groups']['identity']['checks']['current_application_sources_bound'].get('evidence', {})
    require(item == {'baseline_commit': BASELINE, 'candidate_commit': admission['candidate_commit'],
            'candidate_application_files': len(current), 'candidate_application_sha256': admission['candidate_application_sha256'],
            'schema_changing_upgrade': False}, 'current application check differs from raw source binding')


def acceptance_class(base):
    proof = base.transition_module()

    class CurrentInstallation(base.Acceptance):
        upgrade_from = BASELINE

        def __init__(self, args):
            proof.require(getattr(args, 'mode', None) == MODE, 'explicit current installation mode required')
            super().__init__(args)
            self.required_groups = groups(base.GROUPS)
            self.report.update(mode=MODE, schema_version=3)
            self.report['current_installation'] = {'baseline': {
                'commit': BASELINE, 'tree': BASELINE_TREE, 'application_files': BASELINE_FILES,
                'application_sha256': BASELINE_APPLICATION_SHA256}}

        def initialize_host(self):
            proof.require(self.git(base.REPO, 'rev-parse', BASELINE + '^{tree}') == BASELINE_TREE,
                          'current installation predecessor tree differs')
            self.admitted_old = proof.application_inventory(self.run, base.REPO, BASELINE)
            self.admitted_current = proof.application_inventory(self.run, base.REPO, self.args.revision)
            proof.require(len(self.admitted_old) == BASELINE_FILES
                          and proof.digest(proof.canonical(self.admitted_old)) == BASELINE_APPLICATION_SHA256,
                          'current installation predecessor application differs')
            proof.require(0 < len(self.admitted_current) <= 4096
                          and schema(self.admitted_old) == schema(self.admitted_current),
                          'current installation requires unchanged schema sources')
            super().initialize_host()
            self.report['current_installation']['admission'] = {
                'provider': deepcopy(self.report['provider']), 'checkout': deepcopy(self.report['checkout']),
                'baseline_commit': BASELINE, 'baseline_tree': BASELINE_TREE,
                'candidate_commit': self.args.revision, 'candidate_tree': self.report['checkout']['tree'],
                'candidate_application_files': len(self.admitted_current),
                'candidate_application_sha256': proof.digest(proof.canonical(self.admitted_current)),
                'schema_sources_unchanged': True, 'captured_before_image_builds': True}
            self.save()

        def application_identity(self):
            proof.require(application(self.old_source) == self.admitted_old
                          and application(self.current_source) == self.admitted_current,
                          'built current installation application differs from admitted source')
            for label, project in (('old', self.old), ('current', self.current)):
                raw = (project['state'] / 'SOURCE.json').read_bytes()
                name = 'current-' + label + '-source.json'
                path = self.output / name
                proof.require(not path.exists(), 'frozen current SOURCE evidence cannot be overwritten')
                path.write_bytes(raw)
                self.report['current_installation'][label + '_source'] = {
                    'path': name, 'bytes': len(raw), 'sha256': proof.digest(raw)}
            admission = self.report['current_installation']['admission']
            self.report['runtime_application_sources_unchanged'] = self.admitted_old == self.admitted_current
            self.check('current_application_sources_bound', {
                'baseline_commit': BASELINE, 'candidate_commit': self.args.revision,
                'candidate_application_files': admission['candidate_application_files'],
                'candidate_application_sha256': admission['candidate_application_sha256'],
                'schema_changing_upgrade': False})

    return CurrentInstallation
