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
from magma.acsd import store as store_mod
from magma.acsd.store import (
    TASK_DONE,
    TASK_EXPIRED,
    TASK_FAILED,
    TASK_IN_PROGRESS,
    TASK_PENDING,
    AcsStore,
    Session,
    TaskNotFound,
)

IMSI = 'IMSI001010000000001'
OTHER = 'IMSI001010000000002'


class Clock:
    def __init__(self, now=1000.0):
        self.now = now

    def __call__(self):
        return self.now


class StoreTest(unittest.TestCase):
    def setUp(self):
        self.redis = fakeredis.FakeStrictRedis()
        self.clock = Clock()
        self.store = AcsStore(self.redis, clock=self.clock)

    def _session(self, session_id='s1', ip='10.1.0.5', imsi=IMSI):
        session = Session(session_id=session_id, imsi=imsi, source_ip=ip)
        self.store.put_session(session)
        return session


class SessionTest(StoreTest):
    def test_session_round_trip_and_ttl(self):
        self._session()
        self.assertEqual(self.store.get_session('10.1.0.5').imsi, IMSI)
        self.clock.now += store_mod.SESSION_TIMEOUT_SEC - 1
        self.assertIsNotNone(self.store.get_session('10.1.0.5'))
        self.clock.now += 1
        self.assertIsNone(self.store.get_session('10.1.0.5'))

    def test_put_session_slides_the_expiry(self):
        session = self._session()
        self.clock.now += 100
        self.store.put_session(session)
        self.clock.now += 100
        self.assertIsNotNone(self.store.get_session('10.1.0.5'))

    def test_end_session_requeues_its_task(self):
        task = self.store.create_task(IMSI, 'reboot')
        self._session()
        self.store.claim_next_task(IMSI, 's1')
        self.assertEqual(self.store.end_session('10.1.0.5', 'gone'), 1)
        task = self.store.get_task(task.task_id)
        self.assertEqual(
            (task.status, task.session_id, task.fault_string),
            (TASK_PENDING, '', 'gone'),
        )
        self.assertIsNone(self.store.get_session('10.1.0.5'))
        self.assertEqual(self.store.end_session('10.1.0.5', 'again'), 0)

    def test_end_imsi_sessions_only_touches_that_cpe(self):
        self._session('s1', '10.1.0.5', IMSI)
        self._session('s2', '10.1.0.6', OTHER)
        self.store.end_imsi_sessions(IMSI, 'new session')
        self.assertIsNone(self.store.get_session('10.1.0.5'))
        self.assertIsNotNone(self.store.get_session('10.1.0.6'))

    def test_state_survives_a_new_store_on_the_same_redis(self):
        task = self.store.create_task(IMSI, 'reboot')
        self._session()
        restarted = AcsStore(self.redis, clock=self.clock)
        self.assertEqual(restarted.get_task(task.task_id).type, 'reboot')
        self.assertEqual(restarted.get_session('10.1.0.5').session_id, 's1')

    def test_restart_requeues_in_flight_claims(self):
        task = self.store.create_task(IMSI, 'reboot')
        self._session()
        self.store.claim_next_task(IMSI, 's1')
        restarted = AcsStore(self.redis, clock=self.clock)
        self.assertEqual(restarted.end_all_sessions('acsd restarted'), 1)
        task = restarted.get_task(task.task_id)
        self.assertEqual((task.status, task.attempts), (TASK_PENDING, 1))
        self.assertEqual(restarted.list_sessions(), [])


class TaskQueueTest(StoreTest):
    def test_tasks_run_in_creation_order_per_imsi(self):
        first = self.store.create_task(IMSI, 'reboot')
        second = self.store.create_task(IMSI, 'refresh')
        self.store.create_task(OTHER, 'reboot')
        self.assertEqual(self.store.claim_next_task(IMSI, 's1').task_id, first.task_id)
        claimed = self.store.claim_next_task(IMSI, 's1')
        self.assertEqual(claimed.task_id, second.task_id)
        self.assertEqual((claimed.status, claimed.attempts), (TASK_IN_PROGRESS, 1))
        self.assertIsNone(self.store.claim_next_task(IMSI, 's1'))
        self.assertEqual(self.store.pending_count(IMSI), 2)
        self.assertEqual(self.store.pending_count(OTHER), 1)

    def test_complete_task_keeps_result(self):
        task = self.store.create_task(IMSI, 'get_parameter_values', {'parameter_names': ['A.']})
        self.store.claim_next_task(IMSI, 's1')
        self.store.save_task_result(task.task_id, {'values': {'A.x': '1'}})
        self.assertEqual(self.store.get_task(task.task_id).result, {'values': {'A.x': '1'}})
        done = self.store.complete_task(task.task_id, {'values': {'A.x': '2'}})
        self.assertEqual((done.status, done.session_id), (TASK_DONE, ''))
        self.assertEqual(self.store.get_task(task.task_id).args, {'parameter_names': ['A.']})
        self.assertEqual(self.store.pending_count(IMSI), 0)

    def test_retryable_fault_requeues_for_the_next_session(self):
        task = self.store.create_task(IMSI, 'reboot')
        self.store.claim_next_task(IMSI, 's1')
        failed = self.store.fail_task(task.task_id, 9002, 'Internal error', retryable=True)
        self.assertEqual((failed.status, failed.fault_code), (TASK_PENDING, 9002))
        self.assertIsNone(self.store.claim_next_task(IMSI, 's1'))
        self.assertEqual(self.store.claim_next_task(IMSI, 's2').attempts, 2)

    def test_retryable_fault_fails_once_attempts_run_out(self):
        task = self.store.create_task(IMSI, 'reboot', max_attempts=2)
        for session in ('s1', 's2'):
            self.store.claim_next_task(IMSI, session)
            last = self.store.fail_task(task.task_id, 9002, 'x', retryable=True)
        self.assertEqual(last.status, TASK_FAILED)
        self.assertIsNone(self.store.claim_next_task(IMSI, 's3'))

    def test_non_retryable_fault_fails_at_once(self):
        task = self.store.create_task(IMSI, 'set_parameter_values')
        self.store.claim_next_task(IMSI, 's1')
        failed = self.store.fail_task(task.task_id, 9007, 'Invalid value', retryable=False)
        self.assertEqual((failed.status, failed.fault_string), (TASK_FAILED, 'Invalid value'))

    def test_unknown_task_raises(self):
        with self.assertRaises(TaskNotFound):
            self.store.complete_task('nope', {})

    def test_task_past_deadline_is_not_claimed_and_expires(self):
        task = self.store.create_task(IMSI, 'reboot', ttl_sec=60)
        self.clock.now += 60
        self.assertIsNone(self.store.claim_next_task(IMSI, 's1'))
        self.assertEqual(self.store.reap_expired().expired_tasks, 1)
        self.assertEqual(self.store.get_task(task.task_id).status, TASK_EXPIRED)

    def test_no_ttl_never_expires(self):
        self.store.create_task(IMSI, 'reboot', ttl_sec=0)
        self.clock.now += 10 * store_mod.TASK_TTL_SEC
        self.assertIsNotNone(self.store.claim_next_task(IMSI, 's1'))

    def test_finished_tasks_are_pruned(self):
        ids = []
        for _ in range(store_mod.FINISHED_TASKS_KEPT + 2):
            task = self.store.create_task(IMSI, 'reboot')
            self.store.claim_next_task(IMSI, 's1')
            self.store.complete_task(task.task_id, {})
            ids.append(task.task_id)
        pending = self.store.create_task(IMSI, 'refresh')
        kept = [t.task_id for t in self.store.list_tasks(IMSI)]
        self.assertEqual(kept, ids[-store_mod.FINISHED_TASKS_KEPT:] + [pending.task_id])
        self.assertIsNone(self.store.get_task(ids[0]))


class ReapTest(StoreTest):
    def test_expired_session_requeues_its_task(self):
        task = self.store.create_task(IMSI, 'reboot')
        self._session()
        self.store.claim_next_task(IMSI, 's1')
        self.clock.now += store_mod.SESSION_TIMEOUT_SEC
        res = self.store.reap_expired()
        self.assertEqual((res.sessions, res.requeued_tasks), (1, 1))
        task = self.store.get_task(task.task_id)
        self.assertEqual((task.status, task.fault_string), (TASK_PENDING, 'session timed out'))

    def test_orphan_out_of_attempts_fails(self):
        task = self.store.create_task(IMSI, 'reboot', max_attempts=1)
        self.store.claim_next_task(IMSI, 'gone')
        self.store.reap_expired()
        self.assertEqual(self.store.get_task(task.task_id).status, TASK_FAILED)

    def test_live_session_keeps_its_task(self):
        task = self.store.create_task(IMSI, 'reboot')
        self._session()
        self.store.claim_next_task(IMSI, 's1')
        self.assertEqual(self.store.reap_expired().requeued_tasks, 0)
        self.assertEqual(self.store.get_task(task.task_id).status, TASK_IN_PROGRESS)


class ParametersAndInformsTest(StoreTest):
    def test_parameters_merge(self):
        self.assertEqual(self.store.get_parameters(IMSI).values, {})
        self.store.merge_parameters(IMSI, {'A': '1', 'B': '2'})
        self.clock.now += 5
        snap = self.store.merge_parameters(IMSI, {'B': '3'})
        self.assertEqual(snap.values, {'A': '1', 'B': '3'})
        self.assertEqual(self.store.get_parameters(IMSI).updated, self.clock.now)

    def test_inform_counts_in_fixed_windows(self):
        self.assertIsNone(self.store.get_inform_count(IMSI))
        self.store.count_inform(IMSI, window_sec=60)
        counts = self.store.count_inform(IMSI, window_sec=60)
        self.assertEqual((counts.count, counts.total, counts.window_end), (2, 2, 1060.0))
        self.clock.now += 60
        counts = self.store.count_inform(IMSI, window_sec=60)
        self.assertEqual((counts.count, counts.total, counts.last_inform), (1, 3, 1060.0))


if __name__ == '__main__':
    unittest.main()


class ModelTest(StoreTest):
    def test_put_and_get_survive_a_restart(self):
        self.assertIsNone(self.store.get_model(IMSI))
        self.store.put_model(IMSI, 'generic', {'root': 'Device.'})
        cpe = AcsStore(self.redis, clock=self.clock).get_model(IMSI)
        self.assertEqual(
            (cpe.imsi, cpe.handler, cpe.model, cpe.updated),
            (IMSI, 'generic', {'root': 'Device.'}, self.clock.now),
        )
