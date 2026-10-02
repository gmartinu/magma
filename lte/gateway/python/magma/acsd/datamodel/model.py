"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

The vendor-independent view of a CPE that acsd reports to the NMS.

The JSON names match the Go model of the cloud ACS (acs/cloud/go/services/
acs/datamodel), plus the `lan` and `wifi` sections the AGW adds. Every
field is optional: None means the CPE did not report it, so a partial model
(from an Inform) can be merged onto a fuller one (from a refresh).
"""
import dataclasses
from dataclasses import dataclass, field
from enum import Enum
from typing import Any, Dict, Optional

ROOT_TR181 = 'Device.'
ROOT_TR098 = 'InternetGatewayDevice.'


@dataclass
class Identity:
    manufacturer: Optional[str] = None
    oui: Optional[str] = None
    product_class: Optional[str] = None
    serial_number: Optional[str] = None
    model_name: Optional[str] = None
    hardware_version: Optional[str] = None


@dataclass
class Firmware:
    software_version: Optional[str] = None


@dataclass
class Cellular:
    """Radio KPIs of the WAN modem, in dB or dBm after quirk scaling."""
    technology: Optional[str] = None
    band: Optional[str] = None
    cell_id: Optional[str] = None
    pci: Optional[str] = None
    rsrp: Optional[float] = None
    rsrq: Optional[float] = None
    sinr: Optional[float] = None
    rssi: Optional[float] = None
    imei: Optional[str] = None
    imsi: Optional[str] = None
    iccid: Optional[str] = None
    apn: Optional[str] = None
    operator: Optional[str] = None


@dataclass
class Wan:
    ipv4_address: Optional[str] = None
    ipv6_address: Optional[str] = None


@dataclass
class Lan:
    ipv4_address: Optional[str] = None
    dhcp_server_enable: Optional[bool] = None
    host_count: Optional[int] = None


@dataclass
class Wifi:
    """The first SSID and radio; enough for the NMS to show Wi-Fi is up."""
    enable: Optional[bool] = None
    ssid: Optional[str] = None
    channel: Optional[int] = None
    standard: Optional[str] = None


@dataclass
class ManagementServer:
    url: Optional[str] = None
    username: Optional[str] = None
    connection_request_url: Optional[str] = None
    periodic_inform_enable: Optional[bool] = None
    periodic_inform_interval: Optional[int] = None


@dataclass
class Model:
    root: Optional[str] = None
    uptime_sec: Optional[int] = None
    identity: Identity = field(default_factory=Identity)
    firmware: Firmware = field(default_factory=Firmware)
    cellular: Cellular = field(default_factory=Cellular)
    wan: Wan = field(default_factory=Wan)
    lan: Lan = field(default_factory=Lan)
    wifi: Wifi = field(default_factory=Wifi)
    management_server: ManagementServer = field(
        default_factory=ManagementServer,
    )

    def get(self, f: 'Field') -> Any:
        section, attr = _split(f)
        target = getattr(self, section) if section else self
        return getattr(target, attr)

    def set(self, f: 'Field', value: Any) -> None:
        section, attr = _split(f)
        target = getattr(self, section) if section else self
        setattr(target, attr, value)

    def merge(self, other: Optional['Model']) -> None:
        """Overwrite this model with every field `other` sets."""
        if other is None:
            return
        if other.root is not None:
            self.root = other.root
        for f in Field:
            value = other.get(f)
            if value is not None:
                self.set(f, value)

    def to_dict(self) -> Dict[str, Any]:
        """JSON-ready dict without unset fields or empty sections."""
        out: Dict[str, Any] = {}
        for f in dataclasses.fields(self):
            value = getattr(self, f.name)
            if dataclasses.is_dataclass(value):
                value = {
                    k: v for k, v in dataclasses.asdict(value).items()
                    if v is not None
                }
                if not value:
                    continue
            elif value is None:
                continue
            out[f.name] = value
        return out


class Field(str, Enum):
    """A settable field of Model, named by its JSON path."""
    MANUFACTURER = 'identity.manufacturer'
    OUI = 'identity.oui'
    PRODUCT_CLASS = 'identity.product_class'
    SERIAL_NUMBER = 'identity.serial_number'
    MODEL_NAME = 'identity.model_name'
    HARDWARE_VERSION = 'identity.hardware_version'
    SOFTWARE_VERSION = 'firmware.software_version'
    UPTIME = 'uptime_sec'
    TECHNOLOGY = 'cellular.technology'
    BAND = 'cellular.band'
    CELL_ID = 'cellular.cell_id'
    PCI = 'cellular.pci'
    RSRP = 'cellular.rsrp'
    RSRQ = 'cellular.rsrq'
    SINR = 'cellular.sinr'
    RSSI = 'cellular.rssi'
    IMEI = 'cellular.imei'
    IMSI = 'cellular.imsi'
    ICCID = 'cellular.iccid'
    APN = 'cellular.apn'
    OPERATOR = 'cellular.operator'
    WAN_IPV4 = 'wan.ipv4_address'
    WAN_IPV6 = 'wan.ipv6_address'
    LAN_IPV4 = 'lan.ipv4_address'
    LAN_DHCP_SERVER_ENABLE = 'lan.dhcp_server_enable'
    LAN_HOST_COUNT = 'lan.host_count'
    WIFI_ENABLE = 'wifi.enable'
    WIFI_SSID = 'wifi.ssid'
    WIFI_CHANNEL = 'wifi.channel'
    WIFI_STANDARD = 'wifi.standard'
    MS_URL = 'management_server.url'
    MS_USERNAME = 'management_server.username'
    MS_CONN_REQ_URL = 'management_server.connection_request_url'
    MS_PERIODIC_INFORM_ENABLE = 'management_server.periodic_inform_enable'
    MS_PERIODIC_INFORM_INTERVAL = 'management_server.periodic_inform_interval'


def _split(f: Field):
    section, _, attr = f.value.rpartition('.')
    return section, attr
