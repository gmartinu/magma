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
import http.client
import logging
import os
import threading
import unittest

import fakeredis
from magma.acsd.config import CwmpBind
from magma.acsd.digest import (
    DigestAuthenticator,
    Ha1,
    NonceStore,
    StaticCredentialProvider,
    ha1_of,
    parse_digest,
)
from magma.acsd.server import make_cwmp_server
from magma.acsd.session import CwmpSessionHandler
from magma.acsd.store import AcsStore

FIXTURES = os.path.join(os.path.dirname(__file__), 'fixtures')
XML = {'Content-Type': 'text/xml; charset=utf-8'}
REALM = 'magma-acs'
USER, PASSWORD = 'cpe', 's3cret-bootstrap'
IMSI = 'IMSI001010000000001'


def _fixture(name):
    with open(os.path.join(FIXTURES, name), 'rb') as f:
        return f.read()


class Clock:
    def __init__(self):
        self.now = 1000.0

    def __call__(self):
        return self.now


class DigestClient:
    """A CPE side of Digest, computed independently of acsd's code."""

    def __init__(self, user=USER, password=PASSWORD, algorithm='MD5'):
        self.user, self.password, self.algorithm = user, password, algorithm
        self.nonce = None
        self.nc = 0

    def take_challenge(self, www_authenticate):
        self.nonce = parse_digest(www_authenticate.partition(' ')[2])['nonce']
        self.nc = 0

    def _h(self, s):
        new = hashlib.sha256 if self.algorithm == 'SHA-256' else hashlib.md5
        return new(s.encode()).hexdigest()

    def header(self, method='POST', uri='/', nc=None, qop='auth'):
        self.nc = self.nc + 1 if nc is None else nc
        nc_hex, cnonce = '%08x' % self.nc, 'c0ffee'
        ha1 = self._h('%s:%s:%s' % (self.user, REALM, self.password))
        ha2 = self._h('%s:%s' % (method, uri))
        if qop:
            resp = self._h(':'.join((ha1, self.nonce, nc_hex, cnonce, qop, ha2)))
            extra = ', qop=%s, nc=%s, cnonce="%s"' % (qop, nc_hex, cnonce)
        else:
            resp = self._h('%s:%s:%s' % (ha1, self.nonce, ha2))
            extra = ''
        return (
            'Digest username="%s", realm="%s", nonce="%s", uri="%s", '
            'response="%s", algorithm=%s%s' % (
                self.user, REALM, self.nonce, uri, resp, self.algorithm, extra,
            )
        )


def _authenticator(clock=None, ttl=300):
    nonces = NonceStore(ttl, clock=clock) if clock else NonceStore(ttl)
    return DigestAuthenticator(
        REALM, StaticCredentialProvider(USER, PASSWORD), nonces,
    )


class ParseDigestTest(unittest.TestCase):
    def test_quoted_commas_and_escapes(self):
        p = parse_digest(
            'username="a,b", realm="r\\"x", nc=00000001 ,qop=auth, uri="/"',
        )
        self.assertEqual(p, {
            'username': 'a,b', 'realm': 'r"x', 'nc': '00000001',
            'qop': 'auth', 'uri': '/',
        })


class DigestAuthenticatorTest(unittest.TestCase):
    def setUp(self):
        self.clock = Clock()
        self.auth = _authenticator(self.clock)
        self.client = DigestClient()
        self.client.take_challenge(self.auth.challenge())

    def _check(self, header, uri='/'):
        return self.auth.authenticate('POST', uri, header, '10.0.0.2')

    def test_challenge(self):
        challenge = self.auth.challenge(stale=True)
        p = parse_digest(challenge.partition(' ')[2])
        self.assertTrue(challenge.startswith('Digest '))
        self.assertEqual(
            (p['realm'], p['qop'], p['algorithm'], p['stale']),
            (REALM, 'auth', 'MD5', 'true'),
        )
        self.assertNotIn('stale', self.auth.challenge())

    def test_round_trip_and_increasing_nc(self):
        for _ in range(3):
            result = self._check(self.client.header())
            self.assertTrue(result.ok, result.reason)
            self.assertEqual(result.username, USER)

    def test_no_credentials(self):
        result = self._check(None)
        self.assertEqual((result.ok, result.reason), (False, 'no credentials'))

    def test_basic_is_refused(self):
        self.assertFalse(self._check('Basic Y3BlOnMzY3JldA==').ok)

    def test_wrong_password(self):
        self.client.password = 'nope'
        result = self._check(self.client.header())
        self.assertEqual(result.reason, 'wrong response')
        self.assertFalse(result.stale)

    def test_unknown_username(self):
        client = DigestClient(user='other')
        client.nonce = self.client.nonce
        self.assertEqual(self._check(client.header()).reason, 'unknown username')

    def test_expired_nonce_is_stale(self):
        self.clock.now += 301
        result = self._check(self.client.header())
        self.assertEqual((result.reason, result.stale), ('expired nonce', True))

    def test_nonce_from_before_restart_is_stale(self):
        self.client.nonce = 'issued-by-a-previous-acsd'
        result = self._check(self.client.header())
        self.assertEqual((result.reason, result.stale), ('unknown nonce', True))

    def test_replayed_nc(self):
        header = self.client.header()
        self.assertTrue(self._check(header).ok)
        result = self._check(header)
        self.assertEqual(result.reason, 'replayed nonce-count')
        self.assertTrue(result.stale)
        self.assertFalse(self._check(self.client.header(nc=1)).ok)

    def test_without_qop_a_nonce_is_single_use(self):
        self.assertTrue(self._check(self.client.header(qop=None)).ok)
        result = self._check(self.client.header(qop=None))
        self.assertEqual((result.reason, result.stale), ('unknown nonce', True))

    def test_sha256(self):
        client = DigestClient(algorithm='SHA-256')
        client.nonce = self.client.nonce
        self.assertTrue(self._check(client.header()).ok)

    def test_stored_ha1_stands_for_the_password(self):
        class Ha1Provider:
            def lookup(self, username, source_ip):
                return ha1_of(username, REALM, PASSWORD) if username == USER else None

        auth = DigestAuthenticator(REALM, Ha1Provider(), NonceStore(300))
        client = DigestClient()
        client.take_challenge(auth.challenge())
        self.assertTrue(auth.authenticate('POST', '/', client.header(), '10.0.0.2').ok)
        wrong = DigestClient(password='other')
        wrong.take_challenge(auth.challenge())
        self.assertEqual(
            auth.authenticate('POST', '/', wrong.header(), '10.0.0.2').reason,
            'wrong response',
        )
        sha = DigestClient(algorithm='SHA-256')
        sha.take_challenge(auth.challenge())
        self.assertEqual(
            auth.authenticate('POST', '/', sha.header(), '10.0.0.2').reason,
            'stored credential needs MD5',
        )

    def test_ha1_of(self):
        expected = hashlib.md5(b'u:r:p').hexdigest()
        self.assertEqual(ha1_of('u', 'r', 'p'), expected)
        self.assertIsInstance(ha1_of('u', 'r', 'p'), Ha1)

    def test_unsupported_parameters(self):
        header = self.client.header()
        cases = {
            'wrong realm': header.replace(REALM, 'other'),
            'unsupported qop': header.replace('qop=auth', 'qop=auth-int'),
            'unsupported algorithm': header.replace('MD5', 'MD5-sess'),
            'missing response': header.replace('response=', 'x='),
        }
        for reason, bad in cases.items():
            self.assertEqual(self._check(bad).reason, reason)

    def test_uri_must_match(self):
        self.assertEqual(
            self._check(self.client.header(uri='/other')).reason,
            'uri does not match the request',
        )
        absolute = self.client.header(uri='http://10.1.0.1:48081/')
        self.assertTrue(self._check(absolute).ok)

    def test_no_bootstrap_credential_refuses_everyone(self):
        auth = DigestAuthenticator(
            REALM, StaticCredentialProvider('', ''), NonceStore(300),
        )
        client = DigestClient(user='', password='')
        client.take_challenge(auth.challenge())
        result = auth.authenticate('POST', '/', client.header(), '10.0.0.2')
        self.assertFalse(result.ok)


class _Listener(unittest.TestCase):
    """Digest through the real listener, with the replayed captures."""
    authenticated = True
    known_ips = ('127.0.0.1',)

    def setUp(self):
        self.identify_calls = 0

        def identify(source_ip, inform):
            self.identify_calls += 1
            return IMSI if source_ip in self.known_ips else None

        self.clock = Clock()
        self.handler = CwmpSessionHandler(
            identify, store=AcsStore(fakeredis.FakeStrictRedis()),
        )
        self.server = make_cwmp_server(
            CwmpBind('lo', '127.0.0.1', 0), self.handler, workers=2,
            authenticator=_authenticator(self.clock)
            if self.authenticated else None,
        )
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.conn = self._conn()
        self.client = DigestClient()

    def tearDown(self):
        self.conn.close()
        self.server.shutdown()
        self.server.server_close()

    def _conn(self):
        return http.client.HTTPConnection(
            '127.0.0.1', self.server.server_address[1], timeout=5,
        )

    def _post(self, body, auth=None, conn=None):
        headers = dict(XML)
        if auth:
            headers['Authorization'] = auth
        conn = conn or self.conn
        conn.request('POST', '/', body, headers)
        resp = conn.getresponse()
        return resp, resp.read()

    def _challenged_inform(self):
        resp, body = self._post(_fixture('sim4000_inform.xml'))
        self.assertEqual((resp.status, body), (401, b''))
        self.client.take_challenge(resp.getheader('WWW-Authenticate'))
        return resp


class ListenerDigestTest(_Listener):
    def test_challenge_then_session_on_the_same_connection(self):
        self._challenged_inform()
        resp, body = self._post(
            _fixture('sim4000_inform.xml'), self.client.header(),
        )
        self.assertEqual(resp.status, 200)
        self.assertIn(b'InformResponse', body)
        self.assertEqual(self.handler.session_identity('127.0.0.1'), IMSI)
        # The CPE sends no credentials on the rest of the session.
        resp, _ = self._post(b'')
        self.assertEqual(resp.status, 204)

    def test_new_connection_must_authenticate_again(self):
        self._challenged_inform()
        self._post(_fixture('sim4000_inform.xml'), self.client.header())
        other = self._conn()
        try:
            resp, _ = self._post(b'', conn=other)
        finally:
            other.close()
        self.assertEqual(resp.status, 401)

    def test_wrong_password_never_logged(self):
        self._challenged_inform()
        self.client.password = 'wrong-guess'
        with self.assertLogs(level=logging.INFO) as logs:
            resp, _ = self._post(
                _fixture('sim4000_inform.xml'), self.client.header(),
            )
        self.assertEqual(resp.status, 401)
        self.assertNotIn('stale', resp.getheader('WWW-Authenticate'))
        output = '\n'.join(logs.output)
        self.assertIn('wrong response', output)
        self.assertNotIn(PASSWORD, output)
        self.assertNotIn('wrong-guess', output)
        self.assertEqual(self.identify_calls, 0)

    def test_stale_nonce(self):
        self._challenged_inform()
        self.clock.now += 301
        resp, _ = self._post(
            _fixture('sim4000_inform.xml'), self.client.header(),
        )
        self.assertEqual(resp.status, 401)
        self.assertIn('stale=true', resp.getheader('WWW-Authenticate'))
        self.client.take_challenge(resp.getheader('WWW-Authenticate'))
        resp, _ = self._post(
            _fixture('sim4000_inform.xml'), self.client.header(),
        )
        self.assertEqual(resp.status, 200)

    def test_replayed_request_refused(self):
        self._challenged_inform()
        header = self.client.header()
        self._post(_fixture('sim4000_inform.xml'), header)
        replay = self._conn()
        try:
            resp, _ = self._post(
                _fixture('sim4000_inform.xml'), header, conn=replay,
            )
        finally:
            replay.close()
        self.assertEqual(resp.status, 401)
        self.assertEqual(self.identify_calls, 1)

    def test_known_ip_without_credentials_is_401(self):
        self._challenged_inform()
        self.assertEqual(self.identify_calls, 0)
        self.assertIsNone(self.handler.session_identity('127.0.0.1'))


class ListenerDigestUnknownIpTest(_Listener):
    known_ips = ()

    def test_valid_digest_unknown_ip_is_403(self):
        self._challenged_inform()
        resp, body = self._post(
            _fixture('sim4000_inform.xml'), self.client.header(),
        )
        self.assertEqual((resp.status, body), (403, b''))
        self.assertEqual(self.identify_calls, 1)


class ListenerAuthOffTest(_Listener):
    authenticated = False

    def test_no_challenge(self):
        resp, body = self._post(_fixture('sim4000_inform.xml'))
        self.assertEqual(resp.status, 200)
        self.assertIn(b'InformResponse', body)
        self.assertIsNone(resp.getheader('WWW-Authenticate'))


if __name__ == '__main__':
    unittest.main()
