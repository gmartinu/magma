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
"""

import logging
import socket
import threading
import time
import warnings
from concurrent.futures import ThreadPoolExecutor
from typing import Callable, NamedTuple, Optional, Tuple

import requests
from magma.acsd.config import CONNECTION_REQUEST_AUTO
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
from requests.auth import HTTPDigestAuth
from urllib3.exceptions import InsecureRequestWarning

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


def resolve(host: str) -> Optional[str]:
    try:
        return socket.getaddrinfo(host, None)[0][4][0]
    except (OSError, IndexError):
        return None


class ConnectionRequester:
    def __init__(
        self,
        reacher: Reacher,
        store,
        timeout_secs: float,
        http_get: Callable[..., requests.Response] = requests.get,
        resolver: Callable[[str], Optional[str]] = resolve,
        executor=None,
        clock: Callable[[], float] = time.time,
    ):
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
        refused = self._refused_name(reach)
        if refused:
            reason, detail = refused
            self._reacher.record_attempt(cpe_key, False, detail)
            return Outcome(False, reach._replace(how=NEXT_INFORM, reason=reason), detail)
        try:
            resp = self._get(
                reach.url, auth=HTTPDigestAuth(*credential),
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

    def _refused_name(self, reach: Reach) -> Optional[Tuple[str, str]]:
        """
        A URL host given by name passes reach.assess(); in auto mode, refuse
        it here, as (reason, detail), when it resolves where an IP literal
        would be refused.
        """
        host = url_host(reach.url)
        auto = self._reacher.config.connection_request == CONNECTION_REQUEST_AUTO
        if not auto or parse_ip(host) is not None:
            return None
        address = self._resolve(host)
        if address is None:
            return FAILED, 'cannot resolve %s' % host
        if not host_reachable(parse_ip(address), reach.source_ip):
            return BEHIND_NAT, '%s resolves to %s' % (host, address)
        return None
