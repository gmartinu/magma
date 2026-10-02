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

import unittest
from types import SimpleNamespace

import fakeredis
from magma.acsd import tasks
from magma.acsd.datamodel import GENERIC, Quirks, Registry, Spec
from magma.acsd.session import CwmpSessionHandler
from magma.acsd.store import TASK_DONE, AcsStore
from magma.tr069 import models

IMSI = 'IMSI001010000000001'
IP = '10.1.0.5'
OUI = '00A1B2'

TITAN = Spec(
    name='titan',
    ouis=frozenset({OUI}),
    refresh={'Device.': ('Device.DeviceInfo.', 'Device.X_TITAN.')},
    quirks=Quirks(max_gpv_names=1),
)


def _ctx(source_ip=IP):
    return SimpleNamespace(
        transport=SimpleNamespace(
            req_env={'REMOTE_ADDR': source_ip}, resp_code=None,
        ),
    )


def _params(values):
    return models.ParameterValueList(ParameterValueStruct=[
        models.ParameterValueStruct(
            Name=name, Value=models.anySimpleType(Data=value),
        )
        for name, value in values.items()
    ])


def _inform(oui='', **params):
    return models.Inform(
        DeviceId=models.DeviceIdStruct(
            Manufacturer='Acme', OUI=oui, ProductClass='CPE',
            SerialNumber='SIM0001',
        ),
        MaxEnvelopes=1,
        ParameterList=_params(params),
    )


def _gpv_response(**values):
    return models.GetParameterValuesResponse(ParameterList=_params(values))


class _Base(unittest.TestCase):
    def setUp(self):
        self.store = AcsStore(fakeredis.FakeStrictRedis())
        self.handler = CwmpSessionHandler(
            lambda ip, inform: IMSI, store=self.store,
            registry=Registry([TITAN]),
        )

    def send(self, message, source_ip=IP):
        return self.handler.handle_tr069_message(_ctx(source_ip), message)

    def enqueue(self, task_type, **kwargs):
        return tasks.enqueue_task(self.store, IMSI, task_type, **kwargs)


class HandlerSelectionTest(_Base):
    def test_handler_picked_from_the_inform_device_id(self):
        self.send(_inform(oui=OUI))
        self.assertEqual(self.store.get_session(IP).model_handler, 'titan')
        self.send(_inform(oui='FFFFFF'))
        self.assertEqual(self.store.get_session(IP).model_handler, GENERIC.name)

    def test_refresh_reads_the_handler_paths(self):
        task = self.enqueue(tasks.REFRESH)
        self.send(_inform(oui=OUI))
        first = self.send(models.DummyInput())
        self.assertEqual(first.ParameterNames.string, ['Device.DeviceInfo.'])
        second = self.send(_gpv_response(**{'Device.DeviceInfo.UpTime': '5'}))
        self.assertEqual(second.ParameterNames.string, ['Device.X_TITAN.'])
        self.send(_gpv_response(**{'Device.X_TITAN.RSRP': '-90'}))
        self.assertEqual(self.store.get_task(task.task_id).status, TASK_DONE)

    def test_gpv_cap_splits_a_task_across_exchanges(self):
        task = self.enqueue(
            tasks.GET_PARAMETER_VALUES, parameter_names=['Device.A', 'Device.B'],
        )
        self.send(_inform(oui=OUI))
        first = self.send(models.DummyInput())
        self.assertEqual(first.ParameterNames.string, ['Device.A'])
        second = self.send(_gpv_response(**{'Device.A': '1'}))
        self.assertEqual(second.ParameterNames.string, ['Device.B'])
        self.assertIsInstance(
            self.send(_gpv_response(**{'Device.B': '2'})), models.DummyInput,
        )
        done = self.store.get_task(task.task_id)
        self.assertEqual(done.status, TASK_DONE)
        self.assertEqual(done.result['values'], {'Device.A': '1', 'Device.B': '2'})

    def test_unknown_stored_handler_falls_back_to_generic(self):
        self.enqueue(tasks.REFRESH)
        self.send(_inform(oui=OUI))
        session = self.store.get_session(IP)
        session.model_handler = 'removed-since'
        self.store.put_session(session)
        first = self.send(models.DummyInput())
        self.assertEqual(
            first.ParameterNames.string, [GENERIC.refresh_paths('Device.')[0]],
        )
