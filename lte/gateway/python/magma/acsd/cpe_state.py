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
The per-CPE view acsd reports: the `cpe_acs` operational state, and what the
CpeManager gRPC service answers.

The state is keyed by cpe_key, so a core CPE's row ("IMSI<digits>") joins
the subscriber next to `icmp_monitoring`. The normalized model rides along;
the raw parameter snapshot does not, as it is the bulk of a CPE's data and
GetCpe serves it on demand.
"""

import json
import time
from dataclasses import asdict, dataclass, field
from typing import Any, Callable, Dict, List, Optional

from magma.acsd.config import MODE_CLAIMED, MODE_CORE
from magma.acsd.store import AcsStore, SessionOutcome
from orc8r.protos.service303_pb2 import State

CPE_ACS_STATE_TYPE = 'cpe_acs'
IMSI_PREFIX = 'IMSI'

# A CPE is online while its last Inform is at most this many periodic
# inform intervals old, as in the cloud ACS (PR #5); the default interval
# stands in when neither the CPE nor the mconfig sets one.
ONLINE_INTERVAL_MULTIPLE = 2
DEFAULT_INFORM_INTERVAL_SEC = 3600
# CPEs silent this long drop out of the state report, so the row of a CPE
# that left the gateway does not stay forever. Their records stay in Redis.
STATE_MAX_AGE_SEC = 7 * 24 * 3600


@dataclass
class CpeView:
    cpe_key: str
    mode: str
    imsi: str = ''
    serial_number: str = ''
    oui: str = ''
    product_class: str = ''
    software_version: str = ''
    handler: str = ''
    last_inform: float = 0.0
    informs_total: int = 0
    online: bool = False
    pending_tasks: int = 0
    last_session: Optional[Dict[str, Any]] = None
    model: Dict[str, Any] = field(default_factory=dict)


def cpe_mode(
    open_mode: Optional[str], last: Optional[SessionOutcome],
) -> str:
    """
    The mode of the listener the CPE last came in on: its open session's,
    else its last finished one's. The cpe_key is opaque and says nothing.
    """
    if open_mode:
        return open_mode
    if last is not None and last.mode:
        return last.mode
    return MODE_CORE


def imsi_of(mode: str, cpe_key: str, model: Dict[str, Any]) -> str:
    """The IMSI when known: the key of a core CPE, or the one a claimed CPE
    reports for its SIM."""
    if mode == MODE_CORE:
        return cpe_key
    digits = str(model.get('cellular', {}).get('imsi') or '')
    return IMSI_PREFIX + digits if digits.isdigit() else ''


def inform_interval(model: Dict[str, Any], default_sec: float) -> float:
    reported = model.get('management_server', {}).get('periodic_inform_interval')
    try:
        reported = float(reported or 0)
    except (TypeError, ValueError):
        reported = 0
    return reported if reported > 0 else default_sec


def is_online(
    last_inform: float, model: Dict[str, Any], now: float, default_interval_sec: float,
) -> bool:
    if last_inform <= 0:
        return False
    interval = inform_interval(model, default_interval_sec)
    return now - last_inform <= ONLINE_INTERVAL_MULTIPLE * interval


class CpeViews:
    """Builds CpeViews from the store acsd's CWMP handler writes."""

    def __init__(
        self,
        store: AcsStore,
        default_interval_sec: float = DEFAULT_INFORM_INTERVAL_SEC,
        clock: Callable[[], float] = time.time,
    ):
        self._store = store
        self.default_interval_sec = default_interval_sec or DEFAULT_INFORM_INTERVAL_SEC
        self._clock = clock

    def get(
        self, cpe_key: str, open_modes: Optional[Dict[str, str]] = None,
    ) -> Optional[CpeView]:
        """
        The view of a CPE, or None if acsd never heard from it. open_modes
        maps cpe_key to the mode of its open session; list() passes it so
        the sessions are read once.
        """
        informs = self._store.get_inform_count(cpe_key)
        stored = self._store.get_model(cpe_key)
        if informs is None and stored is None:
            return None
        model = stored.model if stored else {}
        identity = model.get('identity', {})
        last = self._store.get_last_session(cpe_key)
        last_inform = informs.last_inform if informs else 0.0
        if open_modes is None:
            open_modes = self._open_modes()
        mode = cpe_mode(open_modes.get(cpe_key), last)
        return CpeView(
            cpe_key=cpe_key,
            mode=mode,
            imsi=imsi_of(mode, cpe_key, model),
            serial_number=str(identity.get('serial_number') or ''),
            oui=str(identity.get('oui') or ''),
            product_class=str(identity.get('product_class') or ''),
            software_version=str(model.get('firmware', {}).get('software_version') or ''),
            handler=stored.handler if stored else '',
            last_inform=last_inform,
            informs_total=informs.total if informs else 0,
            online=is_online(last_inform, model, self._clock(), self.default_interval_sec),
            pending_tasks=self._store.pending_count(cpe_key),
            last_session=asdict(last) if last else None,
            model=model,
        )

    def list(self, max_age_sec: float = 0) -> List[CpeView]:
        """Every known CPE; with max_age_sec, only those heard from since."""
        now = self._clock()
        open_modes = self._open_modes()
        views = (self.get(key, open_modes) for key in self._store.list_cpe_keys())
        return [
            v for v in views
            if v is not None and (max_age_sec <= 0 or now - v.last_inform <= max_age_sec)
        ]

    def mode_of(self, cpe_key: str) -> str:
        """The mode of one CPE, as get() reports it, without the rest."""
        open_mode = next(
            (s.mode for s in self._store.list_sessions() if s.cpe_key == cpe_key),
            None,
        )
        return cpe_mode(open_mode, self._store.get_last_session(cpe_key))

    def _open_modes(self) -> Dict[str, str]:
        return {s.cpe_key: s.mode for s in self._store.list_sessions()}

    def operational_states(self) -> List[State]:
        """The `cpe_acs` states, one per CPE heard from lately."""
        return [
            State(
                type=CPE_ACS_STATE_TYPE,
                deviceID=view.cpe_key,
                value=json.dumps(asdict(view), sort_keys=True).encode('utf-8'),
            )
            for view in self.list(STATE_MAX_AGE_SEC)
        ]
