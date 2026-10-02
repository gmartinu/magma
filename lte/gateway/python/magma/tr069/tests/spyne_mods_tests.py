"""
Copyright 2020 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
"""
from datetime import datetime
from unittest import TestCase

from magma.tr069 import models
from magma.tr069.spyne_mods import REDACTED, as_dict, redact_secrets


class SpineModsTests(TestCase):
    def test_as_dict(self):
        inp = models.Inform(
            DeviceId=models.DeviceIdStruct(
                Manufacturer='some_manufacturer',
                OUI='123456',
                ProductClass='some_product_class',
                SerialNumber='some_serial_number',
            ),
            Event=models.EventList(
                EventStruct=[
                    models.EventStruct(
                        EventCode='some_event_code',
                        CommandKey='some_command_key',
                    ),
                    models.EventStruct(
                        EventCode='other_event_code',
                        CommandKey='other_command_key',
                    ),
                ],
            ),
            MaxEnvelopes=1234,
            CurrentTime=datetime.fromisoformat('2021-09-15 16:15:43.351680'),
        )
        out = as_dict(inp)
        expected = {
            'DeviceId': {
                'Manufacturer': 'some_manufacturer',
                'OUI': '123456',
                'ProductClass': 'some_product_class',
                'SerialNumber': 'some_serial_number',
            },
            'Event': {
                'EventStruct': [
                    {
                        'EventCode': 'some_event_code',
                        'CommandKey': 'some_command_key',
                    },
                    {
                        'EventCode': 'other_event_code',
                        'CommandKey': 'other_command_key',
                    },
                ],
            },
            'MaxEnvelopes': '1234',
            'CurrentTime': '2021-09-15 16:15:43.351680',
        }
        self.assertEqual(out, expected)


class RedactSecretsTests(TestCase):
    def test_password_values_are_redacted(self):
        spv = models.SetParameterValues(
            ParameterList=models.ParameterValueList(
                ParameterValueStruct=[
                    models.ParameterValueStruct(
                        Name=name, Value=models.anySimpleType(Data=value),
                    )
                    for name, value in (
                        ('Device.ManagementServer.Username', 'cpe-1'),
                        ('Device.ManagementServer.Password', 'hunter2'),
                        ('Device.ManagementServer.ConnectionRequestPassword', 'pw2'),
                    )
                ],
            ),
        )
        out = str(redact_secrets(as_dict(spv)))
        self.assertIn('cpe-1', out)
        self.assertNotIn('hunter2', out)
        self.assertNotIn('pw2', out)
        self.assertEqual(out.count(REDACTED), 2)

    def test_other_shapes_pass_through(self):
        self.assertEqual(redact_secrets('x'), 'x')
        self.assertEqual(
            redact_secrets({'Name': 'A.Password'}), {'Name': 'A.Password'},
        )
