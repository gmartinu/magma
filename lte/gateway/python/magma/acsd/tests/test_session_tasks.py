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

import http.client
import os
import threading
import unittest
import xml.etree.ElementTree as ET
from types import SimpleNamespace

import fakeredis
from magma.acsd import tasks
from magma.acsd.config import CwmpBind
from magma.acsd.datamodel import Registry, Spec
from magma.acsd.server import make_cwmp_server
from magma.acsd.session import CwmpSessionHandler
from magma.acsd.store import (
    SESSION_TIMEOUT_SEC,
    TASK_DONE,
    TASK_FAILED,
    TASK_IN_PROGRESS,
    TASK_PENDING,
    AcsStore,
)
from magma.tr069 import models

IMSI = 'IMSI001010000000001'
IP = '10.1.0.5'
FIXTURES = os.path.join(os.path.dirname(__file__), 'fixtures')
SOAP_NS = 'http://schemas.xmlsoap.org/soap/envelope/'
CWMP_NS = 'urn:dslforum-org:cwmp-1-0'
XML = {'Content-Type': 'text/xml; charset=utf-8'}
ENVELOPE = (
    '<soap-env:Envelope'
    ' xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/"'
    ' xmlns:cwmp="urn:dslforum-org:cwmp-1-0">'
    '<soap-env:Header><cwmp:ID soap-env:mustUnderstand="1">%s</cwmp:ID>'
    '</soap-env:Header><soap-env:Body>%s</soap-env:Body></soap-env:Envelope>'
)
REBOOT_RESPONSE = '<cwmp:RebootResponse/>'
FAULT = (
    '<soap-env:Fault><faultcode>Client</faultcode>'
    '<faultstring>CWMP fault</faultstring><detail><cwmp:Fault>'
    '<FaultCode>%d</FaultCode><FaultString>%s</FaultString>'
    '</cwmp:Fault></detail></soap-env:Fault>'
)


class Clock:
    def __init__(self):
        self.now = 1000.0

    def __call__(self):
        return self.now


def _ctx(source_ip=IP):
    return SimpleNamespace(
        transport=SimpleNamespace(
            req_env={'REMOTE_ADDR': source_ip}, resp_code=None,
        ),
    )


def _inform(**params):
    return models.Inform(
        DeviceId=models.DeviceIdStruct(SerialNumber='SIM0001'),
        MaxEnvelopes=1,
        ParameterList=models.ParameterValueList(ParameterValueStruct=[
            models.ParameterValueStruct(
                Name=name, Value=models.anySimpleType(Data=value),
            )
            for name, value in params.items()
        ]),
    )


def _gpv_response(**values):
    return models.GetParameterValuesResponse(
        ParameterList=models.ParameterValueList(ParameterValueStruct=[
            models.ParameterValueStruct(
                Name=name, Value=models.anySimpleType(Data=value),
            )
            for name, value in values.items()
        ]),
    )


def _fault(code, text='fault'):
    return models.Fault(FaultCode=code, FaultString=text)


class SessionTasksTest(unittest.TestCase):
    """Drives the handler directly, one call per CWMP message."""

    def setUp(self):
        self.clock = Clock()
        self.store = AcsStore(fakeredis.FakeStrictRedis(), clock=self.clock)
        self.handler = CwmpSessionHandler(
            lambda ip, inform: IMSI, store=self.store,
        )

    def send(self, message):
        return self.handler.handle_tr069_message(_ctx(), message)

    def enqueue(self, task_type, **kwargs):
        return tasks.enqueue_task(self.store, IMSI, task_type, **kwargs)

    def status(self, task):
        return self.store.get_task(task.task_id).status

    def test_no_tasks_ends_after_the_empty_post(self):
        self.assertIsInstance(self.send(_inform()), models.InformResponse)
        self.assertIsInstance(self.send(models.DummyInput()), models.DummyInput)
        self.assertIsNone(self.store.get_session(IP))

    def test_queued_reboot_runs_in_the_next_session(self):
        task = self.enqueue(tasks.REBOOT)
        self.send(_inform())
        request = self.send(models.DummyInput())
        self.assertIsInstance(request, models.Reboot)
        self.assertEqual(request.CommandKey, task.task_id)
        self.assertEqual(self.status(task), TASK_IN_PROGRESS)
        self.assertIsInstance(self.send(models.RebootResponse()), models.DummyInput)
        self.assertEqual(self.status(task), TASK_DONE)
        self.assertIsNone(self.store.get_session(IP))

    def test_tasks_drain_in_order_one_rpc_per_exchange(self):
        gpv = self.enqueue(tasks.GET_PARAMETER_VALUES, parameter_names=['Device.DeviceInfo.UpTime'])
        spv = self.enqueue(tasks.SET_PARAMETER_VALUES, parameter_values=[{'name': 'Device.X.Y', 'value': '1'}])
        reset = self.enqueue(tasks.FACTORY_RESET)
        self.send(_inform())
        self.assertIsInstance(self.send(models.DummyInput()), models.GetParameterValues)
        self.assertIsInstance(
            self.send(_gpv_response(**{'Device.DeviceInfo.UpTime': '100'})),
            models.SetParameterValues,
        )
        self.assertIsInstance(
            self.send(models.SetParameterValuesResponse(Status=0)),
            models.FactoryReset,
        )
        self.assertIsInstance(self.send(models.FactoryResetResponse()), models.DummyInput)
        self.assertEqual([self.status(t) for t in (gpv, spv, reset)], [TASK_DONE] * 3)
        self.assertEqual(
            self.store.get_task(gpv.task_id).result,
            {'values': {'Device.DeviceInfo.UpTime': '100'}},
        )
        self.assertEqual(self.store.get_task(spv.task_id).result, {'status': 0})
        self.assertEqual(
            self.store.get_parameters(IMSI).values['Device.DeviceInfo.UpTime'], '100',
        )

    def test_refresh_skips_missing_subtrees_and_updates_the_snapshot(self):
        self.handler = CwmpSessionHandler(
            lambda ip, inform: IMSI, store=self.store, registry=Registry(
                fallback=Spec(name='t', refresh={
                    'Device.': ('Device.DeviceInfo.', 'Device.Cellular.'),
                }),
            ),
        )
        task = self.enqueue(tasks.REFRESH)
        self.send(_inform(**{'Device.DeviceInfo.SerialNumber': 'SIM0001'}))
        first = self.send(models.DummyInput())
        self.assertEqual(first.ParameterNames.string, ['Device.DeviceInfo.'])
        second = self.send(_gpv_response(**{'Device.DeviceInfo.UpTime': '5'}))
        self.assertEqual(second.ParameterNames.string, ['Device.Cellular.'])
        self.assertIsInstance(self.send(_fault(9005, 'Invalid parameter name')), models.DummyInput)
        done = self.store.get_task(task.task_id)
        self.assertEqual(done.status, TASK_DONE)
        self.assertEqual(done.result['faults'][0]['fault_code'], 9005)
        self.assertEqual(self.store.get_parameters(IMSI).values, {
            'Device.DeviceInfo.SerialNumber': 'SIM0001',
            'Device.DeviceInfo.UpTime': '5',
        })

    def test_non_retryable_fault_fails_the_task_and_moves_on(self):
        spv = self.enqueue(tasks.SET_PARAMETER_VALUES, parameter_values=[{'name': 'A.b', 'value': 'x'}])
        reboot = self.enqueue(tasks.REBOOT)
        self.send(_inform())
        self.send(models.DummyInput())
        self.assertIsInstance(self.send(_fault(9007, 'Invalid parameter value')), models.Reboot)
        failed = self.store.get_task(spv.task_id)
        self.assertEqual(
            (failed.status, failed.fault_code, failed.fault_string),
            (TASK_FAILED, 9007, 'Invalid parameter value'),
        )
        self.send(models.RebootResponse())
        self.assertEqual(self.status(reboot), TASK_DONE)

    def test_retryable_fault_waits_for_the_next_session(self):
        task = self.enqueue(tasks.REBOOT)
        self.send(_inform())
        self.send(models.DummyInput())
        self.assertIsInstance(self.send(_fault(9002, 'Internal error')), models.DummyInput)
        self.assertEqual(self.status(task), TASK_PENDING)
        self.send(_inform())
        self.assertIsInstance(self.send(models.DummyInput()), models.Reboot)
        self.send(models.RebootResponse())
        done = self.store.get_task(task.task_id)
        self.assertEqual((done.status, done.attempts), (TASK_DONE, 2))

    def test_empty_post_or_wrong_answer_is_a_retryable_fault(self):
        for answer in (models.DummyInput(), models.SetParameterValuesResponse(Status=0)):
            store = AcsStore(fakeredis.FakeStrictRedis(), clock=self.clock)
            handler = CwmpSessionHandler(lambda ip, inform: IMSI, store=store)
            task = tasks.enqueue_task(store, IMSI, tasks.REBOOT)
            handler.handle_tr069_message(_ctx(), _inform())
            handler.handle_tr069_message(_ctx(), models.DummyInput())
            reply = handler.handle_tr069_message(_ctx(), answer)
            self.assertIsInstance(reply, models.DummyInput)
            failed = store.get_task(task.task_id)
            self.assertEqual((failed.status, failed.fault_code), (TASK_PENDING, 9002))

    def test_new_inform_requeues_the_running_task(self):
        task = self.enqueue(tasks.REBOOT)
        self.send(_inform())
        self.send(models.DummyInput())
        self.send(_inform())
        requeued = self.store.get_task(task.task_id)
        self.assertEqual(
            (requeued.status, requeued.fault_string),
            (TASK_PENDING, 'CPE started a new session'),
        )
        self.assertIsInstance(self.send(models.DummyInput()), models.Reboot)

    def test_session_timeout_requeues_the_running_task(self):
        task = self.enqueue(tasks.REBOOT)
        self.send(_inform())
        self.send(models.DummyInput())
        self.clock.now += SESSION_TIMEOUT_SEC
        # The answer comes too late: the session is gone.
        self.assertIsInstance(self.send(models.RebootResponse()), models.DummyInput)
        self.assertEqual(self.status(task), TASK_IN_PROGRESS)
        self.assertEqual(self.store.reap_expired().requeued_tasks, 1)
        self.assertEqual(self.status(task), TASK_PENDING)

    def test_each_answer_slides_the_session_timeout(self):
        self.enqueue(tasks.GET_PARAMETER_VALUES, parameter_names=['A'])
        self.enqueue(tasks.REBOOT)
        self.send(_inform())
        self.send(models.DummyInput())
        self.clock.now += SESSION_TIMEOUT_SEC - 1
        self.assertIsInstance(self.send(_gpv_response(A='1')), models.Reboot)
        self.clock.now += SESSION_TIMEOUT_SEC - 1
        self.assertIsInstance(self.send(models.RebootResponse()), models.DummyInput)

    def test_inform_counts_and_root(self):
        self.send(_inform(**{'InternetGatewayDevice.DeviceInfo.UpTime': '1'}))
        self.assertEqual(self.store.get_session(IP).root, 'InternetGatewayDevice.')
        self.assertEqual(self.store.get_inform_count(IMSI).total, 1)
        self.assertEqual(self.handler.session_identity(IP), IMSI)


class TaskCaptureReplayTest(unittest.TestCase):
    """Replays the simulator captures through the real listener."""

    def setUp(self):
        self.store = AcsStore(fakeredis.FakeStrictRedis())
        self.server = make_cwmp_server(
            CwmpBind('lo', '127.0.0.1', 0),
            CwmpSessionHandler(lambda ip, inform: IMSI, store=self.store),
            workers=2,
        )
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.conn = http.client.HTTPConnection(
            '127.0.0.1', self.server.server_address[1], timeout=5,
        )

    def tearDown(self):
        self.conn.close()
        self.server.shutdown()
        self.server.server_close()

    def _post(self, body):
        self.conn.request('POST', '/', body, XML)
        resp = self.conn.getresponse()
        return resp, resp.read()

    @staticmethod
    def _fixture(name):
        with open(os.path.join(FIXTURES, name), 'rb') as f:
            return f.read()

    @staticmethod
    def _body(xml):
        return ET.fromstring(xml).find('{%s}Body' % SOAP_NS)[0]

    def test_queued_reboot_runs_on_the_next_session(self):
        resp, _ = self._post(self._fixture('sim4000_inform.xml'))
        self.assertEqual(resp.status, 200)
        self.assertEqual(self._post(b'')[0].status, 204)

        task = tasks.enqueue_task(self.store, IMSI, tasks.REBOOT)
        resp, body = self._post(self._fixture('synthetic_periodic_inform.xml'))
        self.assertEqual(self._body(body).tag, '{%s}InformResponse' % CWMP_NS)
        resp, body = self._post(b'')
        self.assertEqual(resp.status, 200)
        reboot = self._body(body)
        self.assertEqual(reboot.tag, '{%s}Reboot' % CWMP_NS)
        self.assertEqual(reboot.find('CommandKey').text, task.task_id)
        resp, body = self._post((ENVELOPE % ('9', REBOOT_RESPONSE)).encode())
        self.assertEqual((resp.status, body), (204, b''))
        self.assertEqual(self.store.get_task(task.task_id).status, TASK_DONE)

    def test_gpv_capture_lands_in_the_task_and_snapshot(self):
        task = tasks.enqueue_task(
            self.store, IMSI, tasks.GET_PARAMETER_VALUES,
            parameter_names=['Device.DeviceInfo.UpTime', 'Device.Cellular.Interface.1.RSRP'],
        )
        self._post(self._fixture('sim4000_inform.xml'))
        resp, body = self._post(b'')
        gpv = self._body(body)
        self.assertEqual(gpv.tag, '{%s}GetParameterValues' % CWMP_NS)
        self.assertEqual(len(gpv.find('ParameterNames')), 2)
        resp, _ = self._post(self._fixture('sim4000_gpv_response.xml'))
        self.assertEqual(resp.status, 204)
        self.assertEqual(self.store.get_task(task.task_id).result['values'], {
            'Device.DeviceInfo.UpTime': '100',
            'Device.Cellular.Interface.1.RSRP': '-95',
        })
        self.assertEqual(
            self.store.get_parameters(IMSI).values['Device.Cellular.Interface.1.RSRP'], '-95',
        )

    def test_cpe_fault_fails_the_task_on_the_wire(self):
        task = tasks.enqueue_task(
            self.store, IMSI, tasks.SET_PARAMETER_VALUES,
            parameter_values=[{'name': 'Device.X.Y', 'value': '1', 'type': 'xsd:int'}],
        )
        self._post(self._fixture('sim4000_inform.xml'))
        resp, body = self._post(b'')
        spv = self._body(body)
        self.assertEqual(spv.tag, '{%s}SetParameterValues' % CWMP_NS)
        self.assertEqual(spv.find('ParameterKey').text, task.task_id)
        fault = FAULT % (9007, 'Invalid parameter value')
        resp, _ = self._post((ENVELOPE % ('10', fault)).encode())
        self.assertEqual(resp.status, 204)
        failed = self.store.get_task(task.task_id)
        self.assertEqual((failed.status, failed.fault_code), (TASK_FAILED, 9007))


if __name__ == '__main__':
    unittest.main()
