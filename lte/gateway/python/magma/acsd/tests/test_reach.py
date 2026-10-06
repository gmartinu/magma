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
from magma.acsd.config import (
    CONNECTION_REQUEST_ALWAYS,
    CONNECTION_REQUEST_OFF,
    ReachConfig,
)
from magma.acsd.credentials import CredentialStore
from magma.acsd.reach import (
    BEHIND_NAT,
    CONNECTION_REQUEST,
    DISABLED,
    FAILED,
    FROZEN,
    NEXT_INFORM,
    NO_CREDENTIAL,
    NO_URL,
    Reach,
    Reacher,
    assess,
)
from magma.acsd.store import SESSION_COMPLETED, AcsStore, Session

AUTO = ReachConfig()
NAT_IP = '203.0.113.7'
KEY = 'CLAIMtitan-1'


def _how(url, source_ip=NAT_IP, config=AUTO, **kwargs):
    reach = assess(config, url, source_ip, kwargs.pop('has_credential', True), **kwargs)
    return reach.how, reach.reason


class AssessTest(unittest.TestCase):
    def test_cgnat_or_private_url_from_another_address_is_behind_nat(self):
        # What a CPE on a carrier network reports: its address inside it.
        for url in (
            'http://100.64.12.34:7547/', 'http://10.20.0.5:7547/cr',
            'http://192.168.1.1:7547/', 'http://[fd00::5]:7547/',
            'http://169.254.0.1/', 'http://127.0.0.1:7547/',
        ):
            self.assertEqual(_how(url), (NEXT_INFORM, BEHIND_NAT), url)

    def test_url_on_the_source_address_is_reachable(self):
        self.assertEqual(_how('http://203.0.113.7:7547/'), (CONNECTION_REQUEST, ''))
        # A lab CPE on the AGW's own LAN: private, but no NAT in between.
        self.assertEqual(
            _how('http://192.168.128.12:7547/cr', source_ip='192.168.128.12'),
            (CONNECTION_REQUEST, ''),
        )
        self.assertEqual(
            _how('http://192.168.128.12:7547/', source_ip='::ffff:192.168.128.12'),
            (CONNECTION_REQUEST, ''),
        )

    def test_public_address_or_name_is_tried(self):
        self.assertEqual(_how('http://93.184.216.34:7547/'), (CONNECTION_REQUEST, ''))
        self.assertEqual(_how('https://cpe.example.net:7547/'), (CONNECTION_REQUEST, ''))

    def test_always_skips_the_address_check_only(self):
        always = ReachConfig(connection_request=CONNECTION_REQUEST_ALWAYS)
        self.assertEqual(
            _how('http://100.64.12.34:7547/', config=always), (CONNECTION_REQUEST, ''),
        )
        self.assertEqual(
            _how('http://100.64.12.34:7547/', config=always, has_credential=False),
            (NEXT_INFORM, NO_CREDENTIAL),
        )

    def test_reasons_to_wait(self):
        url = 'http://203.0.113.7:7547/'
        off = ReachConfig(connection_request=CONNECTION_REQUEST_OFF)
        self.assertEqual(_how(url, config=off), (NEXT_INFORM, DISABLED))
        self.assertEqual(_how(url, has_credential=False), (NEXT_INFORM, NO_CREDENTIAL))
        self.assertEqual(_how(''), (NEXT_INFORM, NO_URL))
        self.assertEqual(_how('not a url'), (NEXT_INFORM, NO_URL))
        self.assertEqual(_how(url, failed_since_inform=True), (NEXT_INFORM, FAILED))
        self.assertEqual(_how(url, frozen=True), (NEXT_INFORM, FROZEN))

    def test_reach_carries_url_and_source(self):
        reach = assess(AUTO, 'http://100.64.0.9/', NAT_IP, True)
        self.assertEqual(reach, Reach(NEXT_INFORM, BEHIND_NAT, 'http://100.64.0.9/', NAT_IP))


class Clock:
    def __init__(self):
        self.now = 10000.0

    def __call__(self):
        return self.now


class ReacherTest(unittest.TestCase):
    def setUp(self):
        self.clock = Clock()
        self.redis = fakeredis.FakeStrictRedis()
        self.store = AcsStore(self.redis, clock=self.clock)
        self.creds = CredentialStore(self.redis, 'magma-acs', clock=self.clock)
        self.reacher = Reacher(AUTO, self.store, self.creds, self.redis, clock=self.clock)

    def _cpe(self, url='http://203.0.113.7:7547/', rotated=True):
        self.store.count_inform(KEY)
        self.store.put_model(KEY, 'generic', {
            'management_server': {'connection_request_url': url},
        })
        if rotated:
            self.creds.begin_rotation(KEY, 16)
            self.creds.promote(KEY, 1)
        session = Session('s1', KEY, NAT_IP, session_key='claimed/x', mode='claimed')
        self.store.close_session(session, SESSION_COMPLETED, 'session ended')

    def test_claimed_cpe_with_credential_on_its_own_address(self):
        self._cpe()
        self.assertEqual(self.reacher.assess_cpe(KEY).how, CONNECTION_REQUEST)
        self.assertEqual(self.reacher.assess_cpe(KEY).source_ip, NAT_IP)

    def test_not_rotated_yet_has_no_credential(self):
        self._cpe(rotated=False)
        self.assertEqual(self.reacher.assess_cpe(KEY).reason, NO_CREDENTIAL)

    def test_without_claimed_mode_no_cpe_has_a_credential(self):
        self._cpe()
        reacher = Reacher(AUTO, self.store, None, self.redis)
        self.assertEqual(reacher.assess_cpe(KEY).reason, NO_CREDENTIAL)

    def test_open_session_source_wins(self):
        self._cpe(url='http://198.51.100.20:7547/')
        self.store.put_session(Session('s2', KEY, '198.51.100.20', session_key='claimed/y'))
        self.assertEqual(self.reacher.source_ip(KEY), '198.51.100.20')

    def test_failed_attempt_waits_for_the_next_inform(self):
        self._cpe()
        self.clock.now += 10
        attempt = self.reacher.record_attempt(KEY, False, 'timed out')
        self.assertEqual((attempt.at, attempt.ok), (self.clock.now, False))
        self.assertEqual(self.reacher.assess_cpe(KEY).reason, FAILED)
        # Survives an acsd restart.
        restarted = Reacher(AUTO, self.store, self.creds, self.redis)
        self.assertEqual(restarted.last_attempt(KEY).detail, 'timed out')
        self.clock.now += 10
        self.store.count_inform(KEY)
        self.assertEqual(self.reacher.assess_cpe(KEY).how, CONNECTION_REQUEST)

    def test_successful_attempt_keeps_it_reachable(self):
        self._cpe()
        self.clock.now += 10
        self.reacher.record_attempt(KEY, True)
        self.assertEqual(self.reacher.assess_cpe(KEY).how, CONNECTION_REQUEST)


if __name__ == '__main__':
    unittest.main()
