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

import threading
import unittest

import fakeredis
from magma.acsd.credentials import (
    ACTIVE,
    PENDING,
    CredentialsBusy,
    CredentialStore,
    RedisMutex,
    random_password,
)
from magma.acsd.digest import ha1_of

REALM = 'magma-acs'
KEY = 'CLAIMtitan-1'


class CredentialStoreTest(unittest.TestCase):
    def setUp(self):
        self.redis = fakeredis.FakeStrictRedis()
        self.creds = CredentialStore(self.redis, REALM)

    def test_new_cpe_has_no_credential(self):
        cred = self.creds.get(KEY)
        self.assertFalse(cred.rotated)
        self.assertIsNone(self.creds.owner('%s.1' % KEY))

    def test_rotation_is_pending_until_promoted(self):
        cred, password = self.creds.begin_rotation(KEY, 16)
        self.assertEqual(len(password), 16)
        self.assertEqual(cred.pending_username, KEY + '.1')
        self.assertFalse(self.creds.get(KEY).rotated)
        owner = self.creds.owner(KEY + '.1')
        self.assertEqual((owner.cpe_key, owner.which), (KEY, PENDING))
        self.assertEqual(owner.ha1, ha1_of(KEY + '.1', REALM, password))

        self.assertTrue(self.creds.promote(KEY, 1))
        cred = self.creds.get(KEY)
        self.assertTrue(cred.rotated)
        self.assertEqual((cred.username, cred.generation), (KEY + '.1', 1))
        self.assertEqual(self.creds.owner(KEY + '.1').which, ACTIVE)
        # Promoting twice is a no-op.
        self.assertFalse(self.creds.promote(KEY, 1))

    def test_next_rotation_retires_the_old_username(self):
        self.creds.begin_rotation(KEY, 16)
        self.creds.promote(KEY, 1)
        cred, _ = self.creds.begin_rotation(KEY, 16)
        self.assertEqual(cred.pending_generation, 2)
        # Until the CPE takes the new one, the old one still works.
        self.assertEqual(self.creds.owner(KEY + '.1').which, ACTIVE)
        self.creds.promote(KEY, 2)
        self.assertIsNone(self.creds.owner(KEY + '.1'))
        self.assertEqual(self.creds.owner(KEY + '.2').which, ACTIVE)

    def test_abort_drops_only_that_generation(self):
        self.creds.begin_rotation(KEY, 16)
        self.creds.begin_rotation(KEY, 16)
        # Generation 1 was replaced by 2; a late failure of 1 changes nothing.
        self.assertIsNone(self.creds.owner(KEY + '.1'))
        self.creds.abort_rotation(KEY, 1)
        self.assertEqual(self.creds.owner(KEY + '.2').which, PENDING)
        self.assertFalse(self.creds.promote(KEY, 1))
        self.creds.abort_rotation(KEY, 2)
        self.assertIsNone(self.creds.owner(KEY + '.2'))
        self.assertFalse(self.creds.get(KEY).rotated)

    def test_reset_forgets_everything(self):
        self.creds.begin_rotation(KEY, 16)
        self.creds.promote(KEY, 1)
        self.creds.begin_rotation(KEY, 16)
        self.creds.reset(KEY)
        self.assertFalse(self.creds.get(KEY).rotated)
        self.assertIsNone(self.creds.owner(KEY + '.1'))
        self.assertIsNone(self.creds.owner(KEY + '.2'))
        # After a reset the generation starts over; usernames are reused
        # only by the same CPE.
        cred, _ = self.creds.begin_rotation(KEY, 16)
        self.assertEqual(cred.pending_username, KEY + '.1')

    def test_connection_request_credential_follows_the_rotation(self):
        cred, password = self.creds.begin_rotation(KEY, 16)
        self.assertEqual(len(cred.pending_cr_password), 16)
        self.assertNotEqual(cred.pending_cr_password, password)
        self.assertIsNone(self.creds.get(KEY).connection_request)
        self.creds.promote(KEY, 1)
        cred = self.creds.get(KEY)
        self.assertEqual(cred.connection_request, (KEY + '.1', cred.cr_password))
        self.assertEqual(cred.pending_cr_password, '')

        # A failed next rotation keeps the one in use.
        self.creds.begin_rotation(KEY, 16)
        self.creds.abort_rotation(KEY, 2)
        after = self.creds.get(KEY)
        self.assertEqual(after.connection_request, cred.connection_request)
        self.assertEqual(after.pending_cr_password, '')

    def test_drop_connection_request(self):
        self.assertFalse(self.creds.drop_connection_request(KEY))
        self.creds.begin_rotation(KEY, 16)
        self.creds.promote(KEY, 1)
        self.assertTrue(self.creds.drop_connection_request(KEY))
        cred = self.creds.get(KEY)
        self.assertIsNone(cred.connection_request)
        # The ACS credential stays.
        self.assertTrue(cred.rotated)
        self.assertEqual(self.creds.owner(KEY + '.1').which, ACTIVE)

    def test_reset_forgets_the_connection_request_credential(self):
        self.creds.begin_rotation(KEY, 16)
        self.creds.promote(KEY, 1)
        self.creds.reset(KEY)
        self.assertIsNone(self.creds.get(KEY).connection_request)

    def test_records_without_connection_request_fields_load(self):
        # Written by an acsd from before Connection Request credentials.
        self.creds.begin_rotation(KEY, 16)
        self.creds.promote(KEY, 1)
        raw = dict(self.creds._creds[KEY])
        del raw['cr_password'], raw['pending_cr_password']
        self.creds._creds[KEY] = raw
        cred = self.creds.get(KEY)
        self.assertTrue(cred.rotated)
        self.assertIsNone(cred.connection_request)

    def test_password_never_reaches_redis(self):
        _, password = self.creds.begin_rotation(KEY, 24)
        self.creds.promote(KEY, 1)
        dump = b''.join(
            f + v for k in self.redis.keys('*')
            for f, v in self.redis.hgetall(k).items()
        )
        self.assertTrue(dump)
        self.assertNotIn(password.encode(), dump)

    def test_random_password_length_and_alphabet(self):
        for n in (8, 32, 64):
            p = random_password(n)
            self.assertEqual(len(p), n)
            self.assertRegex(p, r'^[A-Za-z0-9_-]+$')



class CrossProcessTest(unittest.TestCase):
    """acsd and acsd_cli.py are two CredentialStores on one Redis."""

    def setUp(self):
        self.redis = fakeredis.FakeStrictRedis()
        self.acsd = CredentialStore(self.redis, REALM)
        self.cli = CredentialStore(self.redis, REALM)

    def test_cli_reset_waits_for_a_promotion_in_flight(self):
        cred, _ = self.acsd.begin_rotation(KEY, 16)
        entered, go = threading.Event(), threading.Event()
        real_get = self.acsd.get

        def slow_get(cpe_key):
            got = real_get(cpe_key)
            entered.set()
            go.wait(5)
            return got

        self.acsd.get = slow_get
        promote = threading.Thread(
            target=self.acsd.promote, args=(KEY, cred.pending_generation),
        )
        promote.start()
        self.assertTrue(entered.wait(5))
        reset = threading.Thread(target=self.cli.reset, args=(KEY,))
        reset.start()
        reset.join(0.2)
        self.assertTrue(reset.is_alive(), 'reset ran during the promotion')
        go.set()
        promote.join(5)
        reset.join(5)
        # The reset came last and stands; the promotion did not undo it.
        self.assertFalse(self.cli.get(KEY).rotated)
        self.assertIsNone(self.cli.owner(cred.pending_username))

    def test_busy_lock_raises(self):
        self.redis.set('acsd:credentials_lock', 'someone-else')
        mutex = RedisMutex(self.redis, 'acsd:credentials_lock', wait_sec=0.05)
        with self.assertRaises(CredentialsBusy):
            with mutex.held():
                pass

    def test_release_leaves_another_holder_alone(self):
        mutex = RedisMutex(self.redis, 'acsd:credentials_lock')
        with mutex.held():
            # Our hold expired and another process took the lock.
            self.redis.set('acsd:credentials_lock', 'someone-else')
        self.assertEqual(self.redis.get('acsd:credentials_lock'), b'someone-else')

    def test_lock_is_freed_after_each_update(self):
        self.acsd.begin_rotation(KEY, 16)
        self.cli.reset(KEY)
        self.assertIsNone(self.redis.get('acsd:credentials_lock'))


if __name__ == '__main__':
    unittest.main()
