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
import ipaddress
import os
import threading
import unittest

import fakeredis
import grpc
from lte.protos.subscriberdb_pb2 import SubscriberID
from magma.acsd.bindings import BindingChangeKind, SerialBinder
from magma.acsd.config import CwmpBind
from magma.acsd.identity import CpeIdentifier, SessionIdentifier
from magma.acsd.mobilityd_client import MobilitydClient
from magma.acsd.server import make_cwmp_server
from magma.acsd.session import CwmpSessionHandler
from magma.acsd.store import AcsStore
from magma.tr069 import models


def memory_store():
    return AcsStore(fakeredis.FakeStrictRedis())


FIXTURES = os.path.join(os.path.dirname(__file__), 'fixtures')
XML = {'Content-Type': 'text/xml; charset=utf-8'}
IMSI_DIGITS = '001010000000001'
IMSI = 'IMSI' + IMSI_DIGITS


class NotFound(grpc.RpcError):
    def code(self):
        return grpc.StatusCode.NOT_FOUND

    def details(self):
        return 'not found'


class FakeMobilityStub:
    def __init__(self, table):
        self.table = table
        self.calls = 0

    def GetSubscriberIDFromIP(self, request, timeout=None):
        self.calls += 1
        ip = str(ipaddress.ip_address(request.address))
        if ip not in self.table:
            raise NotFound()
        return SubscriberID(id=self.table[ip], type=SubscriberID.IMSI)


def session_identifier(table, on_change=None):
    stub = FakeMobilityStub(table)
    client = MobilitydClient(stub_factory=lambda: stub)
    binder = SerialBinder(on_change=on_change) if on_change else SerialBinder()
    return SessionIdentifier(CpeIdentifier(client), binder), stub


def _inform(serial='SIM0001'):
    return models.Inform(
        DeviceId=models.DeviceIdStruct(SerialNumber=serial), MaxEnvelopes=1,
    )


def _fixture(name):
    with open(os.path.join(FIXTURES, name), 'rb') as f:
        return f.read()


class SessionIdentifierTest(unittest.TestCase):
    def test_known_ip_returns_imsi(self):
        identify, stub = session_identifier({'10.1.0.5': IMSI_DIGITS})
        self.assertEqual(identify('10.1.0.5', _inform()), IMSI)
        self.assertEqual(stub.calls, 1)

    def test_unknown_ip_refused(self):
        identify, _ = session_identifier({})
        self.assertIsNone(identify('10.1.0.5', _inform()))

    def test_inform_without_serial_refused(self):
        changes = []
        identify, _ = session_identifier(
            {'10.1.0.5': IMSI_DIGITS}, changes.append,
        )
        self.assertIsNone(identify('10.1.0.5', models.Inform()))
        self.assertIsNone(identify('10.1.0.5', _inform(serial='')))
        self.assertEqual(changes, [])

    def test_serial_change_reported(self):
        changes = []
        identify, _ = session_identifier(
            {'10.1.0.5': IMSI_DIGITS}, changes.append,
        )
        identify('10.1.0.5', _inform('SIM0001'))
        self.assertEqual(changes, [])
        self.assertEqual(identify('10.1.0.5', _inform('SIM0002')), IMSI)
        self.assertEqual(len(changes), 1)
        self.assertEqual(changes[0].kind, BindingChangeKind.SERIAL_CHANGED)
        self.assertEqual(changes[0].previous, 'SIM0001')


class _ListenerHarness(unittest.TestCase):
    """The 1.4 replay captures through the real listener, mocked mobilityd."""
    table: dict = {}

    def setUp(self):
        self.changes = []
        identify, self.stub = session_identifier(
            self.table, self.changes.append,
        )
        self.handler = CwmpSessionHandler(identify, store=memory_store())
        self.server = make_cwmp_server(
            CwmpBind('lo', '127.0.0.1', 0), self.handler, workers=2,
        )
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.conn = http.client.HTTPConnection(
            '127.0.0.1', self.server.server_address[1], timeout=5,
        )

    def tearDown(self):
        self.conn.close()
        self.server.shutdown()
        self.server.server_close()

    def _post(self, body):
        self.conn.request('POST', '/', body, XML)
        resp = self.conn.getresponse()
        return resp, resp.read()


class ListenerIdentityTest(_ListenerHarness):
    table = {'127.0.0.1': IMSI_DIGITS}

    def test_known_ip_accepted_and_imsi_held(self):
        with self.assertLogs(level='INFO') as logs:
            resp, body = self._post(_fixture('sim4000_inform.xml'))
        self.assertEqual(resp.status, 200)
        self.assertIn(b'InformResponse', body)
        self.assertEqual(self.handler.session_identity('127.0.0.1'), IMSI)
        self.assertTrue(any(IMSI in line for line in logs.output))
        resp, _ = self._post(b'')
        self.assertEqual(resp.status, 204)
        self.assertIsNone(self.handler.session_identity('127.0.0.1'))
        self.assertEqual(self.stub.calls, 1)

    def test_binding_change_reported(self):
        self._post(_fixture('sim4000_inform.xml'))
        self._post(b'')
        resp, _ = self._post(_fixture('synthetic_periodic_inform.xml'))
        self.assertEqual(resp.status, 200)
        self.assertEqual(len(self.changes), 1)
        change = self.changes[0]
        self.assertEqual(change.kind, BindingChangeKind.SERIAL_CHANGED)
        self.assertEqual(
            (change.imsi, change.serial, change.previous),
            (IMSI, 'SYN0000042', 'SIM0001'),
        )


class ListenerUnknownIpTest(_ListenerHarness):
    def test_unknown_ip_is_403(self):
        resp, body = self._post(_fixture('sim4000_inform.xml'))
        self.assertEqual(resp.status, 403)
        self.assertEqual(body, b'')
        self.assertIsNone(self.handler.session_identity('127.0.0.1'))
        self.assertEqual(self.changes, [])


if __name__ == '__main__':
    unittest.main()
