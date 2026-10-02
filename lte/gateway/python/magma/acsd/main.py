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

import _thread
import asyncio
import logging
import threading
from typing import Optional

from lte.protos.mconfig import mconfigs_pb2
from magma.acsd.config import (
    AUTH_OFF,
    CwmpAuthConfig,
    CwmpBind,
    get_cwmp_auth,
    get_cwmp_bind,
    get_cwmp_workers,
)
from magma.acsd.cpe_state import CpeViews
from magma.acsd.digest import (
    DigestAuthenticator,
    NonceStore,
    StaticCredentialProvider,
)
from magma.acsd.identity import SessionIdentifier
from magma.acsd.rpc_servicer import CpeManagerRpcServicer
from magma.acsd.server import make_cwmp_server
from magma.acsd.session import REAP_INTERVAL_SEC, CwmpSessionHandler
from magma.acsd.store import AcsStore
from magma.common.redis.client import get_default_client
from magma.common.sentry import sentry_init
from magma.common.service import MagmaService
from magma.configuration import load_service_config


def schedule_reaper(
    loop: asyncio.AbstractEventLoop,
    store: AcsStore,
    interval_sec: float = REAP_INTERVAL_SEC,
) -> None:
    """
    Reap on a timer too: the handler only reaps when an Inform arrives, so
    on a quiet gateway dead sessions would linger in the state and pending
    tasks would never expire.
    """
    def reap():
        try:
            store.reap_expired()
        except Exception:  # pylint: disable=broad-except
            logging.exception('Reaping acsd sessions and tasks failed')
        finally:
            loop.call_later(interval_sec, reap)
    loop.call_soon(reap)


def make_authenticator(auth: CwmpAuthConfig) -> Optional[DigestAuthenticator]:
    """The Digest authenticator for `auth`, or None when it is off."""
    if auth.mode == AUTH_OFF:
        logging.warning('CWMP Digest authentication is off')
        return None
    if not auth.has_credential:
        logging.error(
            'CWMP Digest authentication is required but cwmp_auth has no '
            'username/password; every CPE will be refused with 401',
        )
    else:
        logging.info(
            'CWMP Digest authentication required (realm %s, user %s)',
            auth.realm, auth.username,
        )
    return DigestAuthenticator(
        auth.realm,
        StaticCredentialProvider(auth.username, auth.password),
        NonceStore(auth.nonce_ttl_secs),
    )


def start_cwmp_listener(
    bind: CwmpBind,
    handler: CwmpSessionHandler,
    workers: int,
    authenticator: Optional[DigestAuthenticator] = None,
) -> threading.Thread:
    """
    Serve CWMP on `bind` from a daemon thread. If the listener dies, the
    main thread is interrupted so systemd restarts acsd instead of leaving
    a healthy-looking service that no CPE can reach.
    """
    server = make_cwmp_server(bind, handler, workers, authenticator)

    def serve():
        try:
            server.serve_forever()
        finally:
            logging.error('CWMP listener stopped; interrupting acsd')
            _thread.interrupt_main()

    thread = threading.Thread(target=serve, name='cwmp-listener', daemon=True)
    thread.server = server
    thread.start()
    return thread


def main():
    """Start acsd"""
    service = MagmaService('acsd', mconfigs_pb2.AcsD())

    # Optionally pipe errors to Sentry
    sentry_init(service_name=service.name, sentry_mconfig=service.shared_mconfig.sentry_config)

    config = load_service_config('acsd')
    bind = get_cwmp_bind(config, service.mconfig)
    workers = get_cwmp_workers(config)
    store = AcsStore(get_default_client())
    # A new process cannot continue the HTTP exchanges of the last one.
    requeued = store.end_all_sessions('acsd restarted')
    if requeued:
        logging.info('Requeued %d tasks left in progress', requeued)
    handler = CwmpSessionHandler(identify=SessionIdentifier(), store=store)
    authenticator = make_authenticator(get_cwmp_auth(config))
    start_cwmp_listener(bind, handler, workers, authenticator)
    logging.info(
        'acsd started in mode %s; CWMP on %s %s:%d (%d workers)',
        mconfigs_pb2.AcsD.Mode.Name(service.mconfig.mode),
        bind.interface, bind.address, bind.port, workers,
    )

    views = CpeViews(handler.store, service.mconfig.periodic_inform_interval)
    service.register_operational_states_callback(views.operational_states)
    CpeManagerRpcServicer(handler.store, views).add_to_server(service.rpc_server)
    schedule_reaper(service.loop, store)

    # Run the service loop
    service.run()

    # Cleanup the service
    service.close()


if __name__ == "__main__":
    main()
