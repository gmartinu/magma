"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

How a queued task reaches a CPE: a Connection Request makes the CPE open a
session now; otherwise the task waits for its next periodic Inform.

A claimed CPE is usually behind a carrier NAT, and the ConnectionRequestURL
it reports holds its address inside the carrier network, which acsd cannot
reach. So in `auto` mode a Connection Request goes out only when the URL
host is plausibly reachable: it is the address the CPE's sessions come from
(no NAT in between), or a public address or name. A private, CGNAT or other
non-global address that is not the session's source is behind a NAT, and
those are also the addresses a CPE could otherwise point acsd's requests at
inside the AGW's own networks. TR-111 (STUN) is out of scope.

A failed Connection Request is not retried until the CPE Informs again,
since a new Inform may bring a new URL or NAT mapping.
"""

import ipaddress
import time
from dataclasses import asdict, dataclass
from typing import Any, Callable, Dict, NamedTuple, Optional, Union
from urllib.parse import urlsplit

from magma.acsd.config import (
    CONNECTION_REQUEST_ALWAYS,
    CONNECTION_REQUEST_OFF,
    ReachConfig,
)
from magma.common.redis.containers import RedisHashDict
from magma.common.redis.serializers import (
    get_json_deserializer,
    get_json_serializer,
)

CONNECTION_REQUEST = 'connection_request'
NEXT_INFORM = 'next_inform'

# Why a CPE's tasks wait for its next Inform.
FROZEN = 'frozen'
DISABLED = 'disabled'
NO_CREDENTIAL = 'no_credential'
NO_URL = 'no_url'
BEHIND_NAT = 'behind_nat'
FAILED = 'connection_request_failed'

IpAddress = Union[ipaddress.IPv4Address, ipaddress.IPv6Address]


class Reach(NamedTuple):
    # CONNECTION_REQUEST or NEXT_INFORM
    how: str
    # Why NEXT_INFORM; '' for CONNECTION_REQUEST.
    reason: str = ''
    url: str = ''
    source_ip: str = ''


@dataclass
class Attempt:
    """The last Connection Request acsd sent a CPE."""
    at: float
    ok: bool
    detail: str = ''


def parse_ip(value: str) -> Optional[IpAddress]:
    try:
        address = ipaddress.ip_address(value)
    except ValueError:
        return None
    if address.version == 6 and address.ipv4_mapped:
        return address.ipv4_mapped
    return address


def url_host(url: str) -> str:
    try:
        return urlsplit(url).hostname or ''
    except ValueError:
        return ''


def host_reachable(host_ip: Optional[IpAddress], source_ip: str) -> bool:
    """
    The `auto` rule for an address: the session's source, or a public one.
    A name (host_ip None) passes here and is checked again once resolved.
    """
    if host_ip is None:
        return True
    if host_ip == parse_ip(source_ip):
        return True
    return host_ip.is_global


def assess(
    config: ReachConfig,
    url: str,
    source_ip: str,
    has_credential: bool,
    failed_since_inform: bool = False,
    frozen: bool = False,
) -> Reach:
    """How tasks reach a CPE with this ConnectionRequestURL and source."""
    def wait(reason):
        return Reach(NEXT_INFORM, reason, url, source_ip)
    if frozen:
        return wait(FROZEN)
    if config.connection_request == CONNECTION_REQUEST_OFF:
        return wait(DISABLED)
    if not has_credential:
        return wait(NO_CREDENTIAL)
    host = url_host(url)
    if not host:
        return wait(NO_URL)
    if failed_since_inform:
        return wait(FAILED)
    if config.connection_request != CONNECTION_REQUEST_ALWAYS and not host_reachable(
        parse_ip(host), source_ip,
    ):
        return wait(BEHIND_NAT)
    return Reach(CONNECTION_REQUEST, '', url, source_ip)


class Reacher:
    """
    Assesses the reach of each CPE from the store, the Connection Request
    credentials of claimed CPEs and the last attempt. Core CPEs get no
    Connection Request credential, so their tasks wait for the next Inform.
    """

    def __init__(
        self,
        config: ReachConfig,
        store,
        credentials=None,
        client=None,
        frozen: bool = False,
        prefix: str = 'acsd',
        clock: Callable[[], float] = time.time,
    ):
        self.config = config
        self._store = store
        self._credentials = credentials
        self._frozen = frozen
        self._clock = clock
        self._attempts = RedisHashDict(
            client, '%s:reach' % prefix,
            get_json_serializer(), get_json_deserializer(),
        ) if client is not None else {}

    def assess(
        self,
        cpe_key: str,
        model: Dict[str, Any],
        source_ip: str,
        last_inform: float,
    ) -> Reach:
        url = str(model.get('management_server', {}).get('connection_request_url') or '')
        last = self.last_attempt(cpe_key)
        return assess(
            self.config, url, source_ip,
            self.credential(cpe_key) is not None,
            failed_since_inform=bool(last and not last.ok and last.at >= last_inform),
            frozen=self._frozen,
        )

    def assess_cpe(self, cpe_key: str) -> Reach:
        """assess() from what the store holds for the CPE."""
        stored = self._store.get_model(cpe_key)
        informs = self._store.get_inform_count(cpe_key)
        return self.assess(
            cpe_key,
            stored.model if stored else {},
            self.source_ip(cpe_key),
            informs.last_inform if informs else 0.0,
        )

    def source_ip(self, cpe_key: str) -> str:
        """Where the CPE's open session, else its last one, came from."""
        for session in self._store.list_sessions():
            if session.cpe_key == cpe_key:
                return session.source_ip
        last = self._store.get_last_session(cpe_key)
        return last.source_ip if last else ''

    def credential(self, cpe_key: str):
        if self._credentials is None:
            return None
        return self._credentials.get(cpe_key).connection_request

    def last_attempt(self, cpe_key: str) -> Optional[Attempt]:
        raw = self._attempts.get(cpe_key)
        return Attempt(**raw) if raw else None

    def record_attempt(self, cpe_key: str, ok: bool, detail: str = '') -> Attempt:
        attempt = Attempt(self._clock(), ok, detail)
        self._attempts[cpe_key] = asdict(attempt)
        return attempt
