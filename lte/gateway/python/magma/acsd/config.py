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
# The largest Inform or GetParameterValuesResponse acsd accepts; a full
# TR-181 dump of a CPE is a few hundred KiB.
DEFAULT_CWMP_MAX_BODY_BYTES = 1024 * 1024

# Identity modes of a CWMP listener: `core` names the CPE by the IMSI
# behind its source IP on mtr0; `claimed` by its claim and per-CPE Digest.
MODE_CORE = 'core'
MODE_CLAIMED = 'claimed'
# WSGI environ key with the mode of the listener a request came in on.
LISTENER_MODE = 'acsd.listener_mode'

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


def get_cwmp_max_body(service_config: Dict[str, Any]) -> int:
    """The largest CWMP request body accepted; larger ones get 413."""
    return max(
        1, int(service_config.get('cwmp_max_body_bytes') or DEFAULT_CWMP_MAX_BODY_BYTES),
    )


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


DEFAULT_WAN_INTERFACE = 'eth0'
DEFAULT_WAN_ADDRESS = '0.0.0.0'
DEFAULT_WAN_PORT = 48443


@dataclass(frozen=True)
class CwmpWanConfig:
    """
    The claimed-mode listener on the WAN side: HTTPS, Digest always, and
    the bootstrap credential claimed CPEs start with.
    """
    enabled: bool = False
    bind: CwmpBind = CwmpBind(DEFAULT_WAN_INTERFACE, DEFAULT_WAN_ADDRESS, DEFAULT_WAN_PORT)
    cert_path: str = ''
    key_path: str = ''
    realm: str = DEFAULT_AUTH_REALM
    bootstrap_username: str = ''
    bootstrap_password: str = field(default='', repr=False)
    nonce_ttl_secs: int = DEFAULT_NONCE_TTL_SECS


def get_cwmp_wan(
    service_config: Dict[str, Any], auth: CwmpAuthConfig,
) -> CwmpWanConfig:
    """
    Resolve the `cwmp_wan` section of acsd.yml. The realm, bootstrap
    credential and nonce TTL default to cwmp_auth's, so one shared
    credential bootstraps CPEs on either listener; the WAN listener requires
    Digest even when cwmp_auth is off.
    """
    section = service_config.get('cwmp_wan') or {}
    return CwmpWanConfig(
        enabled=section.get('enabled') is True,
        bind=CwmpBind(
            interface=str(section.get('interface') or DEFAULT_WAN_INTERFACE),
            address=str(section.get('address') or DEFAULT_WAN_ADDRESS),
            port=int(section.get('port') or DEFAULT_WAN_PORT),
        ),
        cert_path=str(section.get('cert_path') or ''),
        key_path=str(section.get('key_path') or ''),
        realm=str(section.get('realm') or auth.realm),
        bootstrap_username=str(section.get('bootstrap_username') or auth.username),
        bootstrap_password=str(section.get('bootstrap_password') or auth.password),
        nonce_ttl_secs=max(
            1, int(section.get('nonce_ttl_secs') or auth.nonce_ttl_secs),
        ),
    )


DEFAULT_CLAIMED_INFORM_INTERVAL_SEC = 300
# When acsd sends Connection Requests: `auto` only to URLs that look
# reachable (reach.py), `always` to any URL, `off` never.
CONNECTION_REQUEST_AUTO = 'auto'
CONNECTION_REQUEST_ALWAYS = 'always'
CONNECTION_REQUEST_OFF = 'off'
CONNECTION_REQUEST_MODES = (
    CONNECTION_REQUEST_AUTO, CONNECTION_REQUEST_ALWAYS, CONNECTION_REQUEST_OFF,
)
DEFAULT_CONNECTION_REQUEST_TIMEOUT_SECS = 5.0


@dataclass(frozen=True)
class ReachConfig:
    """How acsd gets queued tasks to claimed CPEs (the `cwmp_reach` section)."""
    # PeriodicInformInterval set on claimed CPEs at BOOTSTRAP; 0 leaves theirs.
    periodic_inform_interval: int = DEFAULT_CLAIMED_INFORM_INTERVAL_SEC
    connection_request: str = CONNECTION_REQUEST_AUTO
    connection_request_timeout_secs: float = DEFAULT_CONNECTION_REQUEST_TIMEOUT_SECS


def get_reach_config(
    service_config: Dict[str, Any],
    mconfig: mconfigs_pb2.AcsD,
) -> ReachConfig:
    """
    Resolve the `cwmp_reach` section of acsd.yml. The mconfig
    periodic_inform_interval wins when set, as the port does, so Orc8r can
    tune it per network; 0 (proto default) falls back to acsd.yml. An
    unknown connection_request mode is an error, as cwmp_auth's is.
    """
    section = service_config.get('cwmp_reach') or {}
    interval = section.get('periodic_inform_interval')
    if interval is None:
        interval = DEFAULT_CLAIMED_INFORM_INTERVAL_SEC
    mode = section.get('connection_request', CONNECTION_REQUEST_AUTO)
    if mode is False:
        # YAML 1.1 reads an unquoted `off` as false.
        mode = CONNECTION_REQUEST_OFF
    mode = str(mode).lower()
    if mode not in CONNECTION_REQUEST_MODES:
        raise ValueError('unknown cwmp_reach connection_request %r' % mode)
    timeout = section.get('connection_request_timeout_secs')
    return ReachConfig(
        periodic_inform_interval=max(0, mconfig.periodic_inform_interval or int(interval)),
        connection_request=mode,
        connection_request_timeout_secs=max(
            0.5, float(timeout or DEFAULT_CONNECTION_REQUEST_TIMEOUT_SECS),
        ),
    )
