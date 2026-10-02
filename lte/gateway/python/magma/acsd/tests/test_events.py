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


import json
import os
import threading
import unittest
from unittest import mock

import fakeredis
import yaml
from lte.protos.mconfig import mconfigs_pb2
from magma.acsd import main, tasks
from magma.acsd.config import MODE_CLAIMED
from magma.acsd.cpe_state import CpeViews
from magma.acsd.events import (
    CPE_SESSION_COMPLETED,
    CPE_TASK_FAILED,
    STREAM_NAME,
    AcsEvents,
    EventEmitter,
)
from magma.acsd.store import (
    SESSION_TIMEOUT_SEC,
    AcsStore,
    Session,
    StoreListener,
    StoreListeners,
)
from prometheus_client import REGISTRY

CPE = 'IMSI001010000000001'
CLAIMED = 'CLAIM-7f3a'
MAGMA_ROOT = os.environ.get('MAGMA_ROOT', '')
SWAGGER_TYPES = {'string': str, 'integer': int, 'number': (int, float)}


class Clock:
    def __init__(self):
        self.now = 1000.0

    def __call__(self):
        return self.now


class FakeEmitter:
    def __init__(self):
        self.events = []

    def emit(self, event_type, tag, value):
        self.events.append((event_type, tag, json.loads(json.dumps(value))))


class AcsEventsTest(unittest.TestCase):
    def setUp(self):
        self.clock = Clock()
        self.emitter = FakeEmitter()
        events = AcsEvents(self.emitter)
        self.store = AcsStore(
            fakeredis.FakeStrictRedis(), clock=self.clock, listener=events,
        )
        events.mode_of = CpeViews(self.store, clock=self.clock).mode_of

    def test_timed_out_session_is_reported(self):
        self.store.put_session(Session('s1', CPE, '10.1.0.5', created=990.0, faults=1))
        self.clock.now += SESSION_TIMEOUT_SEC
        self.store.reap_expired()
        (event_type, tag, value), = self.emitter.events
        self.assertEqual((event_type, tag), (CPE_SESSION_COMPLETED, CPE))
        self.assertEqual(value, {
            'cpe_key': CPE, 'imsi': CPE, 'session_id': 's1', 'result': 'timed_out',
            'reason': 'session timed out', 'started': 990.0, 'ended': 1120.0,
            'tasks_done': 0, 'tasks_failed': 0, 'faults': 1,
        })

    def test_only_final_failures_are_reported(self):
        # A claimed CPE: its cpe_key is no IMSI, whatever it looks like.
        self.store.put_session(Session(
            's0', CLAIMED, '198.51.100.7', session_key='claimed/198.51.100.7/40001',
            mode=MODE_CLAIMED,
        ))
        task = tasks.enqueue_task(self.store, CLAIMED, tasks.REBOOT, max_attempts=2)
        done = tasks.enqueue_task(self.store, CLAIMED, tasks.FACTORY_RESET)
        self.store.complete_task(done.task_id, {})
        self.store.claim_next_task(CLAIMED, 's1')
        self.store.fail_task(task.task_id, 9002, 'busy', retryable=True)
        self.assertEqual(self.emitter.events, [])
        self.store.claim_next_task(CLAIMED, 's2')
        self.store.fail_task(task.task_id, 9002, 'busy', retryable=True)
        (event_type, tag, value), = self.emitter.events
        self.assertEqual((event_type, tag), (CPE_TASK_FAILED, CLAIMED))
        self.assertEqual(value, {
            'cpe_key': CLAIMED, 'imsi': '', 'task_id': task.task_id,
            'task_type': 'reboot', 'attempts': 2, 'max_attempts': 2,
            'fault_code': 9002, 'fault_string': 'busy',
        })

    @unittest.skipUnless(MAGMA_ROOT, 'needs MAGMA_ROOT to read the swagger specs')
    def test_events_match_their_swagger_schemas(self):
        with open(os.path.join(MAGMA_ROOT, 'lte/swagger/cpe_acs_events.v1.yml')) as f:
            definitions = yaml.safe_load(f)['definitions']
        with open(os.path.join(MAGMA_ROOT, 'lte/gateway/configs/eventd.yml')) as f:
            registry = yaml.safe_load(f)['event_registry']
        self.store.put_session(Session('s1', CPE, '10.1.0.5'))
        self.store.end_session('10.1.0.5', 'CPE started a new session')
        task = tasks.enqueue_task(self.store, CPE, tasks.REBOOT, max_attempts=1)
        self.store.fail_task(task.task_id, 9001, 'denied', retryable=False)
        self.assertEqual(len(self.emitter.events), 2)
        for event_type, _, value in self.emitter.events:
            self.assertEqual(
                registry[event_type], {'module': 'lte', 'filename': 'cpe_acs_events.v1.yml'},
            )
            properties = definitions[event_type]['properties']
            self.assertEqual(set(value), set(properties), event_type)
            for name, prop in properties.items():
                self.assertIsInstance(value[name], SWAGGER_TYPES[prop['type']], name)
                if 'enum' in prop:
                    self.assertIn(value[name], prop['enum'])


class EventEmitterTest(unittest.TestCase):
    def test_events_are_sent_from_a_background_thread(self):
        sent = []
        emitter = EventEmitter(log=lambda e: sent.append((e, threading.current_thread().name)))
        emitter.start()
        emitter.emit(CPE_TASK_FAILED, CPE, {'b': 1, 'a': 2})
        emitter.stop()
        (event, thread), = sent
        self.assertEqual(
            (event.stream_name, event.event_type, event.tag, event.value, thread),
            (STREAM_NAME, CPE_TASK_FAILED, CPE, '{"a": 2, "b": 1}', 'acsd-events'),
        )

    def test_full_queue_drops_and_counts(self):
        before = REGISTRY.get_sample_value('acs_events_dropped_total') or 0
        emitter = EventEmitter(log=lambda e: None, max_queued=1)
        emitter.emit(CPE_TASK_FAILED, CPE, {})
        emitter.emit(CPE_TASK_FAILED, CPE, {})
        self.assertEqual(REGISTRY.get_sample_value('acs_events_dropped_total'), before + 1)

    def test_a_failing_send_does_not_stop_the_thread(self):
        sent = []

        def log(event):
            if not sent:
                sent.append('boom')
                raise RuntimeError('eventd down')
            sent.append(event.tag)
        emitter = EventEmitter(log=log).start()
        with self.assertLogs(level='ERROR'):
            emitter.emit(CPE_TASK_FAILED, 'first', {})
            emitter.emit(CPE_TASK_FAILED, 'second', {})
            emitter.stop()
        self.assertEqual(sent, ['boom', 'second'])


class StoreListenersTest(unittest.TestCase):
    def test_fans_out(self):
        a, b = mock.Mock(spec=StoreListener), mock.Mock(spec=StoreListener)
        listeners = StoreListeners(a, b)
        listeners.session_ended('outcome')
        listeners.task_finished('task')
        for listener in (a, b):
            listener.session_ended.assert_called_once_with('outcome')
            listener.task_finished.assert_called_once_with('task')


class MainEventsWiringTest(unittest.TestCase):
    def test_store_feeds_metrics_and_events(self):
        service = mock.Mock(mconfig=mconfigs_pb2.AcsD())
        emitter = mock.Mock()
        emitter.start.return_value = emitter
        with mock.patch.object(main, 'MagmaService', return_value=service), \
                mock.patch.object(main, 'sentry_init'), \
                mock.patch.object(main, 'load_service_config', return_value={}), \
                mock.patch.object(main, 'get_default_client', return_value=fakeredis.FakeStrictRedis()), \
                mock.patch.object(main, 'EventEmitter', return_value=emitter), \
                mock.patch.object(main, 'start_cwmp_listener') as listen:
            main.main()
        store = listen.call_args.args[1].store
        store.put_session(Session('s1', CPE, '10.1.0.5'))
        store.end_session('10.1.0.5', 'test')
        emitter.start.assert_called_once_with()
        self.assertEqual(emitter.emit.call_args.args[:2], (CPE_SESSION_COMPLETED, CPE))


if __name__ == '__main__':
    unittest.main()
