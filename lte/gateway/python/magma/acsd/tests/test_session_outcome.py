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
from magma.acsd.session import CwmpSessionHandler
from magma.acsd.store import (
    SESSION_COMPLETED,
    SESSION_INTERRUPTED,
    SESSION_TIMED_OUT,
    SESSION_TIMEOUT_SEC,
    TASK_DONE,
    TASK_EXPIRED,
    TASK_FAILED,
    AcsStore,
    Session,
    StoreListener,
)
from magma.tr069 import models

CPE = 'IMSI001010000000001'
IP = '10.1.0.5'


class Clock:
    def __init__(self):
        self.now = 1000.0

    def __call__(self):
        return self.now


class Recorder(StoreListener):
    def __init__(self):
        self.sessions = []
        self.tasks = []

    def session_ended(self, outcome):
        self.sessions.append(outcome)

    def task_finished(self, task):
        self.tasks.append((task.task_id, task.status))


class OutcomeTest(unittest.TestCase):
    def setUp(self):
        self.clock = Clock()
        self.listener = Recorder()
        self.redis = fakeredis.FakeStrictRedis()
        self.store = AcsStore(self.redis, clock=self.clock, listener=self.listener)

    def _session(self, session_id='s1'):
        session = Session(session_id=session_id, cpe_key=CPE, source_ip=IP, created=990.0)
        self.store.put_session(session)
        return session

    def test_close_session_records_the_callers_counters(self):
        session = self._session()
        session.tasks_done, session.tasks_failed, session.faults = 2, 1, 3
        self.store.close_session(session, SESSION_COMPLETED, 'session ended')
        self.assertIsNone(self.store.get_session(IP))
        last = AcsStore(self.redis, clock=self.clock).get_last_session(CPE)
        self.assertEqual(
            (last.session_id, last.result, last.reason, last.started, last.ended),
            ('s1', SESSION_COMPLETED, 'session ended', 990.0, 1000.0),
        )
        self.assertEqual((last.tasks_done, last.tasks_failed, last.faults), (2, 1, 3))
        self.assertEqual(self.listener.sessions, [last])

    def test_end_session_is_an_interruption_by_default(self):
        self._session()
        self.store.end_session(IP, 'CPE started a new session')
        last = self.store.get_last_session(CPE)
        self.assertEqual(
            (last.result, last.reason), (SESSION_INTERRUPTED, 'CPE started a new session'),
        )
        self.assertEqual(self.store.end_session(IP, 'again'), 0)
        self.assertEqual(len(self.listener.sessions), 1)

    def test_reaped_session_timed_out(self):
        self._session()
        self.clock.now += SESSION_TIMEOUT_SEC
        self.store.reap_expired()
        self.assertEqual(self.store.get_last_session(CPE).result, SESSION_TIMED_OUT)

    def test_unknown_cpe_has_no_last_session(self):
        self.assertIsNone(self.store.get_last_session(CPE))

    def test_listener_hears_final_statuses_only(self):
        done = self.store.create_task(CPE, tasks.REBOOT)
        retried = self.store.create_task(CPE, tasks.REBOOT, max_attempts=2)
        expired = self.store.create_task(CPE, tasks.REBOOT, ttl_sec=10)
        self.store.claim_next_task(CPE, 's1')
        self.store.complete_task(done.task_id, {})
        self.store.claim_next_task(CPE, 's1')
        self.store.fail_task(retried.task_id, 9002, 'busy', retryable=True)
        self.assertEqual(self.listener.tasks, [(done.task_id, TASK_DONE)])
        self.store.claim_next_task(CPE, 's2')
        self.store.fail_task(retried.task_id, 9002, 'busy', retryable=True)
        self.clock.now += 10
        self.store.reap_expired()
        self.assertEqual(self.listener.tasks, [
            (done.task_id, TASK_DONE),
            (retried.task_id, TASK_FAILED),
            (expired.task_id, TASK_EXPIRED),
        ])

    def test_orphan_out_of_attempts_is_heard(self):
        task = self.store.create_task(CPE, tasks.REBOOT, max_attempts=1)
        self.store.claim_next_task(CPE, 'gone')
        self.store.reap_expired()
        self.assertEqual(self.listener.tasks, [(task.task_id, TASK_FAILED)])

    def test_cpe_keys_and_task_counts(self):
        other = 'CLAIM-abc'
        self.store.count_inform(other)
        self.store.count_inform(CPE)
        self.store.count_inform(CPE)
        self.assertEqual(self.store.list_cpe_keys(), [other, CPE])
        self.store.create_task(CPE, tasks.REBOOT)
        task = self.store.create_task(CPE, tasks.REFRESH)
        self.store.fail_task(task.task_id, 9003, 'bad', retryable=False)
        counts = self.store.task_counts()
        self.assertEqual((counts['pending'], counts['failed'], counts['done']), (1, 1, 0))


def _ctx():
    return SimpleNamespace(
        transport=SimpleNamespace(req_env={'REMOTE_ADDR': IP}, resp_code=None),
    )


def _inform():
    return models.Inform(
        DeviceId=models.DeviceIdStruct(SerialNumber='SIM0001'), MaxEnvelopes=1,
    )


class SessionCountersTest(unittest.TestCase):
    def setUp(self):
        self.store = AcsStore(fakeredis.FakeStrictRedis())
        self.handler = CwmpSessionHandler(lambda ip, inform: CPE, store=self.store)

    def send(self, message):
        return self.handler.handle_tr069_message(_ctx(), message)

    def test_completed_session_counts_tasks_and_faults(self):
        tasks.enqueue_task(self.store, CPE, tasks.FACTORY_RESET)
        tasks.enqueue_task(self.store, CPE, tasks.REBOOT, max_attempts=1)
        tasks.enqueue_task(self.store, CPE, tasks.REBOOT)
        self.send(_inform())
        self.send(models.DummyInput())
        self.send(models.FactoryResetResponse())
        self.send(models.Fault(FaultCode=9002, FaultString='busy'))
        self.assertIsInstance(self.send(models.RebootResponse()), models.DummyInput)
        last = self.store.get_last_session(CPE)
        self.assertEqual(
            (last.result, last.tasks_done, last.tasks_failed, last.faults),
            (SESSION_COMPLETED, 2, 1, 1),
        )

    def test_new_inform_interrupts_the_open_session(self):
        tasks.enqueue_task(self.store, CPE, tasks.REBOOT)
        self.send(_inform())
        self.send(models.DummyInput())
        self.send(_inform())
        self.assertEqual(self.store.get_last_session(CPE).result, SESSION_INTERRUPTED)


if __name__ == '__main__':
    unittest.main()
