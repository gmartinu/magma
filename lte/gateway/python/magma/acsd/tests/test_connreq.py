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

import hashlib
import http.server
import ipaddress
import re
import threading
import unittest
from types import SimpleNamespace

import fakeredis
import requests
from magma.acsd.config import CONNECTION_REQUEST_ALWAYS, ReachConfig
from magma.acsd.connreq import (
    AUTO_COOLDOWN_SEC,
    IN_SESSION,
    AddressPolicy,
    ConnectionRequester,
    pinned_get,
    resolve,
)
from magma.acsd.credentials import CredentialStore
from magma.acsd.reach import (
    BEHIND_NAT,
    CONNECTION_REQUEST,
    FAILED,
    NEXT_INFORM,
    NO_CREDENTIAL,
    Reacher,
)
from magma.acsd.store import SESSION_COMPLETED, AcsStore, Session

KEY = 'CLAIMtitan-1'
# The CPE reports its own public address: no NAT in between.
NAT_IP = '203.0.113.9'
GATEWAY = [ipaddress.ip_interface('198.51.100.1/24'), ipaddress.ip_interface('10.0.2.15/24')]
CPE_REALM = 'titan-cr'


class Clock:
    def __init__(self):
        self.now = 10000.0

    def __call__(self):
        return self.now


class Inline:
    """An executor that runs the job at once, so tests need no waiting."""

    def __init__(self):
        self.jobs = 0

    def submit(self, fn, *args):
        self.jobs += 1
        fn(*args)


def _md5(text):
    return hashlib.md5(text.encode()).hexdigest()


class FakeCpe(http.server.BaseHTTPRequestHandler):
    """A CPE's Connection Request endpoint: Digest with its own realm."""

    password = ''
    status = 204
    hits = []
    hosts = []

    def do_GET(self):
        FakeCpe.hosts.append(self.headers.get('Host'))
        auth = self.headers.get('Authorization', '')
        if not auth.startswith('Digest ') or not self._valid(auth):
            self.send_response(401)
            self.send_header(
                'WWW-Authenticate',
                'Digest realm="%s", nonce="n0nce", qop="auth", algorithm=MD5' % CPE_REALM,
            )
            self.send_header('Content-Length', '0')
            self.end_headers()
            return
        FakeCpe.hits.append(self.path)
        self.send_response(FakeCpe.status)
        self.send_header('Content-Length', '0')
        self.end_headers()

    def _valid(self, auth):
        f = dict(re.findall(r'(\w+)="?([^",]*)"?', auth[len('Digest '):]))
        ha1 = _md5('%s:%s:%s' % (f.get('username'), CPE_REALM, FakeCpe.password))
        ha2 = _md5('GET:%s' % f.get('uri'))
        expected = _md5(':'.join((ha1, 'n0nce', f.get('nc', ''), f.get('cnonce', ''), 'auth', ha2)))
        return f.get('response') == expected

    def log_message(self, *args):
        pass


class ConnectionRequesterTest(unittest.TestCase):
    def setUp(self):
        self.clock = Clock()
        self.redis = fakeredis.FakeStrictRedis()
        self.store = AcsStore(self.redis, clock=self.clock)
        self.creds = CredentialStore(self.redis, 'magma-acs', clock=self.clock)
        self.executor = Inline()
        self.calls = []
        self.response = SimpleNamespace(status_code=204)
        self.requester = self._requester(ReachConfig())

    def _requester(self, config, http_get=None, resolver=None):
        self.reacher = Reacher(config, self.store, self.creds, self.redis, clock=self.clock)
        return ConnectionRequester(
            self.reacher, self.store, 5.0,
            http_get=http_get or self._get,
            resolver=resolver or (lambda host: []),
            executor=self.executor, clock=self.clock,
            policy=AddressPolicy(lambda: GATEWAY),
        )

    def _get(self, url, **kwargs):
        self.calls.append((url, kwargs))
        if isinstance(self.response, Exception):
            raise self.response
        return self.response

    def _cpe(self, url='http://203.0.113.9:7547/cr', rotated=True):
        self.store.count_inform(KEY)
        self.store.put_model(KEY, 'generic', {
            'management_server': {'connection_request_url': url},
        })
        if rotated:
            self.creds.begin_rotation(KEY, 16)
            self.creds.promote(KEY, 1)
        session = Session('s1', KEY, NAT_IP, session_key='claimed/x', mode='claimed')
        self.store.close_session(session, SESSION_COMPLETED, 'session ended')

    def test_accepted_request(self):
        self._cpe()
        outcome = self.requester.request(KEY)
        self.assertTrue(outcome.sent)
        self.assertEqual(outcome.reach.how, CONNECTION_REQUEST)
        [(url, kwargs)] = self.calls
        self.assertEqual(url, 'http://203.0.113.9:7547/cr')
        self.assertEqual(kwargs['address'], '203.0.113.9')
        self.assertEqual(kwargs['auth'].username, KEY + '.1')
        self.assertEqual(kwargs['auth'].password, self.creds.get(KEY).cr_password)
        self.assertEqual(kwargs['timeout'], 5.0)
        self.assertFalse(kwargs['allow_redirects'])
        self.assertTrue(self.reacher.last_attempt(KEY).ok)

    def test_cpe_behind_nat_gets_no_request(self):
        self._cpe(url='http://100.64.3.4:7547/')
        outcome = self.requester.request(KEY)
        self.assertEqual((outcome.sent, outcome.reach.reason), (False, BEHIND_NAT))
        self.assertEqual(self.calls, [])
        self.assertIsNone(self.reacher.last_attempt(KEY))

    def test_without_credential_no_request(self):
        self._cpe(rotated=False)
        self.assertEqual(self.requester.request(KEY).reach.reason, NO_CREDENTIAL)
        self.assertEqual(self.calls, [])

    def test_open_session_needs_no_request(self):
        self._cpe()
        self.store.put_session(Session('s2', KEY, NAT_IP, session_key='claimed/y'))
        outcome = self.requester.request(KEY)
        self.assertEqual((outcome.sent, outcome.reach.reason), (False, IN_SESSION))
        self.assertEqual(self.calls, [])

    def test_refused_or_unreachable_waits_for_the_next_inform(self):
        self._cpe()
        for response, detail in (
            (SimpleNamespace(status_code=401), 'HTTP 401'),
            (requests.ConnectTimeout('timed out'), 'ConnectTimeout: timed out'),
        ):
            self.response = response
            self.calls.clear()
            outcome = self.requester.request(KEY)
            self.assertEqual(
                (outcome.sent, outcome.reach.how, outcome.reach.reason, outcome.detail),
                (False, NEXT_INFORM, FAILED, detail),
            )
            self.assertEqual(self.reacher.assess_cpe(KEY).reason, FAILED)
            # No second try until the CPE Informs again.
            self.response = SimpleNamespace(status_code=204)
            self.assertFalse(self.requester.request(KEY).sent)
            self.assertEqual(len(self.calls), 1)
            self.clock.now += 1
            self.store.count_inform(KEY)
            self.clock.now += 1

    def test_name_resolving_to_a_private_address_is_refused(self):
        self._cpe(url='http://cpe.carrier.example:7547/')
        requester = self._requester(ReachConfig(), resolver=lambda host: ['10.9.8.7'])
        outcome = requester.request(KEY)
        self.assertEqual((outcome.sent, outcome.reach.reason), (False, BEHIND_NAT))
        self.assertIn('resolves to 10.9.8.7', outcome.detail)
        self.assertEqual(self.calls, [])

        requester = self._requester(ReachConfig(), resolver=lambda host: [])
        self.clock.now += 1
        self.store.count_inform(KEY)
        outcome = requester.request(KEY)
        self.assertEqual((outcome.reach.reason, outcome.detail), (FAILED, 'cannot resolve cpe.carrier.example'))

        self.clock.now += 1
        self.store.count_inform(KEY)
        requester = self._requester(ReachConfig(), resolver=lambda host: ['93.184.216.34'])
        self.assertTrue(requester.request(KEY).sent)
        # Sent to the address that was checked, not resolved again.
        self.assertEqual(self.calls[-1][1]['address'], '93.184.216.34')

    def test_every_resolved_address_is_checked(self):
        # A rebinding name: one public answer, one inside the gateway.
        self._cpe(url='http://rebind.example:7547/')
        requester = self._requester(
            ReachConfig(), resolver=lambda host: ['93.184.216.34', '10.0.2.7'],
        )
        outcome = requester.request(KEY)
        self.assertEqual((outcome.sent, outcome.reach.reason), (False, BEHIND_NAT))
        self.assertIn('10.0.2.7', outcome.detail)
        self.assertEqual(self.calls, [])

    def test_always_mode_sends_to_private_addresses(self):
        self._cpe(url='http://cpe.carrier.example:7547/')
        requester = self._requester(
            ReachConfig(connection_request=CONNECTION_REQUEST_ALWAYS),
            resolver=lambda host: ['100.64.3.4'],
        )
        self.assertTrue(requester.request(KEY).sent)
        self.assertEqual(self.calls[-1][1]['address'], '100.64.3.4')

    def test_always_mode_still_refuses_the_gateway_and_special_addresses(self):
        always = ReachConfig(connection_request=CONNECTION_REQUEST_ALWAYS)
        for url, resolved, detail in (
            ('http://127.0.0.1:7547/', [], 'loopback'),
            ('http://[::1]:7547/', [], 'loopback'),
            ('http://cpe.example/', ['169.254.169.254'], 'link-local'),
            ('http://cpe.example/', ['224.0.0.1'], 'multicast'),
            ('http://0.0.0.0/', [], 'unspecified'),
            ('http://198.51.100.1/', [], 'address of this gateway'),
            ('http://cpe.example/', ['10.0.2.20'], 'network 10.0.2.0/24'),
        ):
            self.calls.clear()
            self.clock.now += 1
            self.store.count_inform(KEY)
            self._cpe(url=url)
            requester = self._requester(always, resolver=lambda host, r=resolved: r)
            outcome = requester.request(KEY)
            self.assertEqual((outcome.sent, outcome.reach.reason), (False, FAILED), url)
            self.assertIn(detail, outcome.detail, url)
            self.assertEqual(self.calls, [], url)

    def test_always_mode_allows_the_session_source_on_a_gateway_network(self):
        # A CPE on the gateway's LAN, reporting the address it comes from.
        self._cpe(url='http://10.0.2.20:7547/')
        session = Session('s1', KEY, '10.0.2.20', session_key='claimed/x', mode='claimed')
        self.store.close_session(session, SESSION_COMPLETED, 'session ended')
        requester = self._requester(ReachConfig(connection_request=CONNECTION_REQUEST_ALWAYS))
        self.assertTrue(requester.request(KEY).sent)

    def test_request_soon_skips_a_cpe_already_asked(self):
        self._cpe()
        self.clock.now += 1
        self.requester.request_soon(KEY)
        self.requester.request_soon(KEY)
        self.assertEqual((self.executor.jobs, len(self.calls)), (1, 1))
        self.clock.now += AUTO_COOLDOWN_SEC
        self.requester.request_soon(KEY)
        self.assertEqual(len(self.calls), 2)
        # The CPE answered: the next task asks it again right away.
        self.clock.now += 1
        self.store.count_inform(KEY)
        self.requester.request_soon(KEY)
        self.assertEqual(len(self.calls), 3)

    def test_request_soon_survives_errors(self):
        self._cpe()
        self.response = RuntimeError('boom')
        with self.assertLogs(level='ERROR'):
            self.requester.request_soon(KEY)
        self.response = SimpleNamespace(status_code=204)
        self.requester.request_soon(KEY)
        self.assertEqual(len(self.calls), 2)


class DigestOverHttpTest(unittest.TestCase):
    """Against a local HTTP server that challenges like a CPE does."""

    def setUp(self):
        self.server = http.server.HTTPServer(('127.0.0.1', 0), FakeCpe)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        redis = fakeredis.FakeStrictRedis()
        self.store = AcsStore(redis)
        self.creds = CredentialStore(redis, 'magma-acs')
        self.creds.begin_rotation(KEY, 16)
        self.creds.promote(KEY, 1)
        FakeCpe.password = self.creds.get(KEY).cr_password
        FakeCpe.status, FakeCpe.hits = 204, []
        self.url = 'http://127.0.0.1:%d/cr' % self.server.server_port
        self.store.count_inform(KEY)
        self.store.put_model(KEY, 'generic', {
            'management_server': {'connection_request_url': self.url},
        })
        session = Session('s1', KEY, '127.0.0.1', session_key='claimed/x', mode='claimed')
        self.store.close_session(session, SESSION_COMPLETED, 'session ended')
        self.reacher = Reacher(ReachConfig(), self.store, self.creds, redis)
        # The stand-in CPE listens on loopback, which the real policy refuses.
        loopback_ok = SimpleNamespace(refusal=lambda *args: None)
        self.requester = ConnectionRequester(self.reacher, self.store, 2.0, policy=loopback_ok)

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()

    def test_digest_with_the_cpe_realm(self):
        outcome = self.requester.request(KEY)
        self.assertTrue(outcome.sent, outcome.detail)
        self.assertEqual(FakeCpe.hits, ['/cr'])

    def test_wrong_password_is_a_401(self):
        FakeCpe.password = 'something-else'
        outcome = self.requester.request(KEY)
        self.assertEqual((outcome.sent, outcome.detail), (False, 'HTTP 401'))

    def test_other_status_is_a_failure(self):
        FakeCpe.status = 503
        self.assertEqual(self.requester.request(KEY).detail, 'HTTP 503')


class PinnedGetTest(unittest.TestCase):
    """pinned_get connects to the given address and keeps the URL's Host."""

    def setUp(self):
        self.server = http.server.HTTPServer(('127.0.0.1', 0), FakeCpe)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        FakeCpe.status, FakeCpe.hits, FakeCpe.hosts = 204, [], []

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()

    def test_name_is_never_resolved(self):
        url = 'http://cpe.invalid:%d/cr' % self.server.server_port
        resp = pinned_get(url, address='127.0.0.1', timeout=2, allow_redirects=False)
        self.assertEqual(resp.status_code, 401)
        self.assertEqual(FakeCpe.hosts, ['cpe.invalid:%d' % self.server.server_port])

    def test_interface_netmasks(self):
        from magma.acsd.connreq import _prefix
        self.assertEqual(_prefix('ffff:ffff:ffff:ffff::'), '64')
        self.assertEqual(_prefix('ffff:ffff::/32'), '32')
        self.assertEqual(_prefix('255.255.255.0'), '255.255.255.0')

    def test_policy_reads_the_interfaces_once_per_refresh(self):
        reads = []
        policy = AddressPolicy(lambda: reads.append(1) or GATEWAY, clock=lambda: 0.0)
        for _ in range(3):
            policy.refusal(ipaddress.ip_address('192.0.2.1'), '', True)
        self.assertEqual(len(reads), 1)

    def test_resolve_lists_addresses_once_each(self):
        addresses = resolve('127.0.0.1')
        self.assertEqual(addresses, ['127.0.0.1'])


if __name__ == '__main__':
    unittest.main()
