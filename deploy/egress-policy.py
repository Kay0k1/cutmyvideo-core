#!/usr/bin/env python3
"""Owned Linux IPv4 Docker policy; plan is inert, apply requires stopped writers.

Never flush a host chain. Docker's iptables (legacy or nft compatibility) backend
must expose DOCKER-USER and rules for the exact owned processing bridge. Native
nftables, rootless/userns, IPv6 and foreign network members are refused.
"""
from __future__ import annotations

import argparse
import contextlib
import fcntl
import json
import os
from pathlib import Path
import re
import shlex
import stat
import sys

# This entrypoint is also executed from a hash-checked immutable snapshot.
sys.dont_write_bytecode = True

from public_common import (PROFILE, Refused, bridge_name, canonical, digest, json_command,
                           local_engine, parse_config, private_read, require, run)
from public_common import project_lock

# Public HTTPS only. Include the non-public IPv4 special-use/transition ranges;
# loopback relays and Docker's embedded DNS stay inside each network namespace.
DENIED = ('0.0.0.0/8', '10.0.0.0/8', '100.64.0.0/10', '127.0.0.0/8', '169.254.0.0/16',
          '172.16.0.0/12', '192.0.0.0/24', '192.0.2.0/24', '192.88.99.0/24',
          '192.168.0.0/16', '198.18.0.0/15', '198.51.100.0/24', '203.0.113.0/24',
          '224.0.0.0/4', '240.0.0.0/4')


def policy(config):
    suffix = digest(config['CUTMY_PROJECT'].encode())[:10]
    forward, database, host = 'CMF_' + suffix, 'CMD_' + suffix, 'CMH_' + suffix
    bridge = bridge_name(config)
    database_bridge = bridge + '_d'
    comment = 'cutmy:' + config['CUTMY_PROJECT'] + ':' + PROFILE
    chains = {forward: [], database: [], host: []}
    for chain in chains:
        chains[chain].append(['-m', 'conntrack', '--ctstate', 'ESTABLISHED,RELATED', '-j', 'RETURN'])
    # Host-local services (including metadata on the host) bypass FORWARD and
    # must also be blocked in INPUT. Stateful replies to host Caddy remain valid.
    chains[host].append(['-j', 'DROP'])
    for last in (10, 11, 12, 13):
        chains[database].append(['-s', config['CUTMY_DATABASE_PREFIX'] + '.' + str(last) + '/32',
                                 '-d', config['CUTMY_DATABASE_PREFIX'] + '.2/32',
                                 '-p', 'tcp', '-m', 'tcp', '--dport', '5432', '-j', 'RETURN'])
    chains[database].append(['-j', 'DROP'])
    for network in DENIED:
        chains[forward].append(['-d', network, '-j', 'DROP'])
    for last in (10, 11):
        chains[forward].append(['-s', config['CUTMY_PROCESSING_PREFIX'] + '.' + str(last) + '/32',
                                '-p', 'tcp', '-m', 'tcp', '--dport', '443', '-j', 'RETURN'])
    chains[forward].append(['-j', 'DROP'])
    hooks = {'DOCKER-USER': [['-i', bridge, '-m', 'comment', '--comment', comment, '-j', forward],
                              ['-i', database_bridge, '-m', 'comment', '--comment', comment, '-j', database]],
             'INPUT': [['-i', bridge, '-m', 'comment', '--comment', comment, '-j', host],
                       ['-i', database_bridge, '-m', 'comment', '--comment', comment, '-j', host]]}
    return {'profile': PROFILE, 'project': config['CUTMY_PROJECT'], 'bridge': bridge,
            'database_bridge': database_bridge,
            'ipv6': 'disabled-required', 'chains': chains, 'hooks': hooks}


def parsed_rules(data):
    chains, rules = set(), {}
    for line in data.decode().splitlines():
        if line.startswith(':'):
            chains.add(line[1:].split()[0])
        elif line.startswith('-A '):
            words = shlex.split(line)
            rules.setdefault(words[1], []).append(words[2:])
    return chains, rules


def validate_backend(data, desired, version):
    require(re.fullmatch(r'iptables v[0-9.]+ \((?:nf_tables|legacy)\)\s*', version),
            'unsupported_iptables_backend')
    chains, rules = parsed_rules(data)
    require({'FORWARD', 'DOCKER-USER', 'INPUT'} <= chains, 'docker_iptables_filter_chains_missing')
    # Existence of the owned chains is insufficient if an earlier FORWARD
    # accept or a missing Docker dispatch bypasses every scoped forward hook.
    require(rules.get('FORWARD', [])[:1] == [['-j', 'DOCKER-USER']]
            and sum('-j' in row and row[row.index('-j') + 1] == 'DOCKER-USER'
                    for row in rules.get('FORWARD', [])) == 1,
            'docker_user_dispatch_missing_or_shadowed')
    require(all(any(bridge in rule for chain, entries in rules.items()
                    if chain.startswith('DOCKER') and chain != 'DOCKER-USER' for rule in entries)
                for bridge in (desired['bridge'], desired['database_bridge'])),
            'docker_backend_does_not_manage_owned_bridge')
    return chains, rules


def validate_networks(config, networks, containers, *, stopped=False):
    project = config['CUTMY_PROJECT']
    require(len(networks) == 2, 'owned_networks_missing')
    for container in containers.values():
        role = container.get('Config', {}).get('Labels', {}).get('com.docker.compose.service')
        labels = container.get('Config', {}).get('Labels', {})
        require(labels.get('com.docker.compose.project') == project
                and role in ('postgres', 'api', 'worker', 'migrate', 'maintenance'), 'foreign_project_container')
        expected = {project + '_database'}
        if role in ('api', 'worker'):
            expected.add(project + '_processing')
        attached = container.get('NetworkSettings', {}).get('Networks', {})
        mode = container.get('HostConfig', {}).get('NetworkMode')
        # A third bridge, host network or shared container namespace would
        # originate traffic outside both exact -i hooks.
        network_ids = {network.get('Id') for network in networks if network.get('Name') in expected}
        require(set(attached) == expected and (mode in expected or mode in network_ids - {None}),
                'container_has_unsupported_network_namespace')
        require(not any(endpoint.get('GlobalIPv6Address') for endpoint in attached.values()),
                'container_ipv6_address_present')
        sysctls = container.get('HostConfig', {}).get('Sysctls', {})
        require(all(sysctls.get('net.ipv6.conf.' + name + '.disable_ipv6') == '1'
                    for name in ('all', 'default')), 'container_ipv6_not_disabled')
        if stopped and role != 'postgres':
            require(container.get('State', {}).get('Running') is False,
                    'stop_application_writers_before_policy_change')
    for kind, prefix, internal in (('database', config['CUTMY_DATABASE_PREFIX'], True),
                                    ('processing', config['CUTMY_PROCESSING_PREFIX'], False)):
        matches = [n for n in networks if n.get('Name') == project + '_' + kind]
        require(len(matches) == 1, 'owned_network_identity_invalid')
        network = matches[0]
        require(network.get('Driver') == 'bridge' and network.get('Scope') == 'local'
                and network.get('Internal') is internal and network.get('EnableIPv6') is False
                and network.get('Labels', {}).get('video.cutmy.profile') == PROFILE
                and network.get('Labels', {}).get('video.cutmy.project') == project
                and network.get('Labels', {}).get('com.docker.compose.project') == project,
                'owned_network_topology_invalid')
        ranges = network.get('IPAM', {}).get('Config', [])
        require(len(ranges) == 1 and ranges[0].get('Subnet') == prefix + '.0/24'
                and not any(':' in value for value in ranges[0].values() if isinstance(value, str)),
                'owned_network_addressing_invalid')
        require(network.get('Options', {}).get('com.docker.network.bridge.name') ==
                bridge_name(config) + ('_d' if kind == 'database' else ''), 'owned_bridge_name_invalid')
        for cid, member in network.get('Containers', {}).items():
            require(cid in containers and not member.get('IPv6Address'), 'foreign_or_ipv6_network_member')
            c = containers[cid]
            labels = c.get('Config', {}).get('Labels', {})
            role = labels.get('com.docker.compose.service')
            allowed = {'api': 10, 'worker': 11} if kind == 'processing' else {
                'postgres': 2, 'api': 10, 'worker': 11, 'migrate': 12, 'maintenance': 13}
            require(labels.get('com.docker.compose.project') == project and role in allowed
                    and member.get('IPv4Address') == prefix + '.' + str(allowed[role]) + '/24',
                    'foreign_or_misaddressed_network_member')
            sysctls = c.get('HostConfig', {}).get('Sysctls', {})
            require(all(sysctls.get('net.ipv6.conf.' + name + '.disable_ipv6') == '1'
                        for name in ('all', 'default')), 'container_ipv6_not_disabled')
            if stopped and role != 'postgres':
                require(c.get('State', {}).get('Running') is False, 'stop_application_writers_before_policy_change')


def disjoint_project_hook(parent, row, desired):
    # Two retained/fresh projects can coexist. Skip only the exact generated
    # hook of another public-v1 project on a different, literal interface.
    # Arbitrary earlier ACCEPT/RETURN, matches, negation and wildcards remain
    # refusal conditions, even when a human thinks they are harmless.
    if len(row) != 8 or row[0] != '-i' or row[2:4] != ['-m', 'comment'] or row[4] != '--comment' or row[6] != '-j':
        return False
    match = re.fullmatch(r'cutmy:(cutmy_[a-z0-9][a-z0-9_]{0,24}):public-v1', row[5])
    if not match or match[1] == desired['project']:
        return False
    suffix = digest(match[1].encode())[:10]
    bridge = 'cm_' + suffix
    if row[1] in (desired['bridge'], desired['database_bridge']):
        return False
    targets = {bridge: 'CMF_' + suffix, bridge + '_d': 'CMD_' + suffix}
    if parent == 'INPUT':
        targets = {name: 'CMH_' + suffix for name in targets}
    return row[1] in targets and row[7] == targets[row[1]]


def require_exact_policy(data, desired):
    _, rules = parsed_rules(data)
    for chain, expected in desired['chains'].items():
        require(rules.get(chain) == expected, 'owned_policy_missing_or_modified')
    for chain, hooks in desired['hooks'].items():
        relevant = [row for row in rules.get(chain, []) if not disjoint_project_hook(chain, row, desired)]
        require(relevant[:len(hooks)] == hooks
                and all(sum(rule == hook for rule in rules.get(chain, [])) == 1 for hook in hooks),
                'owned_policy_hook_missing_duplicate_or_shadowed')
    for chain, rows in rules.items():
        for row in rows:
            if '-j' in row and row[row.index('-j') + 1] in desired['chains']:
                require(chain in desired['hooks'] and row in desired['hooks'][chain], 'foreign_owned_chain_reference')


def restore_script(desired, *, remove=False):
    lines = ['*filter']
    if remove:
        lines += ['-D ' + chain + ' ' + shlex.join(hook) for chain, hooks in desired['hooks'].items() for hook in hooks]
        for chain in desired['chains']:
            lines.extend(['-F ' + chain, '-X ' + chain])
    else:
        lines += [':' + chain + ' - [0:0]' for chain in desired['chains']]
        for chain, rules in desired['chains'].items():
            lines += ['-A ' + chain + ' ' + shlex.join(rule) for rule in rules]
        lines += ['-I ' + chain + ' ' + str(index) + ' ' + shlex.join(hook)
                  for chain, hooks in desired['hooks'].items() for index, hook in enumerate(hooks, 1)]
    return ('\n'.join(lines + ['COMMIT', ''])).encode()


def inspect_networks(config, stopped):
    networks = json_command(['docker', 'network', 'inspect', config['CUTMY_PROJECT'] + '_database',
                             config['CUTMY_PROJECT'] + '_processing'])
    ids = run(['docker', 'ps', '-aq', '--filter', 'label=com.docker.compose.project=' + config['CUTMY_PROJECT']]).decode().split()
    require(all(re.fullmatch(r'[0-9a-f]{12,64}', cid) for cid in ids), 'container_identity_invalid')
    inspected = json_command(['docker', 'inspect', *ids]) if ids else []
    containers = {c['Id']: c for c in inspected}
    for container in inspected:
        role = container.get('Config', {}).get('Labels', {}).get('com.docker.compose.service')
        require(role in ('postgres', 'api', 'worker', 'migrate', 'maintenance'), 'foreign_project_container')
        if stopped and role != 'postgres':
            require(container.get('State', {}).get('Running') is False,
                    'stop_application_writers_before_policy_change')
    validate_networks(config, networks, containers, stopped=stopped)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('command', choices=('plan', 'verify', 'apply', 'remove'))
    args = parser.parse_args()
    config = parse_config(private_read(args.config, 8192))
    desired = policy(config)
    if args.command == 'plan':
        print(canonical(desired).decode(), end='')
        return
    local_engine()
    require(os.geteuid() == 0, 'policy_administrator_required')
    require(Path('/proc/sys/net/bridge/bridge-nf-call-iptables').read_text().strip() == '1',
            'bridge_netfilter_must_be_enabled')
    lock = Path('/run/' + next(iter(desired['chains'])) + '.lock')
    project = project_lock(config) if args.command in ('apply', 'remove') else contextlib.nullcontext()
    project.__enter__()
    fd = None
    try:
        fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        info = os.fstat(fd)
        require(stat.S_ISREG(info.st_mode) and info.st_uid == 0 and info.st_mode & 0o077 == 0, 'policy_lock_invalid')
        fcntl.flock(fd, fcntl.LOCK_EX)
        inspect_networks(config, args.command != 'verify')
        data = run(['iptables-save', '-t', 'filter'])
        existing, rules = validate_backend(data, desired, run(['iptables', '--version']).decode())
        if args.command == 'verify':
            require_exact_policy(data, desired)
        elif args.command == 'remove':
            require_exact_policy(data, desired)
            run(['iptables-restore', '--wait', '5', '--noflush'], input=restore_script(desired, remove=True))
        elif set(desired['chains']) & existing:
            require_exact_policy(data, desired)  # Idempotent; never overwrite an unknown/modified chain.
        else:
            require(not any('-j' in row and row[row.index('-j') + 1] in desired['chains']
                            for rows in rules.values() for row in rows), 'unexpected_owned_chain_reference')
            run(['iptables-restore', '--wait', '5', '--noflush'], input=restore_script(desired))
        if args.command != 'remove':
            inspect_networks(config, False)
            require_exact_policy(run(['iptables-save', '-t', 'filter']), desired)
        print('Owned policy ' + args.command + ' complete: ' + config['CUTMY_PROJECT'])
    finally:
        if fd is not None:
            os.close(fd)
        project.__exit__(None, None, None)


if __name__ == '__main__':
    try:
        main()
    except (Refused, OSError, ValueError, KeyError, TypeError):
        print('Public egress operation refused; check prerequisites and private configuration.', file=sys.stderr)
        raise SystemExit(1)
