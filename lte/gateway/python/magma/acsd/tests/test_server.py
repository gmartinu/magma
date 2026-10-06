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

import http.client
import os
import socket
import threading
import unittest
import xml.etree.ElementTree as ET

import fakeredis
from magma.acsd.config import CwmpBind
from magma.acsd.server import make_cwmp_server
from magma.acsd.session import CwmpSessionHandler
from magma.acsd.store import AcsStore


def memory_store():
    return AcsStore(fakeredis.FakeStrictRedis())


# sim4000_* are the request bodies of the simulator session replayed by the
# Go codec tests (acs/cloud/go/services/acs/cwmp/testdata/captures).
FIXTURES = os.path.join(os.path.dirname(__file__), 'fixtures')
SOAP_NS = 'http://schemas.xmlsoap.org/soap/envelope/'
CWMP_NS = 'urn:dslforum-org:cwmp-1-0'
XML = {'Content-Type': 'text/xml; charset=utf-8'}


def _fixture(name):
    with open(os.path.join(FIXTURES, name), 'rb') as f:
        return f.read()


class CwmpListenerTest(unittest.TestCase):
    identify = None

    def setUp(self):
        handler = CwmpSessionHandler(self.identify, store=memory_store()) if self.identify \
            else CwmpSessionHandler(store=memory_store())
        self.server = make_cwmp_server(
            CwmpBind('lo', '127.0.0.1', 0), handler, workers=4,
        )
        self.port = self.server.server_address[1]
        self.thread = threading.Thread(
            target=self.server.serve_forever, daemon=True,
        )
        self.thread.start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()

    def _conn(self):
        return http.client.HTTPConnection('127.0.0.1', self.port, timeout=5)

    @staticmethod
    def _post(conn, body):
        conn.request('POST', '/', body, XML)
        resp = conn.getresponse()
        return resp, resp.read()

    def _assert_inform_response(self, resp, body, cwmp_id):
        self.assertEqual(resp.status, 200)
        root = ET.fromstring(body)
        header_id = root.find('{%s}Header/{%s}ID' % (SOAP_NS, CWMP_NS))
        self.assertEqual(header_id.text, cwmp_id)
        self.assertEqual(header_id.get('{%s}mustUnderstand' % SOAP_NS), '1')
        inform_resp = root.find(
            '{%s}Body/{%s}InformResponse' % (SOAP_NS, CWMP_NS),
        )
        self.assertIsNotNone(inform_resp)
        # Only top-level CWMP elements carry the namespace.
        self.assertEqual(inform_resp.find('MaxEnvelopes').text, '1')

    def _assert_session_end(self, resp, body):
        self.assertEqual(resp.status, 204)
        self.assertEqual(body, b'')
        self.assertEqual(resp.getheader('Content-Length'), '0')


class CaptureReplayTest(CwmpListenerTest):
    def test_sim4000_session(self):
        conn = self._conn()
        resp, body = self._post(conn, _fixture('sim4000_inform.xml'))
        self._assert_inform_response(resp, body, '1727697600000')
        # Same keep-alive connection, as a CPE sends it.
        self._assert_session_end(*self._post(conn, b''))
        conn.close()

    def test_unsolicited_rpc_response_ends_session(self):
        conn = self._conn()
        self._post(conn, _fixture('sim4000_inform.xml'))
        self._assert_session_end(
            *self._post(conn, _fixture('sim4000_gpv_response.xml')),
        )
        conn.close()

    def test_synthetic_periodic_inform(self):
        conn = self._conn()
        resp, body = self._post(conn, _fixture('synthetic_periodic_inform.xml'))
        self._assert_inform_response(resp, body, '42')
        self._assert_session_end(*self._post(conn, b''))
        conn.close()

    def test_empty_post_without_session(self):
        conn = self._conn()
        self._assert_session_end(*self._post(conn, b''))
        conn.close()

    def test_get_is_405(self):
        conn = self._conn()
        conn.request('GET', '/')
        resp = conn.getresponse()
        resp.read()
        self.assertEqual(resp.status, 405)
        self.assertEqual(resp.getheader('Allow'), 'POST')
        conn.close()

    def test_unsupported_cwmp_version_is_rejected(self):
        # The shared tr069 models only speak cwmp-1-0.
        body = _fixture('sim4000_inform.xml').replace(b'cwmp-1-0', b'cwmp-1-2')
        conn = self._conn()
        resp, _ = self._post(conn, body)
        self.assertEqual(resp.status, 500)
        conn.close()


class ConcurrencyTest(CwmpListenerTest):
    def test_stalled_cpe_does_not_block_others(self):
        stalled = socket.create_connection(('127.0.0.1', self.port))
        try:
            stalled.sendall(
                b'POST / HTTP/1.1\r\nHost: acs\r\nContent-Length: 2042\r\n\r\n',
            )
            conn = self._conn()
            resp, body = self._post(conn, _fixture('sim4000_inform.xml'))
            self._assert_inform_response(resp, body, '1727697600000')
            conn.close()
        finally:
            stalled.close()

    def test_parallel_sessions_get_their_own_ids(self):
        template = _fixture('sim4000_inform.xml')
        errors = []

        def session(i):
            try:
                body = template.replace(b'1727697600000', str(i).encode())
                conn = self._conn()
                resp, out = self._post(conn, body)
                self._assert_inform_response(resp, out, str(i))
                self._assert_session_end(*self._post(conn, b''))
                conn.close()
            except Exception as e:  # pylint: disable=broad-except
                errors.append(e)

        threads = [
            threading.Thread(target=session, args=(i,)) for i in range(12)
        ]
        for t in threads:
            t.start()
        for t in threads:
            t.join(10)
        self.assertEqual(errors, [])


class BodyLimitTest(unittest.TestCase):
    LIMIT = 4096

    def setUp(self):
        self.server = make_cwmp_server(
            CwmpBind('lo', '127.0.0.1', 0), CwmpSessionHandler(store=memory_store()),
            workers=2, max_body=self.LIMIT,
        )
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()

    def _raw(self, content_length):
        sock = socket.create_connection(('127.0.0.1', self.server.server_address[1]), timeout=5)
        # Headers only: a server that tried to read the body would hang.
        sock.sendall((
            'POST / HTTP/1.1\r\nHost: x\r\nContent-Type: text/xml\r\n'
            'Content-Length: %s\r\n\r\n' % content_length
        ).encode())
        return sock

    @staticmethod
    def _read_until_closed(sock):
        data = b''
        while True:
            chunk = sock.recv(4096)
            if not chunk:
                return data
            data += chunk

    def test_oversized_body_is_413_and_the_connection_closes(self):
        sock = self._raw(10 * 1024 * 1024)
        try:
            data = self._read_until_closed(sock)
        finally:
            sock.close()
        self.assertTrue(data.startswith(b'HTTP/1.1 413'), data)

    def test_bad_content_length_is_400(self):
        sock = self._raw('lots')
        try:
            data = self._read_until_closed(sock)
        finally:
            sock.close()
        self.assertTrue(data.startswith(b'HTTP/1.1 400'), data)

    def test_body_within_the_limit_is_served(self):
        conn = http.client.HTTPConnection('127.0.0.1', self.server.server_address[1], timeout=5)
        try:
            conn.request('POST', '/', _fixture('sim4000_inform.xml'), XML)
            resp = conn.getresponse()
            resp.read()
            self.assertEqual(resp.status, 200)
        finally:
            conn.close()


class RefusedSessionTest(CwmpListenerTest):
    @staticmethod
    def identify(source_ip, inform):
        return False

    def test_refused_inform_is_403(self):
        conn = self._conn()
        resp, body = self._post(conn, _fixture('sim4000_inform.xml'))
        self.assertEqual(resp.status, 403)
        self.assertEqual(body, b'')
        conn.close()

