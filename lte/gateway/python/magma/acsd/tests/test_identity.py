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

import ipaddress
import unittest

import grpc
from lte.protos.mobilityd_pb2 import IPAddress
from lte.protos.subscriberdb_pb2 import SubscriberID
from magma.acsd.identity import CpeIdentifier
from magma.acsd.mobilityd_client import (
    MobilitydClient,
    MobilitydUnavailable,
    to_ip_pb,
)


class FakeRpcError(grpc.RpcError):
    def __init__(self, code, details=''):
        super().__init__()
        self._code = code
        self._details = details

    def code(self):
        return self._code

    def details(self):
        return self._details


class FakeMobilityStub:
    """Answers GetSubscriberIDFromIP from an ip -> imsi-digits table."""

    def __init__(self, table=None, error=None):
        self.table = table or {}
        self.error = error
        self.calls = []

    def GetSubscriberIDFromIP(self, request, timeout=None):
        self.calls.append((request, timeout))
        if self.error is not None:
            raise self.error
        ip = str(ipaddress.ip_address(request.address))
        if ip not in self.table:
            raise FakeRpcError(grpc.StatusCode.NOT_FOUND, 'not found')
        return SubscriberID(id=self.table[ip], type=SubscriberID.IMSI)


def client_for(stub, timeout=5):
    return MobilitydClient(stub_factory=lambda: stub, timeout=timeout)


class ToIpPbTest(unittest.TestCase):
    def test_ipv4(self):
        pb = to_ip_pb('10.1.0.5')
        self.assertEqual(pb.version, IPAddress.IPV4)
        self.assertEqual(pb.address, bytes([10, 1, 0, 5]))

    def test_ipv4_mapped_ipv6_is_unwrapped(self):
        self.assertEqual(to_ip_pb('::ffff:10.1.0.5'), to_ip_pb('10.1.0.5'))

    def test_ipv6(self):
        pb = to_ip_pb('fdee::5')
        self.assertEqual(pb.version, IPAddress.IPV6)
        self.assertEqual(len(pb.address), 16)

    def test_garbage_raises(self):
        with self.assertRaises(ValueError):
            to_ip_pb('not-an-ip')


class MobilitydClientTest(unittest.TestCase):
    def test_known_ip(self):
        stub = FakeMobilityStub({'10.1.0.5': '001010000000001'})
        imsi = client_for(stub, timeout=3).get_imsi_for_ip('10.1.0.5')
        self.assertEqual(imsi, 'IMSI001010000000001')
        self.assertEqual(stub.calls[0][1], 3)

    def test_unknown_ip_is_none(self):
        stub = FakeMobilityStub({})
        self.assertIsNone(client_for(stub).get_imsi_for_ip('10.1.0.9'))

    def test_empty_sid_is_none(self):
        stub = FakeMobilityStub({'10.1.0.5': ''})
        self.assertIsNone(client_for(stub).get_imsi_for_ip('10.1.0.5'))

    def test_rpc_error_raises_unavailable(self):
        stub = FakeMobilityStub(
            error=FakeRpcError(grpc.StatusCode.UNAVAILABLE, 'down'),
        )
        with self.assertRaises(MobilitydUnavailable):
            client_for(stub).get_imsi_for_ip('10.1.0.5')

    def test_no_channel_raises_unavailable(self):
        def no_channel():
            raise ValueError('mobilityd not in service registry')
        client = MobilitydClient(stub_factory=no_channel)
        with self.assertRaises(MobilitydUnavailable):
            client.get_imsi_for_ip('10.1.0.5')


class CpeIdentifierTest(unittest.TestCase):
    def test_known_ip_resolves_imsi(self):
        stub = FakeMobilityStub({'10.1.0.5': '001010000000001'})
        identify = CpeIdentifier(client_for(stub))
        self.assertEqual(identify('10.1.0.5'), 'IMSI001010000000001')
        self.assertEqual(len(stub.calls), 1)

    def test_unknown_ip_refused(self):
        identify = CpeIdentifier(client_for(FakeMobilityStub({})))
        with self.assertLogs(level='WARNING'):
            self.assertIsNone(identify('10.1.0.9'))

    def test_mobilityd_error_refused(self):
        for code in (
            grpc.StatusCode.UNAVAILABLE,
            grpc.StatusCode.DEADLINE_EXCEEDED,
            grpc.StatusCode.INTERNAL,
        ):
            stub = FakeMobilityStub(error=FakeRpcError(code))
            identify = CpeIdentifier(client_for(stub))
            with self.assertLogs(level='ERROR'):
                self.assertIsNone(identify('10.1.0.5'), code)

    def test_mobilityd_without_channel_refused(self):
        def no_channel():
            raise ValueError('mobilityd not in service registry')
        identify = CpeIdentifier(MobilitydClient(stub_factory=no_channel))
        with self.assertLogs(level='ERROR'):
            self.assertIsNone(identify('10.1.0.5'))

    def test_bad_source_ip_refused_without_rpc(self):
        stub = FakeMobilityStub({'10.1.0.5': '001010000000001'})
        identify = CpeIdentifier(client_for(stub))
        with self.assertLogs(level='WARNING'):
            self.assertIsNone(identify('garbage'))
        self.assertEqual(stub.calls, [])


if __name__ == '__main__':
    unittest.main()
