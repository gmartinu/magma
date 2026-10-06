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

"""
acsd's Prometheus metrics, served by Service303 GetMetrics and polled by
magmad's metricsd, which adds the gateway labels. The definitions follow
the cloud ACS KPIs of PR #5 (acs_sessions_total, acs_faults_total,
acs_rsrp_dbm, ...), with cpe_key in place of the TR-069 DeviceId label.
"""

from dataclasses import dataclass
from typing import Any, Dict, Iterable, Set

from magma.acsd.cpe_state import CpeView
from magma.acsd.session import SessionObserver
from magma.acsd.store import SessionOutcome, StoreListener, Task
from prometheus_client import Counter, Gauge

INFORMS = Counter('acs_informs_total', 'CWMP Informs received, refused ones included.')
SESSIONS = Counter('acs_sessions_total', 'CWMP sessions started by an accepted Inform.')
SESSIONS_REFUSED = Counter(
    'acs_sessions_refused_total', 'Informs refused because the CPE could not be identified.',
)
SESSIONS_ENDED = Counter(
    'acs_sessions_ended_total', 'CWMP sessions ended, by result.', ['result'],
)
FAULTS = Counter(
    'acs_faults_total', 'CWMP faults CPEs answered acsd requests with, by fault code.', ['code'],
)
# Only claimed CPEs get here, so the labels are bounded by the claims.
BOOTSTRAP_REFUSED = Counter(
    'acs_bootstrap_refused_total',
    'Bootstrap logins refused because the claimed CPE has its own credential '
    '(a factory reset or a copied serial).',
    ['cpe_key', 'serial'],
)
TASKS_FINISHED = Counter(
    'acs_tasks_finished_total', 'Tasks that reached done, failed or expired.', ['status'],
)
TASKS = Gauge('acs_tasks', 'Tasks in the store, by status.', ['status'])
CPES = Gauge('acs_cpes', 'CPEs that sent an Inform lately (the cpe_acs state rows).')
CPES_ONLINE = Gauge('acs_online_cpes', 'CPEs whose last Inform is within the online window.')
RSRP = Gauge('acs_rsrp_dbm', 'RSRP the CPE last reported, in dBm.', ['cpe_key'])
RSRQ = Gauge('acs_rsrq_db', 'RSRQ the CPE last reported, in dB.', ['cpe_key'])
SINR = Gauge('acs_sinr_db', 'SINR the CPE last reported, in dB.', ['cpe_key'])
CPE_KPIS_DROPPED = Gauge(
    'acs_cpe_kpis_dropped', 'Online CPEs left out of the per-CPE KPIs by cpe_kpis.max_cpes.',
)
CPE_KPIS = (('rsrp', RSRP), ('rsrq', RSRQ), ('sinr', SINR))

# CWMP fault codes are 9000-9899 (vendor ones from 9800); anything else is
# folded so a misbehaving CPE cannot mint label values.
_FAULT_CODES = range(9000, 9900)

# Three series per CPE. 200 CPEs (600 series) covers an FWA gateway with
# room to spare and bounds what one gateway can push to the Orc8r
# Prometheus; a larger site raises it or turns the per-CPE KPIs off.
DEFAULT_CPE_KPI_MAX_CPES = 200


@dataclass(frozen=True)
class CpeKpiConfig:
    enabled: bool = True
    max_cpes: int = DEFAULT_CPE_KPI_MAX_CPES


def get_cpe_kpi_config(service_config: Dict[str, Any]) -> CpeKpiConfig:
    """The `cpe_kpis` section of acsd.yml."""
    section = service_config.get('cpe_kpis') or {}
    return CpeKpiConfig(
        enabled=bool(section.get('enabled', True)),
        max_cpes=max(0, int(section.get('max_cpes', DEFAULT_CPE_KPI_MAX_CPES))),
    )


class AcsMetrics(StoreListener, SessionObserver):
    """
    Counts what the handler and the store report, and refreshes the gauges
    from the store's CPE views on acsd's maintenance timer.
    """

    def __init__(self, kpis: CpeKpiConfig = CpeKpiConfig()):
        self._kpis = kpis

    def inform(self, accepted: bool) -> None:
        INFORMS.inc()
        (SESSIONS if accepted else SESSIONS_REFUSED).inc()

    def fault(self, code: int) -> None:
        FAULTS.labels(str(code) if code in _FAULT_CODES else 'other').inc()

    def session_ended(self, outcome: SessionOutcome) -> None:
        SESSIONS_ENDED.labels(outcome.result).inc()

    def bootstrap_refused(self, cpe_key: str, serial: str, source_ip: str) -> None:
        BOOTSTRAP_REFUSED.labels(cpe_key, serial[:64]).inc()

    def task_finished(self, task: Task) -> None:
        TASKS_FINISHED.labels(task.status).inc()

    def refresh(self, views: Iterable[CpeView], task_counts: Dict[str, int]) -> None:
        for status, count in task_counts.items():
            TASKS.labels(status).set(count)
        views = list(views)
        online = sorted((v for v in views if v.online), key=lambda v: v.cpe_key)
        CPES.set(len(views))
        CPES_ONLINE.set(len(online))
        # Only online CPEs: an offline CPE's last reading would pass for a
        # live one. Picking by cpe_key keeps the series stable under the cap.
        kept = online[:self._kpis.max_cpes] if self._kpis.enabled else []
        CPE_KPIS_DROPPED.set(len(online) - len(kept) if self._kpis.enabled else 0)
        exported = set()
        for view in kept:
            cellular = view.model.get('cellular', {})
            for name, gauge in CPE_KPIS:
                value = _number(cellular.get(name))
                if value is not None:
                    gauge.labels(view.cpe_key).set(value)
                    exported.add((name, view.cpe_key))
        # Stale series are read off the gauges, which are process-wide.
        for name, gauge in CPE_KPIS:
            for cpe_key in _label_values(gauge) - {k for n, k in exported if n == name}:
                gauge.remove(cpe_key)


def _label_values(gauge: Gauge) -> Set[str]:
    return {
        s.labels['cpe_key'] for family in gauge.collect() for s in family.samples
    }


def _number(value):
    try:
        return float(value) if value is not None else None
    except (TypeError, ValueError):
        return None
