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
from typing import List

from lte.protos.mconfig import mconfigs_pb2
from magma.acsd.config import get_cwmp_bind
from magma.common.sentry import sentry_init
from magma.common.service import MagmaService
from magma.configuration import load_service_config
from orc8r.protos.service303_pb2 import State


def _get_operational_states() -> List[State]:
    # No CPE sessions yet; `cpe_acs` state is reported once the CWMP
    # listener lands.
    return []


def main():
    """Start acsd"""
    service = MagmaService('acsd', mconfigs_pb2.AcsD())

    # Optionally pipe errors to Sentry
    sentry_init(service_name=service.name, sentry_mconfig=service.shared_mconfig.sentry_config)

    bind = get_cwmp_bind(load_service_config('acsd'), service.mconfig)
    logging.info(
        'acsd started in mode %s; CWMP listener (%s %s:%d) not enabled yet',
        mconfigs_pb2.AcsD.Mode.Name(service.mconfig.mode),
        bind.interface, bind.address, bind.port,
    )

    # Register a callback function for GetOperationalStates
    service.register_operational_states_callback(_get_operational_states)

    # Run the service loop
    service.run()

    # Cleanup the service
    service.close()


if __name__ == "__main__":
    main()
