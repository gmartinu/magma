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

import unittest

import fakeredis
from magma.acsd.claims import (
    Claim,
    ClaimError,
    ClaimRegistry,
    cpe_key_of,
    is_claim_key,
)


class ClaimRegistryTest(unittest.TestCase):
    def setUp(self):
        self.now = 100.0
        self.claims = ClaimRegistry(
            fakeredis.FakeStrictRedis(), clock=lambda: self.now,
        )

    def test_add_and_match_by_device_id(self):
        claim = self.claims.add(
            '00a1b2', 'Titan4000', 'SN1', claim_id='titan-1',
            network='net1', label='lab',
        )
        self.assertEqual(claim.cpe_key, 'CLAIMtitan-1')
        self.assertEqual(claim.oui, '00A1B2')
        self.assertEqual(self.claims.match('00A1B2', 'Titan4000', 'SN1'), claim)
        # OUI is hex, so its case does not matter; the rest is exact.
        self.assertEqual(self.claims.match('00a1b2', 'Titan4000', 'SN1'), claim)
        self.assertIsNone(self.claims.match('00A1B2', 'titan4000', 'SN1'))
        self.assertIsNone(self.claims.match('00A1B2', 'Titan4000', 'SN2'))

    def test_generated_claim_id(self):
        claim = self.claims.add('00A1B2', '', 'SN1')
        self.assertRegex(claim.claim_id, r'^[0-9a-f]{12}$')
        self.assertEqual(self.claims.match('00A1B2', '', 'SN1'), claim)

    def test_claim_keys_never_look_like_imsi_keys(self):
        self.assertTrue(is_claim_key(cpe_key_of('x')))
        self.assertFalse(is_claim_key('IMSI001010000000001'))

    def test_refuses_bad_and_duplicate_claims(self):
        self.claims.add('00A1B2', 'P', 'SN1', claim_id='a')
        for kwargs in (
            {'claim_id': 'a', 'serial': 'SN2'},
            {'claim_id': 'b', 'serial': 'SN1'},
            {'claim_id': 'bad id', 'serial': 'SN3'},
            {'claim_id': 'c', 'serial': ' '},
        ):
            with self.subTest(**kwargs), self.assertRaises(ClaimError):
                self.claims.add('00A1B2', 'P', kwargs['serial'], kwargs['claim_id'])

    def test_remove(self):
        self.claims.add('00A1B2', 'P', 'SN1', claim_id='a')
        self.assertEqual(self.claims.remove('a').serial, 'SN1')
        self.assertIsNone(self.claims.match('00A1B2', 'P', 'SN1'))
        self.assertIsNone(self.claims.remove('a'))
        # The device can be claimed again once released.
        self.claims.add('00A1B2', 'P', 'SN1', claim_id='b')

    def test_list_in_creation_order(self):
        self.claims.add('00A1B2', 'P', 'SN2', claim_id='z')
        self.now = 200.0
        self.claims.add('00A1B2', 'P', 'SN1', claim_id='a')
        self.assertEqual([c.claim_id for c in self.claims.list()], ['z', 'a'])

    def test_replace_all_reports_claims_to_reset(self):
        self.claims.add('00A1B2', 'P', 'SN1', claim_id='keep')
        self.claims.add('00A1B2', 'P', 'SN2', claim_id='moved')
        self.claims.add('00A1B2', 'P', 'SN3', claim_id='gone')
        reset = self.claims.replace_all([
            Claim('keep', '00A1B2', 'P', 'SN1', label='relabeled'),
            Claim('moved', '00A1B2', 'P', 'SN9'),
            Claim('new', '00A1B2', 'P', 'SN4'),
        ])
        self.assertEqual(sorted(reset), ['gone', 'moved'])
        self.assertEqual(self.claims.get('keep').label, 'relabeled')
        self.assertEqual(self.claims.get('keep').created, 100.0)
        self.assertIsNone(self.claims.match('00A1B2', 'P', 'SN2'))
        self.assertEqual(self.claims.match('00A1B2', 'P', 'SN9').claim_id, 'moved')
        self.assertEqual(
            sorted(c.claim_id for c in self.claims.list()), ['keep', 'moved', 'new'],
        )


if __name__ == '__main__':
    unittest.main()
