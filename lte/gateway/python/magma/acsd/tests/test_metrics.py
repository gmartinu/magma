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


import os
import unittest
from types import SimpleNamespace

import fakeredis
import yaml
from magma.acsd import tasks
from magma.acsd.cpe_state import CpeView
from magma.acsd.metrics import (
    DEFAULT_CPE_KPI_MAX_CPES,
    AcsMetrics,
    CpeKpiConfig,
    get_cpe_kpi_config,
)
from magma.acsd.session import CwmpSessionHandler
from magma.acsd.store import AcsStore
from magma.tr069 import models
from prometheus_client import REGISTRY

CPE = 'IMSI001010000000001'
IP = '10.1.0.5'
MAGMA_ROOT = os.environ.get('MAGMA_ROOT', '')


def sample(name, **labels):
    return REGISTRY.get_sample_value(name, labels) or 0.0


class Delta:
    """Counter values relative to when it was made: the registry is global."""

    def __init__(self, *keys):
        self._start = {k: sample(k[0], **dict(k[1:])) for k in keys}

    def __getitem__(self, key):
        return sample(key[0], **dict(key[1:])) - self._start[key]


def _ctx():
    return SimpleNamespace(
        transport=SimpleNamespace(req_env={'REMOTE_ADDR': IP}, resp_code=None),
    )


class CountersTest(unittest.TestCase):
    def test_handler_and_store_feed_the_counters(self):
        metrics = AcsMetrics()
        store = AcsStore(fakeredis.FakeStrictRedis(), listener=metrics)
        cpes = iter([CPE, None])
        handler = CwmpSessionHandler(lambda ip, inform: next(cpes), store=store, observer=metrics)
        keys = (
            ('acs_informs_total',), ('acs_sessions_total',), ('acs_sessions_refused_total',),
            ('acs_faults_total', ('code', '9001')), ('acs_faults_total', ('code', 'other')),
            ('acs_tasks_finished_total', ('status', 'failed')),
            ('acs_tasks_finished_total', ('status', 'done')),
            ('acs_sessions_ended_total', ('result', 'completed')),
        )
        delta = Delta(*keys)
        tasks.enqueue_task(store, CPE, tasks.REBOOT)
        tasks.enqueue_task(store, CPE, tasks.FACTORY_RESET)
        inform = models.Inform(DeviceId=models.DeviceIdStruct(SerialNumber='SN1'), MaxEnvelopes=1)
        handler.handle_tr069_message(_ctx(), inform)
        handler.handle_tr069_message(_ctx(), models.DummyInput())
        handler.handle_tr069_message(_ctx(), models.Fault(FaultCode=9001, FaultString='denied'))
        handler.handle_tr069_message(_ctx(), models.FactoryResetResponse())
        handler.handle_tr069_message(_ctx(), inform)
        self.assertEqual([delta[k] for k in keys], [2, 1, 1, 1, 0, 1, 1, 1])

    def test_out_of_range_fault_codes_fold(self):
        delta = Delta(('acs_faults_total', ('code', 'other')))
        AcsMetrics().fault(123456)
        AcsMetrics().fault(0)
        self.assertEqual(delta[('acs_faults_total', ('code', 'other'))], 2)


def _view(cpe_key, online=True, **cellular):
    return CpeView(cpe_key=cpe_key, mode='core', online=online, model={'cellular': cellular})


class GaugesTest(unittest.TestCase):
    def tearDown(self):
        AcsMetrics().refresh([], {})

    def test_task_and_cpe_gauges(self):
        AcsMetrics().refresh(
            [_view('IMSI1'), _view('IMSI2', online=False)], {'pending': 3, 'failed': 1},
        )
        self.assertEqual(sample('acs_tasks', status='pending'), 3)
        self.assertEqual(sample('acs_tasks', status='failed'), 1)
        self.assertEqual((sample('acs_cpes'), sample('acs_online_cpes')), (2, 1))

    def test_per_cpe_kpis_of_online_cpes(self):
        metrics = AcsMetrics()
        metrics.refresh([
            _view('IMSI1', rsrp=-95.5, rsrq='-11', sinr='n/a'),
            _view('IMSI2', online=False, rsrp=-80),
        ], {})
        self.assertEqual(sample('acs_rsrp_dbm', cpe_key='IMSI1'), -95.5)
        self.assertEqual(sample('acs_rsrq_db', cpe_key='IMSI1'), -11)
        self.assertIsNone(REGISTRY.get_sample_value('acs_sinr_db', {'cpe_key': 'IMSI1'}))
        self.assertIsNone(REGISTRY.get_sample_value('acs_rsrp_dbm', {'cpe_key': 'IMSI2'}))
        # IMSI1 goes offline: its series go away.
        metrics.refresh([_view('IMSI1', online=False, rsrp=-95.5)], {})
        self.assertIsNone(REGISTRY.get_sample_value('acs_rsrp_dbm', {'cpe_key': 'IMSI1'}))
        self.assertIsNone(REGISTRY.get_sample_value('acs_rsrq_db', {'cpe_key': 'IMSI1'}))

    def test_cap_keeps_the_same_cpes(self):
        metrics = AcsMetrics(CpeKpiConfig(max_cpes=2))
        views = [_view('IMSI%d' % i, rsrp=-90 - i) for i in (3, 1, 2)]
        metrics.refresh(views, {})
        exported = [
            k for k in ('IMSI1', 'IMSI2', 'IMSI3')
            if REGISTRY.get_sample_value('acs_rsrp_dbm', {'cpe_key': k}) is not None
        ]
        self.assertEqual(exported, ['IMSI1', 'IMSI2'])
        self.assertEqual(sample('acs_cpe_kpis_dropped'), 1)

    def test_disabled_exports_no_per_cpe_series(self):
        AcsMetrics(CpeKpiConfig(enabled=False)).refresh([_view('IMSI1', rsrp=-90)], {})
        self.assertIsNone(REGISTRY.get_sample_value('acs_rsrp_dbm', {'cpe_key': 'IMSI1'}))
        self.assertEqual(sample('acs_cpe_kpis_dropped'), 0)
        self.assertEqual(sample('acs_online_cpes'), 1)


class ConfigTest(unittest.TestCase):
    def test_defaults_and_overrides(self):
        self.assertEqual(get_cpe_kpi_config({}), CpeKpiConfig(True, DEFAULT_CPE_KPI_MAX_CPES))
        self.assertEqual(
            get_cpe_kpi_config({'cpe_kpis': {'enabled': False, 'max_cpes': -5}}),
            CpeKpiConfig(False, 0),
        )

    @unittest.skipUnless(MAGMA_ROOT, 'needs MAGMA_ROOT to read the shipped configs')
    def test_shipped_acsd_yml(self):
        path = os.path.join(MAGMA_ROOT, 'lte/gateway/configs/acsd.yml')
        with open(path) as f:
            self.assertEqual(get_cpe_kpi_config(yaml.safe_load(f)), CpeKpiConfig(True, 200))


if __name__ == '__main__':
    unittest.main()
