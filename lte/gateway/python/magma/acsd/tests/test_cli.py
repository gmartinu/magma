"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
"""

import io
import os
import shutil
import ssl
import stat
import tempfile
import unittest

import fakeredis
from magma.acsd import cli
from magma.acsd.claims import ClaimRegistry
from magma.acsd.credentials import CredentialStore


class CliTest(unittest.TestCase):
    def setUp(self):
        self.redis = fakeredis.FakeStrictRedis()

    def run_cli(self, *argv):
        out = io.StringIO()
        rc = cli.run(list(argv), client=self.redis, out=out)
        return rc, out.getvalue()

    def test_add_list_remove(self):
        rc, out = self.run_cli(
            'claim-add', '--oui', '00a1b2', '--product-class', 'Titan4000',
            '--serial', 'SN1', '--id', 'titan-1', '--label', 'lab',
        )
        self.assertEqual(rc, 0)
        self.assertIn('CLAIMtitan-1', out)
        self.assertEqual(
            ClaimRegistry(self.redis).match('00A1B2', 'Titan4000', 'SN1').claim_id,
            'titan-1',
        )
        _, out = self.run_cli('claim-list')
        row = out.splitlines()[1].split()
        self.assertEqual(row[:5], ['titan-1', '00A1B2', 'Titan4000', 'SN1', 'bootstrap'])

        creds = CredentialStore(self.redis, 'magma-acs')
        creds.begin_rotation('CLAIMtitan-1', 16)
        creds.promote('CLAIMtitan-1', 1)
        _, out = self.run_cli('claim-list')
        self.assertIn('per-cpe', out)

        self.assertEqual(self.run_cli('claim-remove', 'titan-1')[0], 0)
        self.assertIsNone(creds.owner('CLAIMtitan-1.1'))
        self.assertEqual(self.run_cli('claim-remove', 'titan-1')[0], 1)

    def test_duplicate_claim_is_an_error(self):
        self.run_cli('claim-add', '--oui', '00A1B2', '--serial', 'SN1')
        rc, out = self.run_cli('claim-add', '--oui', '00A1B2', '--serial', 'SN1')
        self.assertEqual(rc, 1)
        self.assertIn('already claimed', out)

    def test_reset_credentials(self):
        self.run_cli('claim-add', '--oui', '00A1B2', '--serial', 'SN1', '--id', 'a')
        creds = CredentialStore(self.redis, 'magma-acs')
        creds.begin_rotation('CLAIMa', 16)
        creds.promote('CLAIMa', 1)
        self.assertEqual(self.run_cli('reset-credentials', 'a')[0], 0)
        self.assertFalse(creds.get('CLAIMa').rotated)
        self.assertEqual(self.run_cli('reset-credentials', 'missing')[0], 1)


@unittest.skipUnless(shutil.which('openssl'), 'needs openssl')
class DevCertTest(unittest.TestCase):
    def test_cert_loads_as_a_server_cert(self):
        tmp = tempfile.mkdtemp()
        try:
            rc = cli.run(['dev-cert', '--out-dir', tmp, '--cn', '127.0.0.1'], out=io.StringIO())
            self.assertEqual(rc, 0)
            cert = os.path.join(tmp, 'acsd_wan.crt')
            key = os.path.join(tmp, 'acsd_wan.key')
            ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER).load_cert_chain(cert, key)
            self.assertEqual(stat.S_IMODE(os.stat(key).st_mode), 0o600)
        finally:
            shutil.rmtree(tmp, ignore_errors=True)


if __name__ == '__main__':
    unittest.main()
