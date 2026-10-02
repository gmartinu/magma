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
from concurrent import futures
from unittest import mock

import fakeredis
import grpc
from lte.protos import cpe_acs_pb2 as pb
from lte.protos.cpe_acs_pb2_grpc import CpeManagerStub
from lte.protos.mconfig import mconfigs_pb2
from magma.acsd import main, tasks
from magma.acsd.cpe_state import CpeViews
from magma.acsd.rpc_servicer import (
    CONNECTION_REQUEST_UNIMPLEMENTED,
    CpeManagerRpcServicer,
)
from magma.acsd.store import (
    SESSION_TIMED_OUT,
    TASK_TTL_SEC,
    AcsStore,
    Session,
)

CPE = 'IMSI001010000000001'
CLAIMED = 'CLAIM-7f3a'
MODEL = {
    'identity': {'serial_number': 'SN1', 'oui': '00A0BC', 'product_class': 'Titan4000'},
    'firmware': {'software_version': '1.2.3'},
    'cellular': {'rsrp': -95.0, 'imsi': '310150123456789'},
}


class Clock:
    def __init__(self):
        self.now = 10000.0

    def __call__(self):
        return self.now


class CpeManagerTest(unittest.TestCase):
    """Through a real in-process gRPC server, so status codes are the wire ones."""

    def setUp(self):
        self.clock = Clock()
        self.store = AcsStore(fakeredis.FakeStrictRedis(), clock=self.clock)
        self.server = grpc.server(futures.ThreadPoolExecutor(max_workers=2))
        CpeManagerRpcServicer(self.store, CpeViews(self.store, clock=self.clock)).add_to_server(self.server)
        port = self.server.add_insecure_port('127.0.0.1:0')
        self.server.start()
        self.channel = grpc.insecure_channel('127.0.0.1:%d' % port)
        self.stub = CpeManagerStub(self.channel)

    def tearDown(self):
        self.channel.close()
        self.server.stop(None)

    def assertCode(self, code, call, *args):
        with self.assertRaises(grpc.RpcError) as ctx:
            call(*args)
        self.assertEqual(ctx.exception.code(), code)
        return ctx.exception.details()

    def _known(self, key=CPE):
        self.store.count_inform(key)
        self.store.put_model(key, 'titan', MODEL)

    def test_enqueue_and_get_task(self):
        task = self.stub.EnqueueTask(pb.EnqueueTaskRequest(
            cpe_key=CPE, type=pb.CPE_TASK_TYPE_SET_PARAMETER_VALUES,
            parameter_values=[
                pb.CpeParameterValue(name='Device.X.Y', value='1'),
                pb.CpeParameterValue(name='Device.X.Z', value='2', type='xsd:int'),
            ],
            max_attempts=5,
        ))
        self.assertEqual(
            (task.cpe_key, task.imsi, task.type, task.status, task.max_attempts),
            (CPE, CPE, pb.CPE_TASK_TYPE_SET_PARAMETER_VALUES, pb.CPE_TASK_STATUS_PENDING, 5),
        )
        self.assertEqual(task.deadline, self.clock.now + TASK_TTL_SEC)
        stored = self.store.get_task(task.task_id)
        self.assertEqual(stored.type, tasks.SET_PARAMETER_VALUES)
        self.assertEqual(stored.args['parameter_values'], [
            {'name': 'Device.X.Y', 'value': '1'},
            {'name': 'Device.X.Z', 'value': '2', 'type': 'xsd:int'},
        ])
        self.assertEqual(self.stub.GetTask(pb.GetTaskRequest(task_id=task.task_id)), task)

    def test_ttl_and_attempt_defaults(self):
        task = self.stub.EnqueueTask(pb.EnqueueTaskRequest(
            cpe_key=CLAIMED, type=pb.CPE_TASK_TYPE_REBOOT, ttl_sec=-1,
        ))
        self.assertEqual((task.deadline, task.max_attempts, task.imsi), (0, 3, ''))
        task = self.stub.EnqueueTask(pb.EnqueueTaskRequest(
            cpe_key=CLAIMED, type=pb.CPE_TASK_TYPE_REBOOT, ttl_sec=60,
        ))
        self.assertEqual(task.deadline, self.clock.now + 60)

    def test_invalid_tasks_are_invalid_argument(self):
        for request in (
            pb.EnqueueTaskRequest(type=pb.CPE_TASK_TYPE_REBOOT),
            pb.EnqueueTaskRequest(cpe_key=CPE),
            pb.EnqueueTaskRequest(cpe_key=CPE, type=99),
            pb.EnqueueTaskRequest(cpe_key=CPE, type=pb.CPE_TASK_TYPE_GET_PARAMETER_VALUES),
            pb.EnqueueTaskRequest(
                cpe_key=CPE, type=pb.CPE_TASK_TYPE_GET_PARAMETER_NAMES, parameter_path='Device',
                next_level=True,
            ),
        ):
            self.assertCode(grpc.StatusCode.INVALID_ARGUMENT, self.stub.EnqueueTask, request)
        self.assertEqual(self.store.list_tasks(CPE), [])

    def test_every_task_type_maps_to_the_store(self):
        names = {
            pb.CPE_TASK_TYPE_REBOOT: tasks.REBOOT,
            pb.CPE_TASK_TYPE_FACTORY_RESET: tasks.FACTORY_RESET,
            pb.CPE_TASK_TYPE_REFRESH: tasks.REFRESH,
            pb.CPE_TASK_TYPE_GET_PARAMETER_VALUES: tasks.GET_PARAMETER_VALUES,
            pb.CPE_TASK_TYPE_SET_PARAMETER_VALUES: tasks.SET_PARAMETER_VALUES,
            pb.CPE_TASK_TYPE_GET_PARAMETER_NAMES: tasks.GET_PARAMETER_NAMES,
        }
        self.assertEqual(sorted(names.values()), sorted(tasks.TYPES))
        args = {
            pb.CPE_TASK_TYPE_GET_PARAMETER_VALUES: {'parameter_names': ['Device.']},
            pb.CPE_TASK_TYPE_SET_PARAMETER_VALUES: {
                'parameter_values': [pb.CpeParameterValue(name='Device.A', value='1')],
            },
            pb.CPE_TASK_TYPE_GET_PARAMETER_NAMES: {'parameter_path': 'Device.', 'next_level': True},
        }
        for value, name in names.items():
            task = self.stub.EnqueueTask(pb.EnqueueTaskRequest(
                cpe_key=CPE, type=value, **args.get(value, {}),
            ))
            self.assertEqual((self.store.get_task(task.task_id).type, task.type), (name, value))

    def test_failed_task_carries_its_fault(self):
        task = tasks.enqueue_task(self.store, CPE, tasks.REBOOT)
        self.store.claim_next_task(CPE, 's1')
        self.store.fail_task(task.task_id, 9001, 'Request denied', retryable=False)
        got = self.stub.GetTask(pb.GetTaskRequest(task_id=task.task_id))
        self.assertEqual(
            (got.status, got.attempts, got.fault_code, got.fault_string),
            (pb.CPE_TASK_STATUS_FAILED, 1, 9001, 'Request denied'),
        )

    def test_unknown_task_is_not_found(self):
        self.assertCode(grpc.StatusCode.NOT_FOUND, self.stub.GetTask, pb.GetTaskRequest(task_id='nope'))
        self.assertCode(grpc.StatusCode.NOT_FOUND, self.stub.GetTask, pb.GetTaskRequest())

    def test_list_cpes_is_compact(self):
        self._known()
        self._known(CLAIMED)
        cpes = self.stub.ListCpes(pb.ListCpesRequest()).cpes
        self.assertEqual([c.cpe_key for c in cpes], [CLAIMED, CPE])
        claimed, core = cpes
        self.assertEqual((core.mode, core.imsi), (pb.CPE_MODE_CORE, CPE))
        self.assertEqual((claimed.mode, claimed.imsi), (pb.CPE_MODE_CLAIMED, 'IMSI310150123456789'))
        self.assertEqual(
            (core.serial_number, core.oui, core.product_class, core.software_version, core.handler),
            ('SN1', '00A0BC', 'Titan4000', '1.2.3', 'titan'),
        )
        self.assertTrue(core.online)
        self.assertFalse(core.HasField('model'))
        self.assertFalse(core.HasField('last_session'))
        self.assertEqual(len(core.tasks), 0)

    def test_get_cpe_with_model_tasks_session_and_parameters(self):
        self._known()
        self.store.merge_parameters(CPE, {'Device.DeviceInfo.UpTime': '42'})
        task = tasks.enqueue_task(self.store, CPE, tasks.REBOOT)
        session = Session('s1', CPE, '10.1.0.5', created=9990.0, faults=2)
        self.store.put_session(session)
        self.clock.now += 1000
        self.store.reap_expired()
        cpe = self.stub.GetCpe(pb.GetCpeRequest(cpe_key=CPE))
        self.assertEqual(cpe.model['firmware']['software_version'], '1.2.3')
        self.assertEqual(cpe.model['cellular']['rsrp'], -95.0)
        self.assertEqual([t.task_id for t in cpe.tasks], [task.task_id])
        self.assertEqual(cpe.pending_tasks, 1)
        self.assertEqual(
            (cpe.last_session.session_id, cpe.last_session.result, cpe.last_session.faults,
             cpe.last_session.started, cpe.last_session.ended),
            ('s1', SESSION_TIMED_OUT, 2, 9990.0, 11000.0),
        )
        self.assertEqual(dict(cpe.parameters), {})
        cpe = self.stub.GetCpe(pb.GetCpeRequest(cpe_key=CPE, include_parameters=True))
        self.assertEqual(dict(cpe.parameters), {'Device.DeviceInfo.UpTime': '42'})

    def test_unknown_cpe_is_not_found(self):
        self.assertCode(grpc.StatusCode.NOT_FOUND, self.stub.GetCpe, pb.GetCpeRequest(cpe_key=CPE))
        self.assertCode(grpc.StatusCode.NOT_FOUND, self.stub.GetCpe, pb.GetCpeRequest())

    def test_connection_request_is_unimplemented(self):
        details = self.assertCode(
            grpc.StatusCode.UNIMPLEMENTED, self.stub.ConnectionRequest,
            pb.ConnectionRequestRequest(cpe_key=CPE),
        )
        self.assertEqual(details, CONNECTION_REQUEST_UNIMPLEMENTED)


class MainServesCpeManagerTest(unittest.TestCase):
    def test_servicer_shares_the_handlers_store(self):
        service = mock.Mock(mconfig=mconfigs_pb2.AcsD())
        with mock.patch.object(main, 'MagmaService', return_value=service), \
                mock.patch.object(main, 'sentry_init'), \
                mock.patch.object(main, 'load_service_config', return_value={}), \
                mock.patch.object(main, 'get_default_client', return_value=fakeredis.FakeStrictRedis()), \
                mock.patch.object(main, 'start_cwmp_listener') as listen, \
                mock.patch.object(main, 'CpeManagerRpcServicer') as servicer:
            main.main()
        handler = listen.call_args.args[1]
        store, views = servicer.call_args.args
        self.assertIs(store, handler.store)
        self.assertIs(views._store, handler.store)
        servicer.return_value.add_to_server.assert_called_once_with(service.rpc_server)


if __name__ == '__main__':
    unittest.main()
