"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Local admin of claimed CPEs (scripts/acsd_cli.py). It writes the same
Redis keys acsd reads, so changes apply on the CPE's next Inform without
restarting acsd. Stage 3 replaces this with the Orc8r feed.
"""

import argparse
import ipaddress
import os
import subprocess
import sys
from typing import List, Optional

from magma.acsd.claims import ClaimError, ClaimRegistry, cpe_key_of
from magma.acsd.credentials import CredentialStore

# The realm only matters for creating credentials, which the CLI never does.
_ANY_REALM = ''


def _parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog='acsd_cli.py', description='Manage CPEs claimed by acsd.',
    )
    sub = p.add_subparsers(dest='command', required=True)

    add = sub.add_parser('claim-add', help='claim a CPE by its Inform DeviceId')
    add.add_argument('--oui', required=True)
    add.add_argument('--product-class', default='')
    add.add_argument('--serial', required=True)
    add.add_argument('--id', default='', help='claim id (default: generated)')
    add.add_argument('--network', default='')
    add.add_argument('--label', default='')

    rm = sub.add_parser(
        'claim-remove', help='drop a claim and its per-CPE credential',
    )
    rm.add_argument('claim_id')

    sub.add_parser('claim-list', help='list claims and credential state')

    reset = sub.add_parser(
        'reset-credentials',
        help='let a claimed CPE bootstrap again (e.g. after a factory reset)',
    )
    reset.add_argument('claim_id')

    cert = sub.add_parser(
        'dev-cert', help='write a self-signed cert/key for the WAN listener '
        '(development only)',
    )
    cert.add_argument('--out-dir', required=True)
    cert.add_argument('--cn', default='localhost')
    cert.add_argument('--days', type=int, default=365)
    return p


def run(argv: List[str], client=None, out=sys.stdout) -> int:
    args = _parser().parse_args(argv)
    if args.command == 'dev-cert':
        cert, key = make_dev_cert(args.out_dir, args.cn, args.days)
        print('wrote %s and %s' % (cert, key), file=out)
        return 0

    if client is None:
        from magma.common.redis.client import get_default_client
        client = get_default_client()
    claims = ClaimRegistry(client)
    creds = CredentialStore(client, _ANY_REALM)

    if args.command == 'claim-add':
        try:
            claim = claims.add(
                args.oui, args.product_class, args.serial, args.id,
                args.network, args.label,
            )
        except ClaimError as err:
            print('error: %s' % err, file=out)
            return 1
        print('claimed %s as %s (cpe_key %s)' % (
            '/'.join((claim.oui, claim.product_class, claim.serial)),
            claim.claim_id, claim.cpe_key,
        ), file=out)
    elif args.command == 'claim-remove':
        if claims.remove(args.claim_id) is None:
            print('error: no claim %s' % args.claim_id, file=out)
            return 1
        creds.reset(cpe_key_of(args.claim_id))
        print('removed claim %s' % args.claim_id, file=out)
    elif args.command == 'claim-list':
        print('%-16s %-8s %-14s %-20s %-10s %s' % (
            'CLAIM', 'OUI', 'PRODUCT', 'SERIAL', 'CREDENTIAL', 'NETWORK/LABEL',
        ), file=out)
        for c in claims.list():
            cred = creds.get(c.cpe_key)
            state = 'per-cpe' if cred.rotated else 'bootstrap'
            if cred.pending_ha1:
                state += '+pending'
            print('%-16s %-8s %-14s %-20s %-10s %s' % (
                c.claim_id, c.oui, c.product_class or '-', c.serial, state,
                '/'.join(x for x in (c.network, c.label) if x) or '-',
            ), file=out)
    elif args.command == 'reset-credentials':
        if claims.get(args.claim_id) is None:
            print('error: no claim %s' % args.claim_id, file=out)
            return 1
        creds.reset(cpe_key_of(args.claim_id))
        print('%s may bootstrap again' % args.claim_id, file=out)
    return 0


def make_dev_cert(out_dir: str, cn: str, days: int = 365):
    """
    A self-signed cert for trying the WAN listener. CPEs will not trust it
    unless told to skip verification; production uses a real certificate.
    """
    os.makedirs(out_dir, exist_ok=True)
    cert = os.path.join(out_dir, 'acsd_wan.crt')
    key = os.path.join(out_dir, 'acsd_wan.key')
    san = 'IP:%s' % cn if _is_ip(cn) else 'DNS:%s' % cn
    subprocess.run(
        [
            'openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
            '-keyout', key, '-out', cert, '-days', str(days),
            '-subj', '/CN=%s' % cn, '-addext', 'subjectAltName=%s' % san,
        ],
        check=True, capture_output=True,
    )
    os.chmod(key, 0o600)
    return cert, key


def _is_ip(value: str) -> bool:
    try:
        ipaddress.ip_address(value)
    except ValueError:
        return False
    return True


def main(argv: Optional[List[str]] = None) -> None:
    sys.exit(run(sys.argv[1:] if argv is None else argv))
