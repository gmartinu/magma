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

import fakeredis
from magma.acsd import tasks
from magma.acsd.datamodel import GENERIC, Quirks, Spec
from magma.acsd.store import AcsStore, Task
from magma.tr069 import models

IMSI = 'IMSI001010000000001'


def _task(task_type, **args):
    return Task(task_id='a' * 32, imsi=IMSI, type=task_type, args=args)


class ValidateTest(unittest.TestCase):
    def test_valid_tasks(self):
        for task_type, args in (
            (tasks.REBOOT, {}),
            (tasks.FACTORY_RESET, {}),
            (tasks.REFRESH, {}),
            (tasks.REFRESH, {'parameter_names': ['Device.Cellular.']}),
            (tasks.GET_PARAMETER_VALUES, {'parameter_names': ['Device.DeviceInfo.UpTime']}),
            (tasks.SET_PARAMETER_VALUES, {'parameter_values': [{'name': 'A.b', 'value': '1', 'type': 'xsd:int'}]}),
            (tasks.GET_PARAMETER_NAMES, {}),
            (tasks.GET_PARAMETER_NAMES, {'parameter_path': 'Device.', 'next_level': True}),
        ):
            tasks.validate(task_type, args)

    def test_invalid_tasks(self):
        for task_type, args in (
            ('upgrade', {}),
            (tasks.REBOOT, {'parameter_names': ['A']}),
            (tasks.REFRESH, {'parameter_path': 'Device.'}),
            (tasks.GET_PARAMETER_VALUES, {}),
            (tasks.GET_PARAMETER_VALUES, {'parameter_names': [' ']}),
            (tasks.SET_PARAMETER_VALUES, {}),
            (tasks.SET_PARAMETER_VALUES, {'parameter_values': [{'name': 'A.', 'value': '1'}]}),
            (tasks.SET_PARAMETER_VALUES, {'parameter_values': [{'name': 'A.b', 'type': 'int'}]}),
            (tasks.GET_PARAMETER_NAMES, {'parameter_path': 'Device.X', 'next_level': True}),
        ):
            with self.assertRaises(tasks.InvalidTask, msg=(task_type, args)):
                tasks.validate(task_type, args)


class EnqueueTest(unittest.TestCase):
    def setUp(self):
        self.store = AcsStore(fakeredis.FakeStrictRedis())

    def test_enqueue_stores_only_the_given_args(self):
        task = tasks.enqueue_task(
            self.store, IMSI, tasks.SET_PARAMETER_VALUES,
            parameter_values=[{'name': 'A.b', 'value': '1'}], max_attempts=5,
        )
        stored = self.store.get_task(task.task_id)
        self.assertEqual(stored.args, {'parameter_values': [{'name': 'A.b', 'value': '1'}]})
        self.assertEqual(stored.max_attempts, 5)
        self.assertEqual(self.store.pending_count(IMSI), 1)

    def test_invalid_task_is_not_queued(self):
        with self.assertRaises(tasks.InvalidTask):
            tasks.enqueue_task(self.store, IMSI, tasks.GET_PARAMETER_VALUES)
        self.assertEqual(self.store.list_tasks(IMSI), [])


class PlanTest(unittest.TestCase):
    def test_reboot_uses_the_task_id_as_command_key(self):
        request, = tasks.plan(_task(tasks.REBOOT), 'Device.')
        self.assertIsInstance(request, models.Reboot)
        self.assertEqual(request.CommandKey, 'a' * 32)

    def test_factory_reset(self):
        request, = tasks.plan(_task(tasks.FACTORY_RESET), 'Device.')
        self.assertIsInstance(request, models.FactoryReset)

    def test_get_parameter_values(self):
        request, = tasks.plan(_task(tasks.GET_PARAMETER_VALUES, parameter_names=['A', 'B']), 'Device.')
        self.assertEqual(request.ParameterNames.string, ['A', 'B'])
        self.assertEqual(request.ParameterNames.arrayType, 'xsd:string[2]')

    def test_refresh_reads_the_generic_handler_paths(self):
        plan = tasks.plan(_task(tasks.REFRESH), 'Device.')
        self.assertEqual(
            [r.ParameterNames.string for r in plan],
            [[p] for p in GENERIC.refresh_paths('Device.')],
        )
        plan = tasks.plan(_task(tasks.REFRESH), 'InternetGatewayDevice.')
        self.assertEqual(len(plan), len(GENERIC.refresh_paths('InternetGatewayDevice.')))
        handler = Spec(name='t', refresh={'Device.': ('Device.',)})
        plan = tasks.plan(_task(tasks.REFRESH), 'Device.', handler)
        self.assertEqual([r.ParameterNames.string for r in plan], [['Device.']])

    def test_get_parameter_values_capped_by_the_handler(self):
        handler = Spec(name='t', quirks=Quirks(max_gpv_names=2))
        plan = tasks.plan(_task(tasks.GET_PARAMETER_VALUES, parameter_names=['A', 'B', 'C']), 'Device.', handler)
        self.assertEqual([r.ParameterNames.string for r in plan], [['A', 'B'], ['C']])
        self.assertEqual([r.ParameterNames.arrayType for r in plan], ['xsd:string[2]', 'xsd:string[1]'])

    def test_refresh_reads_subtrees_alone_and_batches_leaves(self):
        handler = Spec(name='t', quirks=Quirks(max_gpv_names=2))
        plan = tasks.plan(_task(tasks.REFRESH, parameter_names=['A', 'X.', 'B', 'C']), 'Device.', handler)
        self.assertEqual([r.ParameterNames.string for r in plan], [['X.'], ['A', 'B'], ['C']])

    def test_refresh_without_paths_for_the_root_is_empty(self):
        handler = Spec(name='t', refresh={'Device.': ('Device.',)})
        self.assertEqual(tasks.plan(_task(tasks.REFRESH), 'InternetGatewayDevice.', handler), [])

    def test_set_parameter_values(self):
        request, = tasks.plan(
            _task(
                tasks.SET_PARAMETER_VALUES,
                parameter_values=[{'name': 'A.b', 'value': '7', 'type': 'xsd:int'}, {'name': 'A.c', 'value': 'x'}],
            ),
            'Device.',
        )
        structs = request.ParameterList.ParameterValueStruct
        self.assertEqual([(s.Name, s.Value.Data, s.Value.type) for s in structs], [
            ('A.b', '7', 'xsd:int'), ('A.c', 'x', 'xsd:string'),
        ])
        self.assertEqual(request.ParameterList.arrayType, 'cwmp:ParameterValueStruct[2]')
        self.assertEqual(request.ParameterKey.Data, 'a' * 32)

    def test_get_parameter_names_defaults_to_root(self):
        request, = tasks.plan(_task(tasks.GET_PARAMETER_NAMES), 'InternetGatewayDevice.')
        self.assertEqual((request.ParameterPath, request.NextLevel), ('InternetGatewayDevice.', False))


class ApplyTest(unittest.TestCase):
    def test_values_names_and_status(self):
        result = {}
        tasks.apply(result, models.GetParameterValuesResponse(
            ParameterList=models.ParameterValueList(ParameterValueStruct=[
                models.ParameterValueStruct(Name='A', Value=models.anySimpleType(Data='1')),
                models.ParameterValueStruct(Name='B', Value=None),
            ]),
        ))
        tasks.apply(result, models.GetParameterNamesResponse(
            ParameterList=models.ParameterInfoList(ParameterInfoStruct=[
                models.ParameterInfoStruct(Name='A', Writable=True),
            ]),
        ))
        tasks.apply(result, models.SetParameterValuesResponse(Status=1))
        tasks.apply(result, models.RebootResponse())
        self.assertEqual(result, {
            'values': {'A': '1', 'B': ''},
            'names': [{'name': 'A', 'writable': True}],
            'status': 1,
        })

    def test_unexpected_response(self):
        with self.assertRaises(ValueError):
            tasks.apply({}, models.InformResponse())

    def test_fault_rules(self):
        self.assertTrue(tasks.retryable(9002))
        self.assertTrue(tasks.retryable(9004))
        self.assertFalse(tasks.retryable(9005))
        self.assertTrue(tasks.tolerated(tasks.REFRESH, 9005))
        self.assertFalse(tasks.tolerated(tasks.GET_PARAMETER_VALUES, 9005))
        fault = models.Fault(
            FaultCode=9003, FaultString='Invalid arguments',
            SetParameterValuesFault=[models.SetParameterValuesFault(ParameterName='A.b', FaultCode=9007)],
        )
        self.assertEqual(tasks.fault_text(fault), 'Invalid arguments (A.b: 9007)')


if __name__ == '__main__':
    unittest.main()
