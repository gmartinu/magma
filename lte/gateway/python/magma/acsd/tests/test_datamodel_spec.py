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

import re
import unittest

from magma.acsd.datamodel.model import ROOT_TR181, Field, Model
from magma.acsd.datamodel.spec import (
    DEFAULT_PASSWORD_LENGTH,
    DeviceInfo,
    Quirks,
    Spec,
    gpv_batches,
    paths,
)

R = ROOT_TR181
IFACE = 'Device.IP.Interface.{i}.IPv4Address.{i}.IPAddress'


def _spec(*fps, **kwargs):
    return Spec(name='t', fields={R: tuple(fps)}, **kwargs)


class ModelTest(unittest.TestCase):

    def test_to_dict_drops_unset_fields_and_empty_sections(self):
        m = Model(root=R)
        m.set(Field.RSRP, -95.0)
        m.set(Field.UPTIME, 100)
        self.assertEqual(
            m.to_dict(),
            {'root': R, 'uptime_sec': 100, 'cellular': {'rsrp': -95.0}},
        )

    def test_merge_overwrites_only_what_the_other_sets(self):
        m = Model(root=R)
        m.set(Field.SERIAL_NUMBER, 'S1')
        m.set(Field.RSRP, -100.0)
        o = Model()
        o.set(Field.RSRP, -90.0)
        o.set(Field.WIFI_ENABLE, False)
        m.merge(o)
        m.merge(None)
        self.assertEqual(m.root, R)
        self.assertEqual(m.identity.serial_number, 'S1')
        self.assertEqual(m.cellular.rsrp, -90.0)
        self.assertIs(m.wifi.enable, False)


class NormalizeTest(unittest.TestCase):

    def test_typed_values(self):
        spec = _spec(
            paths(Field.UPTIME, 'Device.DeviceInfo.UpTime'),
            paths(Field.RSRP, 'Device.Cellular.Interface.1.RSRP'),
            paths(Field.WIFI_ENABLE, 'Device.WiFi.SSID.1.Enable'),
            paths(Field.WIFI_SSID, 'Device.WiFi.SSID.1.SSID'),
        )
        m = spec.normalize(R, {
            'Device.DeviceInfo.UpTime': ' 100 ',
            'Device.Cellular.Interface.1.RSRP': '-95.5',
            'Device.WiFi.SSID.1.Enable': 'true',
            'Device.WiFi.SSID.1.SSID': 'home',
        })
        self.assertEqual(m.uptime_sec, 100)
        self.assertEqual(m.cellular.rsrp, -95.5)
        self.assertIs(m.wifi.enable, True)
        self.assertEqual(m.wifi.ssid, 'home')

    def test_bad_or_empty_value_falls_through_to_next_candidate(self):
        spec = _spec(
            paths(Field.SINR, 'A.SINR', 'A.SNR', 'A.Other'),
            paths(Field.LAN_DHCP_SERVER_ENABLE, 'A.Dhcp'),
            paths(Field.MODEL_NAME, 'A.Model'),
        )
        m = spec.normalize(R, {
            'A.SINR': 'n/a', 'A.SNR': '12', 'A.Other': '1',
            'A.Dhcp': 'yes', 'A.Model': '  ',
        })
        self.assertEqual(m.cellular.sinr, 12.0)
        self.assertIsNone(m.lan.dhcp_server_enable)
        self.assertIsNone(m.identity.model_name)

    def test_nan_is_not_a_value(self):
        m = _spec(paths(Field.RSRQ, 'A.RSRQ')).normalize(R, {'A.RSRQ': 'nan'})
        self.assertIsNone(m.cellular.rsrq)

    def test_instances_tried_in_numeric_order(self):
        spec = _spec(paths(Field.IMEI, 'Device.Cellular.Interface.{i}.IMEI'))
        m = spec.normalize(R, {
            'Device.Cellular.Interface.10.IMEI': 'ten',
            'Device.Cellular.Interface.2.IMEI': 'two',
            'Device.Cellular.Interface.2.Extra.IMEI': 'nested',
        })
        self.assertEqual(m.cellular.imei, 'two')

    def test_wan_address_skips_unusable_and_lan_addresses(self):
        spec = _spec(
            paths(Field.LAN_IPV4, 'Device.DHCPv4.Server.Pool.1.IPRouters'),
            paths(Field.WAN_IPV4, IFACE),
            paths(Field.WAN_IPV6, 'Device.IP.Interface.{i}.IPv6Address.{i}.IPAddress'),
        )
        m = spec.normalize(R, {
            'Device.DHCPv4.Server.Pool.1.IPRouters': '192.168.1.1,192.168.1.2',
            'Device.IP.Interface.1.IPv4Address.1.IPAddress': '127.0.0.1',
            'Device.IP.Interface.2.IPv4Address.1.IPAddress': '192.168.1.1',
            'Device.IP.Interface.3.IPv4Address.1.IPAddress': '192.168.128.12',
            'Device.IP.Interface.1.IPv6Address.1.IPAddress': 'fe80::1',
            'Device.IP.Interface.3.IPv6Address.1.IPAddress': '2001:db8::5',
        })
        self.assertEqual(m.lan.ipv4_address, '192.168.1.1')
        self.assertEqual(m.wan.ipv4_address, '192.168.128.12')
        self.assertEqual(m.wan.ipv6_address, '2001:db8::5')

    def test_other_root_has_no_fields(self):
        m = _spec(paths(Field.UPTIME, 'Device.DeviceInfo.UpTime')).normalize(
            'InternetGatewayDevice.', {'Device.DeviceInfo.UpTime': '5'},
        )
        self.assertEqual(m.to_dict(), {'root': 'InternetGatewayDevice.'})


class QuirksTest(unittest.TestCase):

    def test_scale_applies_to_numeric_fields(self):
        spec = _spec(
            paths(Field.RSRQ, 'A.RSRQ'),
            quirks=Quirks(scale={Field.RSRQ: 0.1}),
        )
        self.assertAlmostEqual(
            spec.normalize(R, {'A.RSRQ': '-105'}).cellular.rsrq, -10.5,
        )

    def test_password_length_default(self):
        self.assertEqual(
            Quirks().effective_password_length, DEFAULT_PASSWORD_LENGTH,
        )
        self.assertEqual(Quirks(password_length=16).effective_password_length, 16)

    def test_gpv_batches(self):
        refresh = ['Device.DeviceInfo.', 'A.1', 'A.2', 'A.3', 'Device.WiFi.']
        self.assertEqual(
            gpv_batches(refresh, Quirks(max_gpv_names=2)),
            [['Device.DeviceInfo.'], ['Device.WiFi.'], ['A.1', 'A.2'], ['A.3']],
        )
        self.assertEqual(
            gpv_batches(refresh, Quirks()),
            [['Device.DeviceInfo.'], ['Device.WiFi.'], ['A.1', 'A.2', 'A.3']],
        )
        self.assertEqual(gpv_batches([], Quirks()), [])


class MatchTest(unittest.TestCase):

    def test_criteria(self):
        spec = Spec(
            name='titan', ouis=frozenset({'00a1b2'}),
            product_classes=frozenset({'Titan4000'}),
        )
        self.assertEqual(
            spec.match(DeviceInfo(oui='00A1B2', product_class='titan4000')),
            21,
        )
        self.assertEqual(spec.match(DeviceInfo(oui='00A1B2')), 0)
        self.assertEqual(Spec(name='any').match(DeviceInfo()), 1)

    def test_model_pattern_matches_model_or_product_class(self):
        spec = Spec(name='m', model_pattern=re.compile(r'^Titan ?5400'))
        self.assertEqual(spec.match(DeviceInfo(model_name='Titan 5400')), 11)
        self.assertEqual(spec.match(DeviceInfo(product_class='Titan5400')), 11)
        self.assertEqual(spec.match(DeviceInfo(model_name='Titan 4000')), 0)


class BaseSpecTest(unittest.TestCase):

    def setUp(self):
        self.base = Spec(
            name='base',
            refresh={R: ('Device.DeviceInfo.',)},
            fields={R: (
                paths(Field.UPTIME, 'Device.DeviceInfo.UpTime'),
                paths(Field.RSRP, 'Device.Cellular.Interface.{i}.RSRP', guess=True),
            )},
        )
        self.vendor = Spec(
            name='vendor', base=self.base,
            fields={R: (
                paths(Field.RSRP, 'Device.X_VENDOR.RSRP'),
                paths(Field.BAND, 'Device.X_VENDOR.Band', guess=True),
            )},
        )

    def test_vendor_candidates_first_then_base(self):
        fps = {fp.field: fp for fp in self.vendor.field_paths(R)}
        self.assertEqual(fps[Field.RSRP].candidates, (
            'Device.X_VENDOR.RSRP', 'Device.Cellular.Interface.{i}.RSRP',
        ))
        self.assertIn(Field.UPTIME, fps)
        self.assertIn(Field.BAND, fps)
        m = self.vendor.normalize(R, {
            'Device.X_VENDOR.RSRP': '-80',
            'Device.Cellular.Interface.1.RSRP': '-90',
        })
        self.assertEqual(m.cellular.rsrp, -80.0)

    def test_refresh_and_guesses_inherit(self):
        self.assertEqual(self.vendor.refresh_paths(R), ('Device.DeviceInfo.',))
        self.assertEqual(self.vendor.refresh_paths('X.'), ())
        self.assertEqual(self.base.guessed_fields(R), {Field.RSRP})
        # The vendor path for RSRP is confirmed, so it overrides the guess.
        self.assertEqual(self.vendor.guessed_fields(R), {Field.BAND})


if __name__ == '__main__':
    unittest.main()
