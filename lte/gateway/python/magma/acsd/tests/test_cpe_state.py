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


import asyncio
import json
import unittest
from unittest import mock

import fakeredis
from lte.protos.mconfig import mconfigs_pb2
from magma.acsd import main, tasks
from magma.acsd.cpe_state import (
    CPE_ACS_STATE_TYPE,
    MODE_CLAIMED,
    MODE_CORE,
    STATE_MAX_AGE_SEC,
    CpeViews,
    imsi_of,
    is_online,
)
from magma.acsd.store import SESSION_COMPLETED, AcsStore, Session

CPE = 'IMSI001010000000001'
CLAIMED = 'CLAIM-7f3a'
MODEL = {
    'root': 'Device.',
    'identity': {
        'serial_number': 'SN1', 'oui': '00A0BC', 'product_class': 'Titan4000',
    },
    'firmware': {'software_version': '1.2.3'},
    'cellular': {'rsrp': -95.0},
    'management_server': {'periodic_inform_interval': 300},
}


class Clock:
    def __init__(self):
        self.now = 10000.0

    def __call__(self):
        return self.now


class CpeViewsTest(unittest.TestCase):
    def setUp(self):
        self.clock = Clock()
        self.redis = fakeredis.FakeStrictRedis()
        self.store = AcsStore(self.redis, clock=self.clock)
        self.views = CpeViews(self.store, 0, clock=self.clock)

    def _known(self, key=CPE, model=MODEL):
        self.store.count_inform(key)
        self.store.put_model(key, 'titan', model)

    def test_unknown_cpe_has_no_view(self):
        self.assertIsNone(self.views.get(CPE))
        self.assertEqual(self.views.list(), [])

    def test_view_of_a_core_cpe(self):
        self._known()
        tasks.enqueue_task(self.store, CPE, tasks.REBOOT)
        session = Session('s1', CPE, '10.1.0.5', created=9990.0)
        session.tasks_done = 1
        self.store.close_session(session, SESSION_COMPLETED, 'session ended')
        view = self.views.get(CPE)
        self.assertEqual(
            (view.mode, view.imsi, view.serial_number, view.oui, view.product_class,
             view.software_version, view.handler),
            (MODE_CORE, CPE, 'SN1', '00A0BC', 'Titan4000', '1.2.3', 'titan'),
        )
        self.assertEqual((view.last_inform, view.informs_total), (10000.0, 1))
        self.assertTrue(view.online)
        self.assertEqual(view.pending_tasks, 1)
        self.assertEqual(
            (view.last_session['result'], view.last_session['tasks_done']),
            (SESSION_COMPLETED, 1),
        )
        self.assertEqual(view.model, MODEL)

    def test_claimed_cpe_imsi_comes_from_its_sim(self):
        self.assertEqual(imsi_of(CLAIMED, {'cellular': {'imsi': '310150123456789'}}), 'IMSI310150123456789')
        self.assertEqual(imsi_of(CLAIMED, {'cellular': {'imsi': 'n/a'}}), '')
        self.assertEqual(imsi_of(CLAIMED, {}), '')
        self._known(CLAIMED, {})
        view = self.views.get(CLAIMED)
        self.assertEqual((view.mode, view.imsi, view.serial_number), (MODE_CLAIMED, '', ''))

    def test_online_window_follows_the_inform_interval(self):
        self.assertTrue(is_online(1000, MODEL, 1600, 3600))
        self.assertFalse(is_online(1000, MODEL, 1601, 3600))
        self.assertTrue(is_online(1000, {}, 1000 + 7200, 3600))
        self.assertFalse(is_online(1000, {}, 1000 + 7201, 3600))
        self.assertFalse(is_online(0, {}, 1, 3600))
        broken = {'management_server': {'periodic_inform_interval': 'soon'}}
        self.assertTrue(is_online(1000, broken, 1000 + 7200, 3600))

    def test_mconfig_interval_is_the_default(self):
        self._known(model={})
        views = CpeViews(self.store, 60, clock=self.clock)
        self.clock.now += 121
        self.assertFalse(views.get(CPE).online)
        self.assertTrue(self.views.get(CPE).online)

    def test_operational_states(self):
        self._known()
        self._known(CLAIMED, {})
        self.clock.now += STATE_MAX_AGE_SEC
        self.store.count_inform(CLAIMED)
        self.clock.now += 1
        states = self.views.operational_states()
        self.assertEqual([(s.type, s.deviceID) for s in states], [(CPE_ACS_STATE_TYPE, CLAIMED)])
        value = json.loads(states[0].value)
        self.assertEqual(
            sorted(value),
            sorted([
                'cpe_key', 'mode', 'imsi', 'serial_number', 'oui', 'product_class',
                'software_version', 'handler', 'last_inform', 'informs_total',
                'online', 'pending_tasks', 'last_session', 'model',
            ]),
        )
        self.assertEqual(value['mode'], MODE_CLAIMED)


class MainStateWiringTest(unittest.TestCase):
    def test_main_registers_the_state_and_the_reaper(self):
        service = mock.Mock(mconfig=mconfigs_pb2.AcsD(periodic_inform_interval=600))
        redis = fakeredis.FakeStrictRedis()
        with mock.patch.object(main, 'MagmaService', return_value=service), \
                mock.patch.object(main, 'sentry_init'), \
                mock.patch.object(main, 'load_service_config', return_value={}), \
                mock.patch.object(main, 'get_default_client', return_value=redis), \
                mock.patch.object(main, 'start_cwmp_listener'):
            main.main()
        callback = service.register_operational_states_callback.call_args.args[0]
        self.assertIsInstance(callback.__self__, CpeViews)
        self.assertEqual(callback.__self__.default_interval_sec, 600)
        self.assertEqual(callback(), [])
        service.loop.call_soon.assert_called_once()

    def test_reaper_runs_and_reschedules_even_when_redis_fails(self):
        loop = asyncio.new_event_loop()
        self.addCleanup(loop.close)
        store = mock.Mock()
        calls = []

        def reap():
            calls.append(1)
            if len(calls) == 1:
                raise ConnectionError('redis down')
        store.reap_expired.side_effect = reap
        main.schedule_reaper(loop, store, interval_sec=0)
        loop.call_later(0.05, loop.stop)
        with self.assertLogs(level='ERROR'):
            loop.run_forever()
        self.assertGreaterEqual(store.reap_expired.call_count, 2)


if __name__ == '__main__':
    unittest.main()
