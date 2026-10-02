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
import logging
import threading
from typing import List

from lte.protos.mconfig import mconfigs_pb2
from magma.acsd.config import CwmpBind, get_cwmp_bind, get_cwmp_workers
from magma.acsd.identity import SessionIdentifier
from magma.acsd.server import make_cwmp_server
from magma.acsd.session import CwmpSessionHandler
from magma.common.sentry import sentry_init
from magma.common.service import MagmaService
from magma.configuration import load_service_config
from orc8r.protos.service303_pb2 import State


def _get_operational_states() -> List[State]:
    # `cpe_acs` state is reported once acsd keeps per-CPE state (Stage 2).
    return []


def start_cwmp_listener(
    bind: CwmpBind,
    handler: CwmpSessionHandler,
    workers: int,
) -> threading.Thread:
    """
    Serve CWMP on `bind` from a daemon thread. If the listener dies, the
    main thread is interrupted so systemd restarts acsd instead of leaving
    a healthy-looking service that no CPE can reach.
    """
    server = make_cwmp_server(bind, handler, workers)

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
    handler = CwmpSessionHandler(identify=SessionIdentifier())
    start_cwmp_listener(bind, handler, workers)
    logging.info(
        'acsd started in mode %s; CWMP on %s %s:%d (%d workers)',
        mconfigs_pb2.AcsD.Mode.Name(service.mconfig.mode),
        bind.interface, bind.address, bind.port, workers,
    )

    # Register a callback function for GetOperationalStates
    service.register_operational_states_callback(_get_operational_states)

    # Run the service loop
    service.run()

    # Cleanup the service
    service.close()


if __name__ == "__main__":
    main()
