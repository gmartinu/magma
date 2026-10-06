"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

TR-069 Connection Requests (3.2.2): an HTTP GET to the CPE's
ConnectionRequestURL, Digest-authenticated with the per-CPE credential
acsd set, asking the CPE to open a session now. Only sent when reach.py
says the URL is reachable; otherwise tasks wait for the next Inform.

The URL comes from the CPE, so acsd resolves its host once, checks every
address it resolves to, and connects to the checked address (Host header
and TLS SNI still name the host): resolving again at connect time would
let a DNS answer that changes in between (rebinding) point the request
anywhere. Whatever the mode, a request never goes to a loopback,
link-local, multicast or unspecified address, nor to an address of the
gateway itself; with `always`, nor anywhere on the gateway's own
networks but the CPE's session source.
"""

import ipaddress
import logging
import socket
import threading
import time
import warnings
from concurrent.futures import ThreadPoolExecutor
from typing import Callable, List, NamedTuple, Optional, Sequence, Tuple, Union

import requests
from magma.acsd.config import CONNECTION_REQUEST_ALWAYS, CONNECTION_REQUEST_AUTO
from magma.acsd.reach import (
    BEHIND_NAT,
    CONNECTION_REQUEST,
    FAILED,
    NEXT_INFORM,
    Reach,
    Reacher,
    host_reachable,
    parse_ip,
    url_host,
)
from requests.adapters import HTTPAdapter
from requests.auth import HTTPDigestAuth
from urllib3.connection import HTTPConnection, HTTPSConnection
from urllib3.connectionpool import HTTPConnectionPool, HTTPSConnectionPool
from urllib3.exceptions import (
    ConnectTimeoutError,
    InsecureRequestWarning,
    NewConnectionError,
)
from urllib3.util import connection as urllib3_connection

# A CPE accepts with 200 or 204 (TR-069 3.2.2).
ACCEPTED = (200, 204)
# A Connection Request queued by EnqueueTask is skipped when one went out
# this recently and the CPE has not Informed since: it is on its way.
AUTO_COOLDOWN_SEC = 30.0
IN_SESSION = 'in_session'

# CPEs serve an HTTPS ConnectionRequestURL with self-signed certificates.
# Digest never sends the password, so an unverified peer learns nothing it
# can replay beyond waking the CPE.
warnings.filterwarnings('ignore', category=InsecureRequestWarning)


class Outcome(NamedTuple):
    # The CPE accepted it and should open a session now.
    sent: bool
    reach: Reach
    # What went wrong: the HTTP status or the error.
    detail: str = ''


def resolve(host: str) -> List[str]:
    """Every address `host` resolves to, in resolver order; [] if none."""
    try:
        infos = socket.getaddrinfo(host, None, proto=socket.IPPROTO_TCP)
    except OSError:
        return []
    return list(dict.fromkeys(info[4][0] for info in infos))


Network = Union[ipaddress.IPv4Interface, ipaddress.IPv6Interface]


def interface_networks() -> List[Network]:
    """The address and network of every interface of this host."""
    import netifaces
    found = []
    for name in netifaces.interfaces():
        addresses = netifaces.ifaddresses(name)
        for family in (netifaces.AF_INET, netifaces.AF_INET6):
            for entry in addresses.get(family, []):
                address = (entry.get('addr') or '').split('%')[0]
                mask = entry.get('netmask') or ''
                try:
                    found.append(ipaddress.ip_interface('%s/%s' % (address, _prefix(mask))))
                except ValueError:
                    continue
    return found


def _prefix(mask: str) -> str:
    # netifaces gives IPv6 masks as 'ffff:ffff::' or 'ffff:ffff::/32'.
    if '/' in mask:
        return mask.rsplit('/', 1)[1]
    if ':' in mask:
        return str(bin(int(ipaddress.IPv6Address(mask))).count('1'))
    return mask


class AddressPolicy:
    """Addresses acsd never sends a Connection Request to."""

    _NEVER = (
        ('is_loopback', 'loopback'),
        ('is_link_local', 'link-local'),
        ('is_multicast', 'multicast'),
        ('is_unspecified', 'unspecified'),
    )

    def __init__(
        self,
        networks: Callable[[], Sequence[Network]] = interface_networks,
        refresh_sec: float = 60.0,
        clock: Callable[[], float] = time.monotonic,
    ):
        self._networks, self._refresh, self._clock = networks, refresh_sec, clock
        self._lock = threading.Lock()
        self._cached: Sequence[Network] = ()
        self._expires = 0.0

    def refusal(self, address, source_ip: str, always: bool) -> Optional[str]:
        """Why a request to `address` is refused, or None."""
        for attr, what in self._NEVER:
            if getattr(address, attr):
                return '%s is %s' % (address, what)
        local = self._local()
        if any(address == net.ip for net in local):
            return '%s is an address of this gateway' % address
        if always and address != parse_ip(source_ip):
            for net in local:
                if address in net.network:
                    return '%s is on network %s of this gateway' % (address, net.network)
        return None

    def _local(self) -> Sequence[Network]:
        with self._lock:
            if self._clock() >= self._expires:
                try:
                    self._cached = list(self._networks())
                except Exception:  # pylint: disable=broad-except
                    logging.exception('Cannot list the gateway interfaces')
                self._expires = self._clock() + self._refresh
            return self._cached


def _pinned(conn_cls, address: str):
    class Pinned(conn_cls):
        """Connects to `address` whatever the URL host resolves to now."""

        def _new_conn(self):
            extra = {}
            if self.source_address:
                extra['source_address'] = self.source_address
            if self.socket_options:
                extra['socket_options'] = self.socket_options
            try:
                return urllib3_connection.create_connection(
                    (address, self.port), self.timeout, **extra,
                )
            except socket.timeout:
                raise ConnectTimeoutError(
                    self, 'Connection to %s (%s) timed out' % (self.host, address),
                )
            except OSError as err:
                raise NewConnectionError(
                    self, 'Failed to connect to %s (%s): %s' % (self.host, address, err),
                )
    return Pinned


class _PinnedAdapter(HTTPAdapter):
    def __init__(self, address: str):
        self._address = address
        super().__init__(max_retries=0)

    def init_poolmanager(self, *args, **kwargs):
        super().init_poolmanager(*args, **kwargs)
        self.poolmanager.pool_classes_by_scheme = {
            'http': type('PinnedPool', (HTTPConnectionPool,), {
                'ConnectionCls': _pinned(HTTPConnection, self._address),
            }),
            'https': type('PinnedTlsPool', (HTTPSConnectionPool,), {
                'ConnectionCls': _pinned(HTTPSConnection, self._address),
            }),
        }


def pinned_get(url: str, address: str, **kwargs) -> requests.Response:
    """requests.get(url) connecting to `address` instead of resolving."""
    with requests.Session() as session:
        session.trust_env = False
        adapter = _PinnedAdapter(address)
        session.mount('http://', adapter)
        session.mount('https://', adapter)
        return session.get(url, **kwargs)


class ConnectionRequester:
    def __init__(
        self,
        reacher: Reacher,
        store,
        timeout_secs: float,
        http_get: Callable[..., requests.Response] = pinned_get,
        resolver: Callable[[str], List[str]] = resolve,
        executor=None,
        clock: Callable[[], float] = time.time,
        policy: Optional[AddressPolicy] = None,
    ):
        """
        `http_get(url, address=..., **requests_kwargs)` sends the request
        to `address`, the checked address of the URL host.
        """
        self._policy = policy or AddressPolicy()
        self._reacher = reacher
        self._store = store
        self._timeout = timeout_secs
        self._get = http_get
        self._resolve = resolver
        self._executor = executor or ThreadPoolExecutor(
            max_workers=2, thread_name_prefix='acsd-connreq',
        )
        self._clock = clock
        self._lock = threading.Lock()
        self._in_flight = set()

    def request(self, cpe_key: str) -> Outcome:
        """Send a Connection Request now if the CPE can be reached."""
        if any(s.cpe_key == cpe_key for s in self._store.list_sessions()):
            # Its queued tasks run in the session it has open.
            return Outcome(False, Reach(CONNECTION_REQUEST, IN_SESSION))
        reach = self._reacher.assess_cpe(cpe_key)
        if reach.how != CONNECTION_REQUEST:
            return Outcome(False, reach)
        credential = self._reacher.credential(cpe_key)
        address, refused = self._address(reach)
        if refused:
            reason, detail = refused
            logging.warning('Connection Request to %s refused: %s', cpe_key, detail)
            self._reacher.record_attempt(cpe_key, False, detail)
            return Outcome(False, reach._replace(how=NEXT_INFORM, reason=reason), detail)
        try:
            resp = self._get(
                reach.url, address=address, auth=HTTPDigestAuth(*credential),
                timeout=self._timeout, allow_redirects=False, verify=False,
            )
            ok, detail = resp.status_code in ACCEPTED, 'HTTP %d' % resp.status_code
        except requests.RequestException as err:
            ok, detail = False, '%s: %s' % (type(err).__name__, err)
        self._reacher.record_attempt(cpe_key, ok, '' if ok else detail)
        if ok:
            logging.info('Connection Request to %s accepted', cpe_key)
            return Outcome(True, reach)
        logging.warning(
            'Connection Request to %s (%s) failed: %s; its tasks wait for its '
            'next Inform%s', cpe_key, reach.url, detail,
            '; acsd_cli.py rotate-credentials resets its credential'
            if detail == 'HTTP 401' else '',
        )
        return Outcome(False, reach._replace(how=NEXT_INFORM, reason=FAILED), detail)

    def request_soon(self, cpe_key: str) -> None:
        """
        request() from a worker thread, so EnqueueTask does not wait on the
        CPE. One at a time per CPE, and not again while the last one's
        session is likely on its way.
        """
        if self._requested_recently(cpe_key):
            return
        with self._lock:
            if cpe_key in self._in_flight:
                return
            self._in_flight.add(cpe_key)
        self._executor.submit(self._run, cpe_key)

    def _run(self, cpe_key: str) -> None:
        try:
            self.request(cpe_key)
        except Exception:  # pylint: disable=broad-except
            logging.exception('Connection Request to %s failed', cpe_key)
        finally:
            with self._lock:
                self._in_flight.discard(cpe_key)

    def _requested_recently(self, cpe_key: str) -> bool:
        last = self._reacher.last_attempt(cpe_key)
        if last is None or not last.ok:
            return False
        informs = self._store.get_inform_count(cpe_key)
        if informs is not None and informs.last_inform >= last.at:
            return False
        return self._clock() - last.at < AUTO_COOLDOWN_SEC

    def _address(self, reach: Reach) -> Tuple[str, Optional[Tuple[str, str]]]:
        """
        The address to send the request to, resolved once, or ('', (reason,
        detail)) when any address the URL host stands for is refused.
        """
        host = url_host(reach.url)
        literal = parse_ip(host)
        if literal is not None:
            addresses = [literal]
        else:
            resolved = self._resolve(host)
            if isinstance(resolved, str):
                resolved = [resolved]
            addresses = [a for a in (parse_ip(r) for r in resolved or []) if a is not None]
            if not addresses:
                return '', (FAILED, 'cannot resolve %s' % host)
        mode = self._reacher.config.connection_request
        named = '' if literal is not None else '%s resolves to ' % host
        for address in addresses:
            why = self._policy.refusal(
                address, reach.source_ip, mode == CONNECTION_REQUEST_ALWAYS,
            )
            if why:
                return '', (FAILED, named + why)
            if mode == CONNECTION_REQUEST_AUTO and not host_reachable(address, reach.source_ip):
                return '', (BEHIND_NAT, named + str(address))
        return str(addresses[0]), None
