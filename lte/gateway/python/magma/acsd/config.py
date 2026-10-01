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

from typing import Any, Dict, NamedTuple

from lte.protos.mconfig import mconfigs_pb2

DEFAULT_CWMP_INTERFACE = 'mtr0'
DEFAULT_CWMP_ADDRESS = '10.1.0.1'
DEFAULT_CWMP_PORT = 48081


class CwmpBind(NamedTuple):
    """Where the CWMP listener binds."""
    interface: str
    address: str
    port: int


def get_cwmp_bind(
    service_config: Dict[str, Any],
    mconfig: mconfigs_pb2.AcsD,
) -> CwmpBind:
    """
    Resolve the CWMP bind from acsd.yml and the AcsD mconfig.

    The mconfig port wins when set, so Orc8r can move the port without a
    config push to the gateway; 0 (proto default) falls back to acsd.yml.
    """
    port = mconfig.port or int(
        service_config.get('cwmp_port', DEFAULT_CWMP_PORT),
    )
    return CwmpBind(
        interface=service_config.get('cwmp_interface', DEFAULT_CWMP_INTERFACE),
        address=service_config.get('cwmp_address', DEFAULT_CWMP_ADDRESS),
        port=port,
    )
