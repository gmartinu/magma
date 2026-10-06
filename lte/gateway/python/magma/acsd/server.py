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
import ssl
import threading
from concurrent.futures import ThreadPoolExecutor
from typing import Callable, Iterable, List, Optional
from wsgiref.simple_server import WSGIServer

from magma.acsd.config import (
    DEFAULT_CWMP_MAX_BODY_BYTES,
    DEFAULT_CWMP_WORKERS,
    LISTENER_MODE,
    MODE_CORE,
    CwmpBind,
)
from magma.acsd.digest import (
    CLOSE_CONNECTION,
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

# A CPE that connects and never finishes the TLS handshake must not hold a
# worker forever.
TLS_HANDSHAKE_TIMEOUT_SEC = 30.0


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


def make_cwmp_wsgi(handler: Tr069MessageHandler) -> CwmpWsgiApp:
    """
    The CWMP WSGI app with `handler` owning every CPE message. The spyne
    service and its handler are process-wide (class attributes), so every
    listener must share this one app and its lock.
    """
    AutoConfigServer.set_state_machine_manager(handler)
    app = Tr069Application(
        [AutoConfigServer], CWMP_NS,
        in_protocol=Tr069Soap11(validator='soft'),
        out_protocol=Tr069Soap11(),
    )
    # App-level listener: fires after the RPC body, before serialization.
    app.event_manager.add_listener('method_return_object', _echo_cwmp_id)
    return CwmpWsgiApp(WsgiApplication(app))


def make_cwmp_app(
    handler: Tr069MessageHandler,
    authenticator: Optional[DigestAuthenticator] = None,
    mode: str = MODE_CORE,
    cwmp: Optional[CwmpWsgiApp] = None,
    max_body: int = DEFAULT_CWMP_MAX_BODY_BYTES,
) -> WsgiApp:
    """
    The app of one listener: the shared `cwmp` app (built for `handler`
    when not given) behind Digest when `authenticator` is given, with every
    request tagged with the listener's identity mode. Bodies over
    `max_body` bytes are refused before anything reads them.
    """
    app: WsgiApp = cwmp or make_cwmp_wsgi(handler)
    if authenticator is not None:
        app = DigestAuthMiddleware(app, authenticator)
    return _BodyLimit(_ModeTag(app, mode), max_body)


class _BodyLimit:
    """
    Refuse a request whose declared body is too large (413) or unreadable
    (400) and close the connection, so the body is never read into memory.
    """

    def __init__(self, app: WsgiApp, max_body: int):
        self._app, self._max = app, max_body

    def __call__(self, environ: dict, start_response: Callable):
        try:
            length = int(environ.get('CONTENT_LENGTH') or 0)
        except ValueError:
            length = -1
        if 0 <= length <= self._max:
            return self._app(environ, start_response)
        status = '413 Payload Too Large' if length > self._max else '400 Bad Request'
        logging.warning(
            'Refusing CWMP request from %s: %s (Content-Length %r, limit %d)',
            environ.get('REMOTE_ADDR', ''), status,
            environ.get('CONTENT_LENGTH'), self._max,
        )
        conn = environ.get(CONNECTION_STATE)
        if conn is not None:
            conn[CLOSE_CONNECTION] = True
        start_response(status, [('Content-Length', '0')])
        return [b'']


class _ModeTag:
    def __init__(self, app: WsgiApp, mode: str):
        self._app, self._mode = app, mode

    def __call__(self, environ: dict, start_response: Callable):
        environ[LISTENER_MODE] = self._mode
        return self._app(environ, start_response)


class PooledWSGIServer(WSGIServer):
    """
    WSGI server handing each CPE connection to a bounded worker pool, so one
    CPE holding a keep-alive session does not stall the others, and a burst
    of CPEs cannot spawn unbounded threads.
    """
    # socketserver's default backlog (5) resets connections when many CPEs
    # inform at once, e.g. after the gateway restarts.
    request_queue_size = 128

    def __init__(
        self,
        server_address,
        handler_class,
        workers: int,
        ssl_context: Optional[ssl.SSLContext] = None,
    ):
        super().__init__(server_address, handler_class)
        self._ssl_context = ssl_context
        self._pool = ThreadPoolExecutor(
            max_workers=workers, thread_name_prefix='cwmp',
        )

    def process_request(self, request, client_address):
        self._pool.submit(self._process, request, client_address)

    def _process(self, request, client_address):
        try:
            if self._ssl_context is not None:
                # In the worker, so a slow handshake never stalls accept().
                request = self._handshake(request, client_address)
                if request is None:
                    return
            self.finish_request(request, client_address)
        except Exception:  # pylint: disable=broad-except
            self.handle_error(request, client_address)
        finally:
            self.shutdown_request(request)

    def _handshake(self, request, client_address):
        request.settimeout(TLS_HANDSHAKE_TIMEOUT_SEC)
        try:
            tls = self._ssl_context.wrap_socket(request, server_side=True)
        except (ssl.SSLError, OSError) as err:
            logging.info('TLS handshake with %s failed: %s', client_address, err)
            self.shutdown_request(request)
            return None
        tls.settimeout(None)
        return tls

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

    def handle_single(self):
        super().handle_single()
        if self.connection_state.get(CLOSE_CONNECTION):
            # The body of the request was left unread, so nothing after it
            # on this connection can be parsed.
            self.close_connection = 1

    def get_environ(self):
        environ = super().get_environ()
        environ[CONNECTION_STATE] = self.connection_state
        # wsgiref leaves it out; claimed sessions are keyed by connection.
        environ['REMOTE_PORT'] = str(self.client_address[1])
        if isinstance(self.connection, ssl.SSLSocket):
            environ['wsgi.url_scheme'], environ['HTTPS'] = 'https', 'on'
        return environ


def make_cwmp_server(
    bind: CwmpBind,
    handler: Tr069MessageHandler,
    workers: int = DEFAULT_CWMP_WORKERS,
    authenticator: Optional[DigestAuthenticator] = None,
    mode: str = MODE_CORE,
    ssl_context: Optional[ssl.SSLContext] = None,
    cwmp: Optional[CwmpWsgiApp] = None,
    max_body: int = DEFAULT_CWMP_MAX_BODY_BYTES,
) -> PooledWSGIServer:
    """
    Create (but do not start) a CWMP listener on `bind`, serving HTTPS
    when `ssl_context` is given. Listeners of one process pass the same
    `cwmp` app (make_cwmp_wsgi).
    """
    server = PooledWSGIServer(
        (bind.address, bind.port), CwmpRequestHandler, workers, ssl_context,
    )
    server.set_app(make_cwmp_app(handler, authenticator, mode, cwmp, max_body))
    return server


def make_tls_context(cert_path: str, key_path: str) -> ssl.SSLContext:
    """Server-side TLS 1.2+ without client certificates (MVP)."""
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    context.load_cert_chain(cert_path, key_path)
    return context
