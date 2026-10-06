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
import os
import shutil
import ssl
import subprocess
import tempfile
import unittest
import xml.etree.ElementTree as ET
from unittest import mock

import fakeredis
from lte.protos.mconfig import mconfigs_pb2
from magma.acsd import main
from magma.acsd.claimed import ClaimedMode
from magma.acsd.claims import ClaimRegistry
from magma.acsd.config import (
    AUTH_OFF,
    DEFAULT_WAN_PORT,
    CwmpAuthConfig,
    CwmpBind,
    CwmpWanConfig,
    get_cwmp_auth,
    get_cwmp_wan,
)
from magma.acsd.credentials import CredentialStore
from magma.acsd.digest import parse_digest
from magma.acsd.session import CwmpSessionHandler
from magma.acsd.store import AcsStore

FIXTURES = os.path.join(os.path.dirname(__file__), 'fixtures')
XML = {'Content-Type': 'text/xml; charset=utf-8'}
REALM = 'magma-acs'
BOOT_USER, BOOT_PASSWORD = 'bootstrap', 'boot-secret'
IMSI = 'IMSI001010000000001'
KEY = 'CLAIMsim-1'
MS = 'Device.ManagementServer.'


def _fixture(name):
    with open(os.path.join(FIXTURES, name), 'rb') as f:
        return f.read()


class WanConfigTest(unittest.TestCase):
    def test_off_by_default_and_inherits_cwmp_auth(self):
        auth = CwmpAuthConfig(realm='r', username='u', password='p', nonce_ttl_secs=60)
        wan = get_cwmp_wan({}, auth)
        self.assertFalse(wan.enabled)
        self.assertEqual(wan.bind, CwmpBind('eth0', '0.0.0.0', DEFAULT_WAN_PORT))
        self.assertEqual(
            (wan.realm, wan.bootstrap_username, wan.bootstrap_password,
             wan.nonce_ttl_secs),
            ('r', 'u', 'p', 60),
        )

    def test_from_yml(self):
        wan = get_cwmp_wan(
            {'cwmp_wan': {
                'enabled': True, 'interface': 'wan0', 'address': '192.0.2.1',
                'port': 9443, 'cert_path': '/c', 'key_path': '/k',
                'bootstrap_username': 'b', 'bootstrap_password': 'bp',
            }},
            get_cwmp_auth({'cwmp_auth': {'mode': 'off', 'username': 'u'}}),
        )
        self.assertTrue(wan.enabled)
        self.assertEqual(wan.bind, CwmpBind('wan0', '192.0.2.1', 9443))
        self.assertEqual((wan.cert_path, wan.key_path), ('/c', '/k'))
        self.assertEqual((wan.bootstrap_username, wan.bootstrap_password), ('b', 'bp'))
        self.assertNotIn('bp', repr(wan))

    def test_only_a_real_true_enables_it(self):
        for value in ('yes', 'true', 1, None):
            wan = get_cwmp_wan({'cwmp_wan': {'enabled': value}}, CwmpAuthConfig())
            self.assertFalse(wan.enabled, value)


def _openssl_cert(directory):
    cert, key = os.path.join(directory, 'c.pem'), os.path.join(directory, 'k.pem')
    subprocess.run(
        [
            'openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
            '-keyout', key, '-out', cert, '-days', '1', '-subj', '/CN=127.0.0.1',
        ],
        check=True, capture_output=True,
    )
    return cert, key


class DigestCpe:
    """The CPE side of Digest, independent of acsd's code."""

    def __init__(self, user, password):
        self.user, self.password, self.nonce, self.nc = user, password, '', 0

    def take(self, www_authenticate):
        self.nonce = parse_digest(www_authenticate.partition(' ')[2])['nonce']
        self.nc = 0

    def header(self, uri='/'):
        def h(s):
            return hashlib.md5(s.encode()).hexdigest()
        self.nc += 1
        nc = '%08x' % self.nc
        ha1 = h('%s:%s:%s' % (self.user, REALM, self.password))
        resp = h(':'.join((ha1, self.nonce, nc, 'abc', 'auth', h('POST:' + uri))))
        return (
            'Digest username="%s", realm="%s", nonce="%s", uri="%s", '
            'response="%s", qop=auth, nc=%s, cnonce="abc"' % (
                self.user, REALM, self.nonce, uri, resp, nc,
            )
        )


@unittest.skipUnless(shutil.which('openssl'), 'needs openssl to make a test cert')
class WanListenerTest(unittest.TestCase):
    """Both listeners up on one handler, the claimed one over HTTPS."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.mkdtemp()
        cls.cert, cls.key = _openssl_cert(cls.tmp)

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.tmp, ignore_errors=True)

    def setUp(self):
        redis = fakeredis.FakeStrictRedis()
        self.store = AcsStore(redis)
        self.claims = ClaimRegistry(redis)
        self.claims.add('00A1B2', 'SIM4000', 'SIM0001', claim_id='sim-1')
        self.creds = CredentialStore(redis, REALM)
        claimed = ClaimedMode(
            self.claims, self.creds, self.store, BOOT_USER, BOOT_PASSWORD,
        )
        self.handler = CwmpSessionHandler(
            lambda ip, inform: IMSI, store=self.store, claimed=claimed,
        )
        cwmp = main.make_cwmp_wsgi(self.handler)
        self.threads = [
            main.start_cwmp_listener(
                CwmpBind('lo', '127.0.0.1', 0), self.handler, 2,
                main.make_authenticator(CwmpAuthConfig(mode=AUTH_OFF)), cwmp=cwmp,
            ),
        ]
        wan = CwmpWanConfig(
            enabled=True, bind=CwmpBind('lo', '127.0.0.1', 0),
            cert_path=self.cert, key_path=self.key,
        )
        self.threads.append(
            main.start_wan_listener(wan, self.handler, claimed, 2, cwmp),
        )
        self.client_tls = ssl.create_default_context(cafile=self.cert)
        self.client_tls.check_hostname = False

    def tearDown(self):
        with mock.patch.object(main._thread, 'interrupt_main'):
            for thread in self.threads:
                thread.server.shutdown()
                thread.join(5)
                thread.server.server_close()

    def _https(self):
        return http.client.HTTPSConnection(
            '127.0.0.1', self.threads[1].server.server_address[1],
            timeout=5, context=self.client_tls,
        )

    @staticmethod
    def _post(conn, body, auth=None):
        headers = dict(XML)
        if auth:
            headers['Authorization'] = auth
        conn.request('POST', '/', body, headers)
        resp = conn.getresponse()
        return resp, resp.read()

    def _open_session(self, cpe):
        conn = self._https()
        resp, _ = self._post(conn, _fixture('sim4000_inform.xml'))
        self.assertEqual(resp.status, 401)
        cpe.take(resp.getheader('WWW-Authenticate'))
        resp, body = self._post(conn, _fixture('sim4000_inform.xml'), cpe.header())
        return conn, resp, body

    def test_bootstrap_then_rotated_credential_over_https(self):
        conn, resp, body = self._open_session(DigestCpe(BOOT_USER, BOOT_PASSWORD))
        self.assertEqual(resp.status, 200)
        self.assertIn(b'InformResponse', body)
        resp, body = self._post(conn, b'')
        self.assertEqual(resp.status, 200)
        values = {
            p.findtext('Name'): p.findtext('Value')
            for p in ET.fromstring(body).iter('ParameterValueStruct')
        }
        username, password = values[MS + 'Username'], values[MS + 'Password']
        resp, _ = self._post(conn, (
            '<soap-env:Envelope xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/"'
            ' xmlns:cwmp="urn:dslforum-org:cwmp-1-0"><soap-env:Header>'
            '<cwmp:ID soap-env:mustUnderstand="1">1</cwmp:ID></soap-env:Header>'
            '<soap-env:Body><cwmp:SetParameterValuesResponse><Status>0</Status>'
            '</cwmp:SetParameterValuesResponse></soap-env:Body></soap-env:Envelope>'
        ).encode())
        self.assertEqual(resp.status, 204)
        conn.close()
        self.assertTrue(self.creds.get(KEY).rotated)

        # The bootstrap credential is right but no longer this CPE's.
        conn, resp, _ = self._open_session(DigestCpe(BOOT_USER, BOOT_PASSWORD))
        self.assertEqual(resp.status, 403)
        conn.close()
        conn, resp, body = self._open_session(DigestCpe(username, password))
        self.assertEqual(resp.status, 200)
        self.assertIn(b'InformResponse', body)
        conn.close()

    def test_session_cookie_survives_a_reconnect(self):
        cpe = DigestCpe(BOOT_USER, BOOT_PASSWORD)
        conn, resp, _ = self._open_session(cpe)
        self.assertEqual(resp.status, 200)
        cookie = resp.getheader('Set-Cookie').split(';')[0]
        self.assertIn('Secure', resp.getheader('Set-Cookie'))
        conn.close()
        conn = self._https()
        headers = dict(XML, Cookie=cookie)
        conn.request('POST', '/', b'', headers)
        resp = conn.getresponse()
        resp.read()
        self.assertEqual(resp.status, 401)
        cpe.take(resp.getheader('WWW-Authenticate'))
        conn.request('POST', '/', b'', dict(headers, Authorization=cpe.header()))
        resp = conn.getresponse()
        body = resp.read()
        conn.close()
        self.assertEqual(resp.status, 200)
        self.assertIn(b'SetParameterValues', body)

    def test_wrong_password_never_reaches_the_session(self):
        conn, resp, _ = self._open_session(DigestCpe(BOOT_USER, 'nope'))
        self.assertEqual(resp.status, 401)
        conn.close()
        self.assertEqual(self.store.list_sessions(), [])

    def test_plain_http_on_the_tls_port_fails(self):
        conn = http.client.HTTPConnection(
            '127.0.0.1', self.threads[1].server.server_address[1], timeout=5,
        )
        with self.assertRaises((ConnectionError, http.client.HTTPException, OSError)):
            self._post(conn, b'')
        conn.close()

    def test_core_listener_still_names_the_cpe_by_imsi(self):
        conn = http.client.HTTPConnection(
            '127.0.0.1', self.threads[0].server.server_address[1], timeout=5,
        )
        resp, body = self._post(conn, _fixture('sim4000_inform.xml'))
        self.assertEqual(resp.status, 200)
        conn.close()
        [session] = self.store.list_sessions()
        self.assertEqual((session.cpe_key, session.mode), (IMSI, 'core'))
        self.assertIsNone(self.creds.owner(KEY + '.1'))

    def test_bad_cert_keeps_the_wan_listener_down(self):
        wan = CwmpWanConfig(enabled=True, cert_path='/nonexistent', key_path='/x')
        with mock.patch.object(main, 'start_cwmp_listener') as listen:
            self.assertIsNone(
                main.start_wan_listener(wan, self.handler, mock.Mock(), 1, None),
            )
        listen.assert_not_called()


class ClaimedModeForgetTest(unittest.TestCase):
    def test_removing_a_claim_forgets_through_the_live_store(self):
        redis = fakeredis.FakeStrictRedis()
        store = AcsStore(redis)
        mode = main.make_claimed_mode(CwmpWanConfig(enabled=True), store, redis)
        mode.claims.add('00A1B2', 'P', 'SN1', claim_id='t1')
        store.create_task('CLAIMt1', 'reboot')
        cred, _ = mode.credentials.begin_rotation('CLAIMt1', 16)
        mode.credentials.promote('CLAIMt1', cred.pending_generation)
        mode.claims.remove('t1')
        self.assertEqual(store.list_tasks('CLAIMt1'), [])
        self.assertFalse(mode.credentials.get('CLAIMt1').rotated)


class MainWanWiringTest(unittest.TestCase):
    def _run_main(self, config):
        service = mock.Mock(mconfig=mconfigs_pb2.AcsD())
        redis = fakeredis.FakeStrictRedis()
        with mock.patch.object(main, 'MagmaService', return_value=service), \
                mock.patch.object(main, 'sentry_init'), \
                mock.patch.object(main, 'load_service_config', return_value=config), \
                mock.patch.object(main, 'get_default_client', return_value=redis), \
                mock.patch.object(main, 'start_cwmp_listener') as listen, \
                mock.patch.object(main, 'start_wan_listener') as wan_listen:
            main.main()
        return listen, wan_listen

    def test_wan_off_by_default(self):
        listen, wan_listen = self._run_main({})
        wan_listen.assert_not_called()
        handler = listen.call_args.args[1]
        self.assertIsNone(handler._claimed)

    def test_wan_on_shares_handler_and_app(self):
        listen, wan_listen = self._run_main({
            'cwmp_auth': {'username': 'b', 'password': 'bp'},
            'cwmp_wan': {'enabled': True},
        })
        wan, handler, claimed, _, cwmp = wan_listen.call_args.args
        self.assertTrue(wan.enabled)
        self.assertIs(handler, listen.call_args.args[1])
        self.assertIs(handler._claimed, claimed)
        self.assertIs(cwmp, listen.call_args.kwargs['cwmp'])
        self.assertEqual(claimed.lookup('b', '192.0.2.1'), 'bp')

    def test_body_limit_reaches_both_listeners(self):
        listen, wan_listen = self._run_main({
            'cwmp_auth': {'username': 'b', 'password': 'bp'},
            'cwmp_wan': {'enabled': True},
            'cwmp_max_body_bytes': 2048,
        })
        self.assertEqual(listen.call_args.kwargs['max_body'], 2048)
        self.assertEqual(wan_listen.call_args.kwargs['max_body'], 2048)


if __name__ == '__main__':
    unittest.main()
