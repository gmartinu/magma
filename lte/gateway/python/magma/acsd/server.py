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

import io
import logging
import threading
from concurrent.futures import ThreadPoolExecutor
from typing import Callable, Iterable, List, Optional
from wsgiref.simple_server import WSGIServer

from magma.acsd.config import DEFAULT_CWMP_WORKERS, CwmpBind
from magma.acsd.digest import (
    CONNECTION_STATE,
    DigestAuthenticator,
    DigestAuthMiddleware,
)
from magma.tr069.models import CWMP_NS
from magma.tr069.rpc_methods import (
    RPC_RESPONSES,
    AutoConfigServer,
    Tr069MessageHandler,
)
from magma.tr069.server import tr069_WSGIRequestHandler
from magma.tr069.spyne_mods import Tr069Application, Tr069Soap11
from spyne.server.wsgi import WsgiApplication

WsgiApp = Callable[[dict, Callable], Iterable[bytes]]


class CwmpWsgiApp:
    """
    Wraps the spyne CWMP application so it can be served from many threads.

    spyne's TR-069 dispatch writes the reply name onto the shared method
    descriptor (`sub_name`), so two requests in flight at once could swap
    replies. The request body is read before taking the lock, so a slow CPE
    upload never blocks the others; only the in-memory SOAP work is serial.
    """

    def __init__(self, spyne_app: WsgiApp):
        self._spyne_app = spyne_app
        self._lock = threading.Lock()

    def __call__(self, environ: dict, start_response: Callable) -> List[bytes]:
        if environ.get('REQUEST_METHOD', '').upper() != 'POST':
            start_response(
                '405 Method Not Allowed',
                [('Allow', 'POST'), ('Content-Length', '0')],
            )
            return [b'']

        length = int(environ.get('CONTENT_LENGTH') or 0)
        body = environ['wsgi.input'].read(length) if length > 0 else b''
        environ['wsgi.input'] = io.BytesIO(body)
        environ['CONTENT_LENGTH'] = str(len(body))

        reply = {}

        def capture(status, headers, exc_info=None):
            reply['status'], reply['headers'] = status, headers

        with self._lock:
            chunks = b''.join(self._spyne_app(environ, capture))

        status, headers = reply['status'], reply['headers']
        if status.startswith('200') and not chunks:
            # An empty HTTP response ends the CWMP session; TR-069 says to
            # send it as 204 so CPEs do not try to parse a SOAP body.
            status = '204 No Content'
        headers = [
            (k, v) for k, v in headers if k.lower() != 'content-length'
        ] + [('Content-Length', str(len(chunks)))]
        start_response(status, headers)
        return [chunks]


def _echo_cwmp_id(ctx) -> None:
    """
    Answer a CPE-initiated RPC with the cwmp:ID the CPE sent, as TR-069
    requires. The shared dispatch overwrites it with 'null' (enodebd's eNBs
    tolerate that); fixing it here keeps enodebd's behaviour unchanged.
    """
    sub_name = ctx.descriptor.out_message.Attributes.sub_name
    if sub_name in RPC_RESPONSES and ctx.in_header is not None:
        ctx.out_header.Data = ctx.in_header.Data


def make_cwmp_app(
    handler: Tr069MessageHandler,
    authenticator: Optional[DigestAuthenticator] = None,
) -> WsgiApp:
    """
    Build the CWMP WSGI app with `handler` owning every CPE message, behind
    Digest authentication when `authenticator` is given.
    """
    AutoConfigServer.set_state_machine_manager(handler)
    app = Tr069Application(
        [AutoConfigServer], CWMP_NS,
        in_protocol=Tr069Soap11(validator='soft'),
        out_protocol=Tr069Soap11(),
    )
    # App-level listener: fires after the RPC body, before serialization.
    app.event_manager.add_listener('method_return_object', _echo_cwmp_id)
    cwmp = CwmpWsgiApp(WsgiApplication(app))
    if authenticator is None:
        return cwmp
    return DigestAuthMiddleware(cwmp, authenticator)


class PooledWSGIServer(WSGIServer):
    """
    WSGI server handing each CPE connection to a bounded worker pool, so one
    CPE holding a keep-alive session does not stall the others, and a burst
    of CPEs cannot spawn unbounded threads.
    """
    # socketserver's default backlog (5) resets connections when many CPEs
    # inform at once, e.g. after the gateway restarts.
    request_queue_size = 128

    def __init__(self, server_address, handler_class, workers: int):
        super().__init__(server_address, handler_class)
        self._pool = ThreadPoolExecutor(
            max_workers=workers, thread_name_prefix='cwmp',
        )

    def process_request(self, request, client_address):
        self._pool.submit(self._process, request, client_address)

    def _process(self, request, client_address):
        try:
            self.finish_request(request, client_address)
        except Exception:  # pylint: disable=broad-except
            self.handle_error(request, client_address)
        finally:
            self.shutdown_request(request)

    def handle_error(self, request, client_address):
        logging.exception('CWMP connection from %s failed', client_address)

    def server_close(self):
        super().server_close()
        self._pool.shutdown(wait=False)


class CwmpRequestHandler(tr069_WSGIRequestHandler):
    device_name = 'CPE'

    def handle(self):
        # One instance serves one TCP connection, keep-alive included.
        self.connection_state = {}  # pylint: disable=attribute-defined-outside-init
        super().handle()

    def get_environ(self):
        environ = super().get_environ()
        environ[CONNECTION_STATE] = self.connection_state
        # wsgiref leaves it out; claimed sessions are keyed by connection.
        environ['REMOTE_PORT'] = str(self.client_address[1])
        return environ


def make_cwmp_server(
    bind: CwmpBind,
    handler: Tr069MessageHandler,
    workers: int = DEFAULT_CWMP_WORKERS,
    authenticator: Optional[DigestAuthenticator] = None,
) -> PooledWSGIServer:
    """Create (but do not start) the CWMP listener on `bind`."""
    server = PooledWSGIServer(
        (bind.address, bind.port), CwmpRequestHandler, workers,
    )
    server.set_app(make_cwmp_app(handler, authenticator))
    return server
