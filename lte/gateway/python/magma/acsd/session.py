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

import logging
import threading
from typing import Callable, Dict, Optional

from magma.tr069 import models
from spyne.model.complex import ComplexModelBase
from spyne.server.wsgi import WsgiMethodContext

# Names the CPE behind `source_ip`: returns the key the session is held
# under (the IMSI, in acsd), or None to refuse the session
# (HTTP 403). Called once per session, on its Inform.
IdentifyFn = Callable[[str, models.Inform], Optional[str]]

HTTP_403 = '403 Forbidden'


def accept_all(source_ip: str, inform: models.Inform) -> Optional[str]:
    """Default identity check: every CPE is accepted, keyed by its IP."""
    return source_ip


def source_ip_of(ctx: WsgiMethodContext) -> str:
    return ctx.transport.req_env.get('REMOTE_ADDR', '')


class CwmpSessionHandler:
    """
    Minimal CWMP session flow for acsd: answer the Inform, then end the
    session on the CPE's next message (normally the empty POST), since acsd
    has nothing to ask the CPE yet.

    Holds the identity of each open session by source IP. That is sound
    because the identity is derived from the source IP, so a later message
    from the same IP belongs to the same CPE. Thread-safe, so the
    listener's worker threads can share one instance.
    """

    def __init__(self, identify: IdentifyFn = accept_all):
        self._identify = identify
        self._lock = threading.Lock()
        self._sessions: Dict[str, str] = {}

    def session_identity(self, source_ip: str) -> Optional[str]:
        """The identity of the session open from source_ip, if any."""
        with self._lock:
            return self._sessions.get(source_ip)

    def handle_tr069_message(
        self,
        ctx: WsgiMethodContext,
        tr069_message: ComplexModelBase,
    ) -> ComplexModelBase:
        source_ip = source_ip_of(ctx)
        if isinstance(tr069_message, models.Inform):
            return self._handle_inform(ctx, source_ip, tr069_message)
        if not isinstance(tr069_message, models.DummyInput):
            logging.info(
                'CPE %s sent %s outside a task; ending the session',
                source_ip, type(tr069_message).__name__,
            )
        self._end_session(source_ip)
        return models.DummyInput()

    def _handle_inform(
        self,
        ctx: WsgiMethodContext,
        source_ip: str,
        inform: models.Inform,
    ) -> ComplexModelBase:
        serial = _serial_of(inform)
        identity = self._identify(source_ip, inform)
        if not identity:
            self._end_session(source_ip)
            logging.warning(
                'Refusing CWMP session from %s (serial %s)', source_ip, serial,
            )
            ctx.transport.resp_code = HTTP_403
            return models.DummyInput()
        with self._lock:
            self._sessions[source_ip] = identity
        logging.info(
            'Inform from %s (serial %s, identity %s)',
            source_ip, serial, identity,
        )
        return models.InformResponse(MaxEnvelopes=1)

    def _end_session(self, source_ip: str) -> None:
        with self._lock:
            self._sessions.pop(source_ip, None)


def _serial_of(inform: models.Inform) -> str:
    device_id = getattr(inform, 'DeviceId', None)
    return getattr(device_id, 'SerialNumber', None) or '?'
