"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

The generic handler: the CPEs no model handler claims, read through the
standard TR-181 (Device:2) and TR-098 (InternetGatewayDevice:1) objects.

TR181_FIELDS and TR098_FIELDS are the only place paths are listed; edit
them as lab captures come in. `guess=True` marks a path no real CPE capture
has confirmed yet: either it is not in the standard (the LTE KPIs vendors
expose under ad-hoc names) or the standard leaves the mapping ambiguous
(which IP.Interface is the WAN one). Fields whose LAN and WAN candidates
overlap list the LAN field first, so the WAN one can skip its address.
"""
from magma.acsd.datamodel.model import ROOT_TR098, ROOT_TR181, Field
from magma.acsd.datamodel.spec import Spec, paths

GENERIC_HANDLER_NAME = 'generic'


def _common(root: str):
    """DeviceInfo and ManagementServer, named alike under both roots."""
    return (
        paths(Field.MANUFACTURER, root + 'DeviceInfo.Manufacturer'),
        paths(Field.OUI, root + 'DeviceInfo.ManufacturerOUI'),
        paths(Field.PRODUCT_CLASS, root + 'DeviceInfo.ProductClass'),
        paths(Field.SERIAL_NUMBER, root + 'DeviceInfo.SerialNumber'),
        paths(Field.MODEL_NAME, root + 'DeviceInfo.ModelName'),
        paths(Field.HARDWARE_VERSION, root + 'DeviceInfo.HardwareVersion'),
        paths(Field.SOFTWARE_VERSION, root + 'DeviceInfo.SoftwareVersion'),
        paths(Field.UPTIME, root + 'DeviceInfo.UpTime'),
        paths(Field.MS_URL, root + 'ManagementServer.URL'),
        paths(Field.MS_USERNAME, root + 'ManagementServer.Username'),
        paths(
            Field.MS_CONN_REQ_URL,
            root + 'ManagementServer.ConnectionRequestURL',
        ),
        paths(
            Field.MS_PERIODIC_INFORM_ENABLE,
            root + 'ManagementServer.PeriodicInformEnable',
        ),
        paths(
            Field.MS_PERIODIC_INFORM_INTERVAL,
            root + 'ManagementServer.PeriodicInformInterval',
        ),
    )


_CELL = 'Device.Cellular.Interface.{i}.'
_IP = 'Device.IP.Interface.{i}.'

TR181_FIELDS = _common(ROOT_TR181) + (
    # Device.Cellular, TR-181 2.x.
    paths(Field.TECHNOLOGY, _CELL + 'CurrentAccessTechnology'),
    paths(Field.IMEI, _CELL + 'IMEI'),
    paths(Field.IMSI, _CELL + 'USIM.IMSI'),
    paths(Field.ICCID, _CELL + 'USIM.ICCID'),
    paths(Field.OPERATOR, _CELL + 'NetworkInUse'),
    paths(Field.RSSI, _CELL + 'RSSI'),
    paths(Field.APN, 'Device.Cellular.AccessPoint.{i}.APN'),
    # LTE KPIs: RSRP/RSRQ are only in recent TR-181 revisions, the rest
    # is vendor naming seen across CPEs.
    paths(Field.RSRP, _CELL + 'RSRP', guess=True),
    paths(Field.RSRQ, _CELL + 'RSRQ', guess=True),
    paths(Field.SINR, _CELL + 'SINR', _CELL + 'SNR', guess=True),
    paths(Field.BAND, _CELL + 'Band', _CELL + 'CurrentBand', guess=True),
    paths(Field.CELL_ID, _CELL + 'CellID', _CELL + 'CellId', guess=True),
    paths(Field.PCI, _CELL + 'PCI', _CELL + 'PhysicalCellID', guess=True),
    # LAN: the DHCP pool's router is the CPE's own LAN address.
    paths(Field.LAN_IPV4, 'Device.DHCPv4.Server.Pool.{i}.IPRouters'),
    paths(Field.LAN_DHCP_SERVER_ENABLE, 'Device.DHCPv4.Server.Enable'),
    paths(Field.LAN_HOST_COUNT, 'Device.Hosts.HostNumberOfEntries'),
    # WAN: the first usable address that is not the LAN one; TR-181 does
    # not mark which IP.Interface faces the WAN.
    paths(Field.WAN_IPV4, _IP + 'IPv4Address.{i}.IPAddress', guess=True),
    paths(Field.WAN_IPV6, _IP + 'IPv6Address.{i}.IPAddress', guess=True),
    paths(Field.WIFI_ENABLE, 'Device.WiFi.SSID.{i}.Enable'),
    paths(Field.WIFI_SSID, 'Device.WiFi.SSID.{i}.SSID'),
    paths(Field.WIFI_CHANNEL, 'Device.WiFi.Radio.{i}.Channel'),
    paths(Field.WIFI_STANDARD, 'Device.WiFi.Radio.{i}.OperatingStandards'),
)

_LAN = 'InternetGatewayDevice.LANDevice.{i}.'
_WAN = 'InternetGatewayDevice.WANDevice.{i}.WANConnectionDevice.{i}.'

TR098_FIELDS = _common(ROOT_TR098) + (
    # TR-098 has no cellular object; a handler for a TR-098 LTE CPE adds
    # its vendor paths on top of this table.
    paths(
        Field.LAN_IPV4,
        _LAN + 'LANHostConfigManagement.IPInterface.{i}.IPInterfaceIPAddress',
    ),
    paths(
        Field.LAN_DHCP_SERVER_ENABLE,
        _LAN + 'LANHostConfigManagement.DHCPServerEnable',
    ),
    paths(Field.LAN_HOST_COUNT, _LAN + 'Hosts.HostNumberOfEntries'),
    paths(
        Field.WAN_IPV4,
        _WAN + 'WANIPConnection.{i}.ExternalIPAddress',
        _WAN + 'WANPPPConnection.{i}.ExternalIPAddress',
    ),
    paths(Field.WIFI_ENABLE, _LAN + 'WLANConfiguration.{i}.Enable'),
    paths(Field.WIFI_SSID, _LAN + 'WLANConfiguration.{i}.SSID'),
    paths(Field.WIFI_CHANNEL, _LAN + 'WLANConfiguration.{i}.Channel'),
    paths(Field.WIFI_STANDARD, _LAN + 'WLANConfiguration.{i}.Standard'),
)

GENERIC = Spec(
    name=GENERIC_HANDLER_NAME,
    refresh={
        ROOT_TR181: (
            'Device.DeviceInfo.',
            'Device.ManagementServer.',
            'Device.Cellular.',
            'Device.IP.Interface.',
            'Device.DHCPv4.Server.',
            'Device.WiFi.SSID.',
            'Device.WiFi.Radio.',
            'Device.Hosts.HostNumberOfEntries',
        ),
        ROOT_TR098: (
            'InternetGatewayDevice.DeviceInfo.',
            'InternetGatewayDevice.ManagementServer.',
            'InternetGatewayDevice.WANDevice.',
            'InternetGatewayDevice.LANDevice.',
        ),
    },
    fields={ROOT_TR181: TR181_FIELDS, ROOT_TR098: TR098_FIELDS},
)
