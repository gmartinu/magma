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

import glob
import io
import os
import re
import unittest

from magma.acsd.datamodel import (
    GENERIC,
    GENERIC_HANDLER_NAME,
    ROOT_TR098,
    ROOT_TR181,
    DeviceInfo,
    Field,
    Quirks,
    Registry,
    Spec,
    detect_root,
    device_info,
    normalize_inform,
    params_of,
)
from magma.acsd.server import make_cwmp_app
from magma.tr069 import models

FIXTURES = os.path.join(os.path.dirname(__file__), 'fixtures')


class _Recorder:
    def __init__(self):
        self.messages = []

    def handle_tr069_message(self, ctx, message):
        self.messages.append(message)
        return models.DummyInput()


def _decode(name):
    """Decode a captured CWMP request through the acsd SOAP stack."""
    with open(os.path.join(FIXTURES, name), 'rb') as f:
        body = f.read()
    recorder = _Recorder()
    make_cwmp_app(recorder)({
        'REQUEST_METHOD': 'POST',
        'CONTENT_TYPE': 'text/xml; charset=utf-8',
        'CONTENT_LENGTH': str(len(body)),
        'wsgi.input': io.BytesIO(body),
        'wsgi.url_scheme': 'http',
        'REMOTE_ADDR': '192.168.128.12',
        'SERVER_NAME': 'acsd',
        'SERVER_PORT': '48080',
        'PATH_INFO': '/',
        'QUERY_STRING': '',
    }, lambda status, headers, exc_info=None: None)
    assert len(recorder.messages) == 1, name
    return recorder.messages[0]


class CaptureTest(unittest.TestCase):

    def test_simulator_session(self):
        handler, model = normalize_inform(_decode('sim4000_inform.xml'))
        self.assertEqual(handler.name, GENERIC_HANDLER_NAME)
        model.merge(handler.normalize(
            model.root, params_of(_decode('sim4000_gpv_response.xml')),
        ))
        self.assertEqual(model.to_dict(), {
            'root': ROOT_TR181,
            'uptime_sec': 100,
            'identity': {
                'manufacturer': 'Global Telecom (sim)',
                'oui': '00A1B2',
                'product_class': 'SIM4000',
                'serial_number': 'SIM0001',
                'hardware_version': 'HW-A',
            },
            'firmware': {'software_version': '1.0.0-sim'},
            'cellular': {'rsrp': -95.0},
            'management_server': {
                'connection_request_url': 'http://host.docker.internal:7548/cr',
            },
        })

    def test_periodic_inform_identity_comes_from_device_id(self):
        _, model = normalize_inform(_decode('synthetic_periodic_inform.xml'))
        self.assertEqual(model.identity.serial_number, 'SYN0000042')
        self.assertEqual(model.firmware.software_version, '1.2.3')
        self.assertEqual(model.management_server.periodic_inform_interval, 300)

    def test_tr181_refresh(self):
        m = GENERIC.normalize(
            ROOT_TR181, params_of(_decode('synthetic_tr181_gpv_response.xml')),
        )
        d = m.to_dict()
        self.assertEqual(d['cellular'], {
            'technology': 'LTE', 'band': 'B48', 'cell_id': '00101A2B3',
            'pci': '123', 'rsrp': -97.0, 'rsrq': -11.0, 'sinr': 14.0,
            'rssi': -65.0, 'imei': '356938035643809',
            'imsi': '001010000000001', 'iccid': '8901260000000000001',
            'apn': 'internet', 'operator': '00101',
        })
        self.assertEqual(d['wan'], {'ipv4_address': '192.168.128.12'})
        self.assertEqual(d['lan'], {
            'ipv4_address': '192.168.1.1', 'dhcp_server_enable': True,
            'host_count': 3,
        })
        self.assertEqual(d['wifi'], {
            'enable': True, 'ssid': 'Synthetic-WiFi', 'channel': 6,
            'standard': 'b,g,n',
        })
        self.assertEqual(d['uptime_sec'], 86400)
        self.assertEqual(d['identity'], {'model_name': 'Synthetic LTE CPE'})

    def test_tr098_inform(self):
        handler, m = normalize_inform(_decode('synthetic_tr098_inform.xml'))
        self.assertEqual(handler.name, GENERIC_HANDLER_NAME)
        self.assertEqual(m.root, ROOT_TR098)
        self.assertEqual(m.identity.model_name, 'Synthetic IGD')
        self.assertEqual(m.identity.serial_number, 'SYN098')
        self.assertEqual(m.firmware.software_version, '2.0.1')
        self.assertEqual(m.uptime_sec, 3600)
        self.assertEqual(m.wan.ipv4_address, '192.168.128.20')
        self.assertEqual(m.lan.ipv4_address, '192.168.0.1')
        self.assertEqual(m.lan.host_count, 2)
        self.assertIs(m.wifi.enable, False)
        self.assertEqual(m.wifi.channel, 11)
        self.assertIs(m.management_server.periodic_inform_enable, True)
        self.assertEqual(m.to_dict().get('cellular'), None)

    def test_every_captured_inform_normalizes(self):
        # New lab captures dropped in fixtures/ are exercised with no code
        # change.
        informs = glob.glob(os.path.join(FIXTURES, '*inform*.xml'))
        self.assertTrue(informs)
        for path in informs:
            inform = _decode(os.path.basename(path))
            _, m = normalize_inform(inform)
            self.assertIsNotNone(m.root, path)
            self.assertTrue(m.identity.serial_number, path)
            self.assertTrue(m.firmware.software_version, path)

    def test_vendor_quirks_apply_on_selected_handler(self):
        vendor = Spec(
            name='synthetic-lte',
            model_pattern=re.compile('^Synthetic LTE'),
            base=GENERIC,
            quirks=Quirks(scale={Field.RSRQ: 0.1}),
        )
        params = params_of(_decode('synthetic_tr181_gpv_response.xml'))
        handler = Registry([vendor]).select(device_info(None, params))
        self.assertIs(handler, vendor)
        self.assertAlmostEqual(
            handler.normalize(ROOT_TR181, params).cellular.rsrq, -1.1,
        )


class RegistryTest(unittest.TestCase):

    def test_select(self):
        titan = Spec(
            name='titan4000', ouis=frozenset({'00a1b2'}),
            product_classes=frozenset({'Titan4000'}),
        )
        any_gt = Spec(name='gt', ouis=frozenset({'00A1B2'}))
        by_model = Spec(
            name='titan5400', model_pattern=re.compile(r'^Titan ?5400'),
            priority=5,
        )
        r = Registry([any_gt, titan, by_model])
        self.assertEqual(r.select(DeviceInfo(
            oui='00A1B2', product_class='titan4000',
        )).name, 'titan4000')
        self.assertEqual(r.select(DeviceInfo(
            oui='00A1B2', product_class='Other',
        )).name, 'gt')
        self.assertEqual(r.select(DeviceInfo(
            oui='FFFFFF', model_name='Titan 5400',
        )).name, 'titan5400')
        self.assertIs(r.select(DeviceInfo(oui='FFFFFF')), GENERIC)
        self.assertIs(r.get('titan4000'), titan)
        self.assertIs(r.get('removed'), GENERIC)
        self.assertEqual(r.get('gt').quirks, Quirks())

    def test_ties_go_to_first_registered(self):
        a, b = Spec(name='a'), Spec(name='b')
        self.assertIs(Registry([a, b]).select(DeviceInfo()), a)

    def test_device_info_from_params(self):
        info = device_info(models.Inform(DeviceId=models.DeviceIdStruct(
            Manufacturer='GT', OUI='O', ProductClass='P', SerialNumber='S',
        )), {
            'InternetGatewayDevice.DeviceInfo.ModelName': 'Titan 5400',
            'InternetGatewayDevice.DeviceInfo.SoftwareVersion': '1.0',
        })
        self.assertEqual(info, DeviceInfo(
            manufacturer='GT', oui='O', product_class='P',
            model_name='Titan 5400', software_version='1.0',
        ))

    def test_detect_root(self):
        self.assertEqual(detect_root(['Device.A', 'X']), ROOT_TR181)
        self.assertEqual(
            detect_root(['Device.A', 'InternetGatewayDevice.B']), ROOT_TR098,
        )
        self.assertIsNone(detect_root(['X.Y']))
        self.assertIsNone(detect_root([]))

    def test_params_of_tolerates_missing_parts(self):
        self.assertEqual(params_of(models.Inform()), {})
        self.assertEqual(params_of(None), {})
        msg = models.GetParameterValuesResponse(
            ParameterList=models.ParameterValueList(ParameterValueStruct=[
                models.ParameterValueStruct(
                    Name='A', Value=models.anySimpleType(Data='1'),
                ),
                models.ParameterValueStruct(Name='B', Value=None),
                models.ParameterValueStruct(Name=None),
            ]),
        )
        self.assertEqual(params_of(msg), {'A': '1', 'B': ''})

    def test_generic_guesses_are_the_unconfirmed_kpis(self):
        self.assertEqual(GENERIC.guessed_fields(ROOT_TR181), {
            Field.RSRP, Field.RSRQ, Field.SINR, Field.BAND, Field.CELL_ID,
            Field.PCI, Field.WAN_IPV4, Field.WAN_IPV6,
        })
        self.assertEqual(GENERIC.guessed_fields(ROOT_TR098), frozenset())


if __name__ == '__main__':
    unittest.main()
