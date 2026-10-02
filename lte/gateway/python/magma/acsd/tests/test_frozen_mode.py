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
from types import SimpleNamespace
from unittest import mock

import fakeredis
import grpc
from lte.protos import cpe_acs_pb2 as pb
from lte.protos.cpe_acs_pb2_grpc import CpeManagerStub
from lte.protos.mconfig import mconfigs_pb2
from magma.acsd import main, tasks
from magma.acsd.claimed import ClaimedMode
from magma.acsd.claims import ClaimRegistry
from magma.acsd.config import LISTENER_MODE, MODE_CLAIMED
from magma.acsd.cpe_state import CpeViews
from magma.acsd.credentials import CredentialStore
from magma.acsd.digest import DIGEST_USERNAME
from magma.acsd.rpc_servicer import FROZEN, CpeManagerRpcServicer
from magma.acsd.session import CwmpSessionHandler
from magma.acsd.store import TASK_PENDING, AcsStore
from magma.tr069 import models

IMSI = 'IMSI001010000000001'
IP = '10.1.0.5'
OUI, PRODUCT, SERIAL = '00A1B2', 'Titan4000', 'SN0001'
UPTIME = 'Device.DeviceInfo.UpTime'


def _inform(serial='SIM0001', **params):
    return models.Inform(
        DeviceId=models.DeviceIdStruct(
            OUI=OUI, ProductClass=PRODUCT, SerialNumber=serial,
        ),
        MaxEnvelopes=1,
        ParameterList=models.ParameterValueList(ParameterValueStruct=[
            models.ParameterValueStruct(
                Name=name, Value=models.anySimpleType(Data=value),
            )
            for name, value in params.items()
        ]),
    )


def _ctx(env):
    return SimpleNamespace(transport=SimpleNamespace(req_env=env, resp_code=None))


class FrozenSessionTest(unittest.TestCase):
    """A frozen acsd answers Informs and sends the CPE nothing."""

    def setUp(self):
        self.store = AcsStore(fakeredis.FakeStrictRedis())
        self.handler = CwmpSessionHandler(
            lambda ip, inform: IMSI, store=self.store, frozen=True,
        )

    def send(self, message):
        return self.handler.handle_tr069_message(_ctx({'REMOTE_ADDR': IP}), message)

    def test_inform_is_answered_and_recorded(self):
        self.assertIsInstance(self.send(_inform(**{UPTIME: '42'})), models.InformResponse)
        self.assertEqual(self.store.get_parameters(IMSI).values[UPTIME], '42')
        self.assertIsNotNone(self.store.get_model(IMSI))

    def test_queued_tasks_are_not_sent_and_stay_pending(self):
        # Covers every disruptive type: none may reach a frozen CPE.
        queued = [
            tasks.enqueue_task(self.store, IMSI, task_type)
            for task_type in (tasks.REBOOT, tasks.FACTORY_RESET)
        ]
        queued.append(tasks.enqueue_task(
            self.store, IMSI, tasks.SET_PARAMETER_VALUES,
            parameter_values=[{'name': 'Device.X.Y', 'value': '1'}],
        ))
        self.send(_inform())
        self.assertIsInstance(self.send(models.DummyInput()), models.DummyInput)
        self.assertIsNone(self.store.get_session(IP))
        self.assertEqual(
            [self.store.get_task(t.task_id).status for t in queued],
            [TASK_PENDING] * 3,
        )

    def test_an_active_handler_on_the_same_store_runs_them(self):
        task = tasks.enqueue_task(self.store, IMSI, tasks.REBOOT)
        self.send(_inform())
        self.send(models.DummyInput())
        active = CwmpSessionHandler(lambda ip, inform: IMSI, store=self.store)
        ctx = _ctx({'REMOTE_ADDR': IP})
        active.handle_tr069_message(ctx, _inform())
        request = active.handle_tr069_message(_ctx({'REMOTE_ADDR': IP}), models.DummyInput())
        self.assertIsInstance(request, models.Reboot)
        self.assertEqual(request.CommandKey, task.task_id)


class FrozenClaimedTest(unittest.TestCase):
    """No credential rotation starts while frozen."""

    def setUp(self):
        redis = fakeredis.FakeStrictRedis()
        self.store = AcsStore(redis)
        claims = ClaimRegistry(redis)
        claims.add(OUI, PRODUCT, SERIAL, claim_id='titan-1')
        self.creds = CredentialStore(redis, 'magma-acs')
        mode = ClaimedMode(claims, self.creds, self.store, 'bootstrap', 'boot-secret')
        self.handler = CwmpSessionHandler(
            lambda ip, inform: self.fail('core identity used'),
            store=self.store, claimed=mode, frozen=True,
        )

    def test_bootstrap_cpe_is_answered_without_rotation(self):
        env = {
            'REMOTE_ADDR': '203.0.113.7', 'REMOTE_PORT': '40000',
            LISTENER_MODE: MODE_CLAIMED, DIGEST_USERNAME: 'bootstrap',
        }
        resp = self.handler.handle_tr069_message(_ctx(env), _inform(serial=SERIAL))
        self.assertIsInstance(resp, models.InformResponse)
        resp = self.handler.handle_tr069_message(_ctx(env), models.DummyInput())
        self.assertIsInstance(resp, models.DummyInput)
        self.assertFalse(self.creds.get('CLAIMtitan-1').rotation_task_id)
        self.assertEqual(self.store.list_tasks('CLAIMtitan-1'), [])


class FrozenRpcTest(unittest.TestCase):
    def setUp(self):
        self.store = AcsStore(fakeredis.FakeStrictRedis())
        self.server = grpc.server(futures.ThreadPoolExecutor(max_workers=2))
        CpeManagerRpcServicer(
            self.store, CpeViews(self.store), frozen=True,
        ).add_to_server(self.server)
        port = self.server.add_insecure_port('127.0.0.1:0')
        self.server.start()
        self.channel = grpc.insecure_channel('127.0.0.1:%d' % port)
        self.stub = CpeManagerStub(self.channel)

    def tearDown(self):
        self.channel.close()
        self.server.stop(None)

    def test_enqueue_is_refused(self):
        with self.assertRaises(grpc.RpcError) as err:
            self.stub.EnqueueTask(pb.EnqueueTaskRequest(
                cpe_key=IMSI, type=pb.CPE_TASK_TYPE_REBOOT,
            ))
        self.assertEqual(err.exception.code(), grpc.StatusCode.FAILED_PRECONDITION)
        self.assertEqual(err.exception.details(), FROZEN)
        self.assertEqual(self.store.list_tasks(IMSI), [])

    def test_reads_still_work(self):
        self.assertEqual(len(self.stub.ListCpes(pb.ListCpesRequest()).cpes), 0)


class MainFrozenTest(unittest.TestCase):
    def _run(self, mode):
        service = mock.Mock(mconfig=mconfigs_pb2.AcsD(mode=mode))
        with mock.patch.object(main, 'MagmaService', return_value=service), \
                mock.patch.object(main, 'sentry_init'), \
                mock.patch.object(main, 'load_service_config', return_value={}), \
                mock.patch.object(main, 'get_default_client', return_value=fakeredis.FakeStrictRedis()), \
                mock.patch.object(main, 'start_cwmp_listener') as listen, \
                mock.patch.object(main, 'CpeManagerRpcServicer') as servicer:
            main.main()
        return listen.call_args.args[1], servicer.call_args.args[2]

    def test_frozen_mconfig_freezes_handler_and_rpc(self):
        handler, rpc_frozen = self._run(mconfigs_pb2.AcsD.FROZEN)
        self.assertTrue(handler.frozen)
        self.assertTrue(rpc_frozen)

    def test_active_mconfig_is_not_frozen(self):
        handler, rpc_frozen = self._run(mconfigs_pb2.AcsD.ACTIVE)
        self.assertFalse(handler.frozen)
        self.assertFalse(rpc_frozen)


if __name__ == '__main__':
    unittest.main()
