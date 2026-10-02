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
from typing import Callable

from magma.tr069 import models
from spyne.model.complex import ComplexModelBase
from spyne.server.wsgi import WsgiMethodContext

# Decides whether the CPE behind `source_ip` may open a session. Called once
# per session, on its Inform. False refuses the session (HTTP 403).
IdentifyFn = Callable[[str, models.Inform], bool]

HTTP_403 = '403 Forbidden'


def accept_all(source_ip: str, inform: models.Inform) -> bool:
    """Default identity check: every CPE is accepted."""
    return True


def source_ip_of(ctx: WsgiMethodContext) -> str:
    return ctx.transport.req_env.get('REMOTE_ADDR', '')


class CwmpSessionHandler:
    """
    Minimal CWMP session flow for acsd: answer the Inform, then end the
    session on the CPE's next message (normally the empty POST), since acsd
    has nothing to ask the CPE yet.

    Stateless and thread-safe, so the listener's worker threads can share
    one instance.
    """

    def __init__(self, identify: IdentifyFn = accept_all):
        self._identify = identify

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
        return models.DummyInput()

    def _handle_inform(
        self,
        ctx: WsgiMethodContext,
        source_ip: str,
        inform: models.Inform,
    ) -> ComplexModelBase:
        serial = _serial_of(inform)
        if not self._identify(source_ip, inform):
            logging.warning(
                'Refusing CWMP session from %s (serial %s)', source_ip, serial,
            )
            ctx.transport.resp_code = HTTP_403
            return models.DummyInput()
        logging.info('Inform from %s (serial %s)', source_ip, serial)
        return models.InformResponse(MaxEnvelopes=1)


def _serial_of(inform: models.Inform) -> str:
    device_id = getattr(inform, 'DeviceId', None)
    return getattr(device_id, 'SerialNumber', None) or '?'
