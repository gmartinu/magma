"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Thin client for the one mobilityd RPC acsd needs: UE IP -> IMSI.
"""
import ipaddress
from typing import Callable, Optional

import grpc
from lte.protos.mobilityd_pb2 import IPAddress
from lte.protos.mobilityd_pb2_grpc import MobilityServiceStub
from lte.protos.subscriberdb_pb2 import SubscriberID
from magma.common.service_registry import ServiceRegistry

MOBILITYD_SERVICE_NAME = 'mobilityd'
DEFAULT_TIMEOUT_SECS = 5


class MobilitydUnavailable(Exception):
    """mobilityd could not answer, so the IP could not be resolved either way."""


def _default_stub() -> MobilityServiceStub:
    chan = ServiceRegistry.get_rpc_channel(
        MOBILITYD_SERVICE_NAME, ServiceRegistry.LOCAL,
    )
    return MobilityServiceStub(chan)


def to_ip_pb(source_ip: str) -> IPAddress:
    """
    Convert a textual source IP into mobilityd's IPAddress message.

    Raises:
        ValueError: source_ip is not an IP address.
    """
    ip = ipaddress.ip_address(source_ip)
    # A dual-stack listener reports IPv4 peers as ::ffff:a.b.c.d, while
    # mobilityd allocates and indexes the plain IPv4 address.
    if ip.version == 6 and ip.ipv4_mapped is not None:
        ip = ip.ipv4_mapped
    version = IPAddress.IPV4 if ip.version == 4 else IPAddress.IPV6
    return IPAddress(version=version, address=ip.packed)


class MobilitydClient:
    """Resolves a UE source IP to its IMSI via GetSubscriberIDFromIP."""

    def __init__(
        self,
        stub_factory: Callable[[], MobilityServiceStub] = _default_stub,
        timeout: float = DEFAULT_TIMEOUT_SECS,
    ):
        self._stub_factory = stub_factory
        self._timeout = timeout

    def get_imsi_for_ip(self, source_ip: str) -> Optional[str]:
        """
        Return the IMSI (as 'IMSI<digits>') that owns source_ip, or None
        when mobilityd has no allocation for it.

        Raises:
            ValueError: source_ip is not an IP address.
            MobilitydUnavailable: mobilityd is unreachable or failed.
        """
        ip_pb = to_ip_pb(source_ip)
        try:
            stub = self._stub_factory()
        except ValueError as err:
            raise MobilitydUnavailable(
                'no RPC channel to %s: %s' % (MOBILITYD_SERVICE_NAME, err),
            ) from err
        try:
            sid = stub.GetSubscriberIDFromIP(ip_pb, timeout=self._timeout)
        except grpc.RpcError as err:
            if err.code() == grpc.StatusCode.NOT_FOUND:
                return None
            raise MobilitydUnavailable(
                'GetSubscriberIDFromIP failed [%s] %s'
                % (err.code(), err.details()),
            ) from err
        if sid.type != SubscriberID.IMSI or not sid.id:
            return None
        return 'IMSI' + sid.id
