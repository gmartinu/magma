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
import re
import threading
import unittest
from types import SimpleNamespace

import fakeredis
import requests
from magma.acsd.config import CONNECTION_REQUEST_ALWAYS, ReachConfig
from magma.acsd.connreq import AUTO_COOLDOWN_SEC, IN_SESSION, ConnectionRequester
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
NAT_IP = '127.0.0.1'
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

    def do_GET(self):
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
            resolver=resolver or (lambda host: None),
            executor=self.executor, clock=self.clock,
        )

    def _get(self, url, **kwargs):
        self.calls.append((url, kwargs))
        if isinstance(self.response, Exception):
            raise self.response
        return self.response

    def _cpe(self, url='http://127.0.0.1:7547/cr', rotated=True):
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
        self.assertEqual(url, 'http://127.0.0.1:7547/cr')
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
        requester = self._requester(ReachConfig(), resolver=lambda host: '10.9.8.7')
        outcome = requester.request(KEY)
        self.assertEqual((outcome.sent, outcome.reach.reason), (False, BEHIND_NAT))
        self.assertIn('resolves to 10.9.8.7', outcome.detail)
        self.assertEqual(self.calls, [])

        requester = self._requester(ReachConfig(), resolver=lambda host: None)
        self.clock.now += 1
        self.store.count_inform(KEY)
        outcome = requester.request(KEY)
        self.assertEqual((outcome.reach.reason, outcome.detail), (FAILED, 'cannot resolve cpe.carrier.example'))

        self.clock.now += 1
        self.store.count_inform(KEY)
        requester = self._requester(ReachConfig(), resolver=lambda host: '93.184.216.34')
        self.assertTrue(requester.request(KEY).sent)

    def test_always_mode_sends_to_any_url_without_resolving(self):
        self._cpe(url='http://cpe.carrier.example:7547/')
        requester = self._requester(
            ReachConfig(connection_request=CONNECTION_REQUEST_ALWAYS),
            resolver=lambda host: self.fail('resolved'),
        )
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
        self.requester = ConnectionRequester(self.reacher, self.store, 2.0)

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


if __name__ == '__main__':
    unittest.main()
