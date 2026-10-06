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
from typing import Callable, Optional

from lte.protos.mconfig import mconfigs_pb2
from magma.acsd.claimed import ClaimedMode
from magma.acsd.claims import ClaimRegistry
from magma.acsd.config import (
    AUTH_OFF,
    MODE_CLAIMED,
    MODE_CORE,
    CwmpAuthConfig,
    CwmpBind,
    CwmpWanConfig,
    ReachConfig,
    get_cwmp_auth,
    get_cwmp_bind,
    get_cwmp_wan,
    get_cwmp_workers,
    get_reach_config,
)
from magma.acsd.cpe_state import STATE_MAX_AGE_SEC, CpeViews
from magma.acsd.credentials import CredentialStore
from magma.acsd.digest import (
    DigestAuthenticator,
    NonceStore,
    StaticCredentialProvider,
)
from magma.acsd.events import AcsEvents, EventEmitter
from magma.acsd.identity import SessionIdentifier
from magma.acsd.metrics import AcsMetrics, get_cpe_kpi_config
from magma.acsd.rpc_servicer import CpeManagerRpcServicer
from magma.acsd.server import make_cwmp_server, make_cwmp_wsgi, make_tls_context
from magma.acsd.session import REAP_INTERVAL_SEC, CwmpSessionHandler
from magma.acsd.store import AcsStore, StoreListeners
from magma.common.redis.client import get_default_client
from magma.common.sentry import sentry_init
from magma.common.service import MagmaService
from magma.configuration import load_service_config


def schedule_reaper(
    loop: asyncio.AbstractEventLoop,
    store: AcsStore,
    interval_sec: float = REAP_INTERVAL_SEC,
    then: Optional[Callable[[], None]] = None,
) -> None:
    """
    Reap on a timer too: the handler only reaps when an Inform arrives, so
    on a quiet gateway dead sessions would linger in the state and pending
    tasks would never expire. `then` runs after each reap (the metric
    gauges refresh).
    """
    def reap():
        try:
            store.reap_expired()
            if then:
                then()
        except Exception:  # pylint: disable=broad-except
            logging.exception('acsd maintenance (reap, metrics) failed')
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


def make_claimed_mode(
    wan: CwmpWanConfig, store: AcsStore, client, reach: ReachConfig = ReachConfig(),
) -> Optional[ClaimedMode]:
    """Claimed mode for the WAN listener, or None when it is off."""
    if not wan.enabled:
        return None
    if not (wan.bootstrap_username and wan.bootstrap_password):
        logging.error(
            'cwmp_wan is on without a bootstrap credential; only claimed CPEs '
            'that already rotated to a per-CPE one can get in',
        )
    return ClaimedMode(
        ClaimRegistry(client), CredentialStore(client, wan.realm), store,
        wan.bootstrap_username, wan.bootstrap_password,
        reach.periodic_inform_interval,
    )


def start_wan_listener(
    wan: CwmpWanConfig,
    handler: CwmpSessionHandler,
    claimed: ClaimedMode,
    workers: int,
    cwmp,
) -> Optional[threading.Thread]:
    """
    Start the claimed listener, or log why it cannot start. A broken WAN
    setup must not take the core listener down with it.
    """
    try:
        context = make_tls_context(wan.cert_path, wan.key_path)
    except (OSError, ValueError) as err:
        logging.error(
            'cwmp_wan listener not started: cannot load cert %r / key %r: %s',
            wan.cert_path, wan.key_path, err,
        )
        return None
    authenticator = DigestAuthenticator(
        wan.realm, claimed, NonceStore(wan.nonce_ttl_secs),
    )
    thread = start_cwmp_listener(
        wan.bind, handler, workers, authenticator,
        mode=MODE_CLAIMED, ssl_context=context, cwmp=cwmp,
    )
    logging.info(
        'CWMP claimed listener on https://%s:%d (%s)',
        wan.bind.address, wan.bind.port, wan.bind.interface,
    )
    return thread


def start_cwmp_listener(
    bind: CwmpBind,
    handler: CwmpSessionHandler,
    workers: int,
    authenticator: Optional[DigestAuthenticator] = None,
    mode: str = MODE_CORE,
    ssl_context=None,
    cwmp=None,
) -> threading.Thread:
    """
    Serve CWMP on `bind` from a daemon thread. If the listener dies, the
    main thread is interrupted so systemd restarts acsd instead of leaving
    a healthy-looking service that no CPE can reach.
    """
    server = make_cwmp_server(
        bind, handler, workers, authenticator, mode, ssl_context, cwmp,
    )

    def serve():
        try:
            server.serve_forever()
        finally:
            logging.error('CWMP listener stopped; interrupting acsd')
            _thread.interrupt_main()

    thread = threading.Thread(
        target=serve, name='cwmp-listener-%s' % mode, daemon=True,
    )
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
    client = get_default_client()
    metrics = AcsMetrics(get_cpe_kpi_config(config))
    events = AcsEvents(EventEmitter().start())
    store = AcsStore(client, listener=StoreListeners(metrics, events))
    views = CpeViews(store, service.mconfig.periodic_inform_interval)
    events.mode_of = views.mode_of
    # A new process cannot continue the HTTP exchanges of the last one.
    requeued = store.end_all_sessions('acsd restarted')
    if requeued:
        logging.info('Requeued %d tasks left in progress', requeued)
    auth = get_cwmp_auth(config)
    wan = get_cwmp_wan(config, auth)
    reach = get_reach_config(config, service.mconfig)
    claimed = make_claimed_mode(wan, store, client, reach)
    frozen = service.mconfig.mode == mconfigs_pb2.AcsD.FROZEN
    handler = CwmpSessionHandler(
        identify=SessionIdentifier(), store=store, claimed=claimed,
        observer=metrics, frozen=frozen,
    )
    cwmp = make_cwmp_wsgi(handler)
    start_cwmp_listener(
        bind, handler, workers, make_authenticator(auth), cwmp=cwmp,
    )
    if claimed is not None:
        start_wan_listener(wan, handler, claimed, workers, cwmp)
    logging.info(
        'acsd started in mode %s; CWMP on %s %s:%d (%d workers)',
        mconfigs_pb2.AcsD.Mode.Name(service.mconfig.mode),
        bind.interface, bind.address, bind.port, workers,
    )

    service.register_operational_states_callback(views.operational_states)
    CpeManagerRpcServicer(handler.store, views, frozen).add_to_server(service.rpc_server)
    schedule_reaper(
        service.loop, store,
        then=lambda: metrics.refresh(views.list(STATE_MAX_AGE_SEC), store.task_counts()),
    )

    # Run the service loop
    service.run()

    # Cleanup the service
    service.close()


if __name__ == "__main__":
    main()
