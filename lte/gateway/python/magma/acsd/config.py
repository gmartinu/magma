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

from dataclasses import dataclass, field
from typing import Any, Dict, NamedTuple

from lte.protos.mconfig import mconfigs_pb2

DEFAULT_CWMP_INTERFACE = 'mtr0'
DEFAULT_CWMP_ADDRESS = '10.1.0.1'
DEFAULT_CWMP_PORT = 48081
DEFAULT_CWMP_WORKERS = 16

AUTH_OFF = 'off'
AUTH_REQUIRED = 'required'
DEFAULT_AUTH_REALM = 'magma-acs'
DEFAULT_NONCE_TTL_SECS = 300


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


def get_cwmp_workers(service_config: Dict[str, Any]) -> int:
    """Size of the CWMP worker pool: CPE connections served at once."""
    return max(1, int(service_config.get('cwmp_workers', DEFAULT_CWMP_WORKERS)))


@dataclass(frozen=True)
class CwmpAuthConfig:
    """Digest authentication of CPEs on the CWMP listener."""
    mode: str = AUTH_REQUIRED
    realm: str = DEFAULT_AUTH_REALM
    username: str = ''
    # Kept out of repr so the config can be logged.
    password: str = field(default='', repr=False)
    nonce_ttl_secs: int = DEFAULT_NONCE_TTL_SECS

    @property
    def has_credential(self) -> bool:
        return bool(self.username and self.password)


def get_cwmp_auth(service_config: Dict[str, Any]) -> CwmpAuthConfig:
    """
    Resolve the `cwmp_auth` section of acsd.yml. Authentication is required
    unless the section says `mode: off`; an unknown mode is an error, so a
    typo cannot silently turn it off.
    """
    section = service_config.get('cwmp_auth') or {}
    mode = section.get('mode', AUTH_REQUIRED)
    if mode is False:
        # YAML 1.1 reads an unquoted `off` as false.
        mode = AUTH_OFF
    mode = str(mode).lower()
    if mode not in (AUTH_OFF, AUTH_REQUIRED):
        raise ValueError('unknown cwmp_auth mode %r' % mode)
    return CwmpAuthConfig(
        mode=mode,
        realm=str(section.get('realm') or DEFAULT_AUTH_REALM),
        username=str(section.get('username') or ''),
        password=str(section.get('password') or ''),
        nonce_ttl_secs=max(
            1, int(section.get('nonce_ttl_secs', DEFAULT_NONCE_TTL_SECS)),
        ),
    )
