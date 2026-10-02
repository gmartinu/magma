"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Serial <-> IMSI binding.

The IMSI (from the source IP) is the trusted identity; the serial number in
the Inform is only a claim. The first contact binds the two. A later change
is accepted and rebinds, but is reported: with no claim flow, moving a SIM
to a replacement CPE (or a new SIM into a CPE) is a legitimate operation,
and refusing it would lock the CPE out with no way back. The report is what
lets an operator spot the illegitimate case.
"""
import enum
import logging
import threading
from dataclasses import dataclass
from typing import Callable, Dict, List, Optional


class BindingChangeKind(enum.Enum):
    # The IMSI was bound to another serial: the SIM moved to another CPE.
    SERIAL_CHANGED = 'serial_changed'
    # The serial was bound to another IMSI: the CPE got another SIM.
    IMSI_CHANGED = 'imsi_changed'


@dataclass(frozen=True)
class BindingChange:
    kind: BindingChangeKind
    imsi: str
    serial: str
    # The serial (SERIAL_CHANGED) or IMSI (IMSI_CHANGED) it replaced.
    previous: str


class InMemoryBindingStore:
    """
    One-to-one serial <-> IMSI map. The MVP keeps it in memory, so a restart
    of acsd treats every CPE as a first contact; a persistent store only
    has to provide these three methods.
    """

    def __init__(self):
        self._serial_by_imsi: Dict[str, str] = {}
        self._imsi_by_serial: Dict[str, str] = {}

    def serial_for(self, imsi: str) -> Optional[str]:
        return self._serial_by_imsi.get(imsi)

    def imsi_for(self, serial: str) -> Optional[str]:
        return self._imsi_by_serial.get(serial)

    def bind(self, imsi: str, serial: str) -> None:
        old_serial = self._serial_by_imsi.pop(imsi, None)
        if old_serial is not None:
            self._imsi_by_serial.pop(old_serial, None)
        old_imsi = self._imsi_by_serial.pop(serial, None)
        if old_imsi is not None:
            self._serial_by_imsi.pop(old_imsi, None)
        self._serial_by_imsi[imsi] = serial
        self._imsi_by_serial[serial] = imsi


def log_binding_change(change: BindingChange) -> None:
    """Default change sink: one structured warning line per change."""
    logging.warning(
        'acsd binding: event=%s imsi=%s serial=%s previous=%s',
        change.kind.value, change.imsi, change.serial, change.previous,
    )


class SerialBinder:
    """
    Binds the Inform serial to the session IMSI and reports changes to
    on_change. The sink is the seam for eventd: the event schemas are
    registered with the rest of acsd's events (Stage 2.5), and until then
    the default sink logs.
    """

    def __init__(
        self,
        store: Optional[InMemoryBindingStore] = None,
        on_change: Callable[[BindingChange], None] = log_binding_change,
    ):
        self._store = store if store is not None else InMemoryBindingStore()
        self._on_change = on_change
        self._lock = threading.Lock()

    def bind(self, imsi: str, serial: str) -> List[BindingChange]:
        """
        Bind serial to imsi and return the changes it caused (empty on a
        first contact or a repeat of the current binding).

        Raises:
            ValueError: imsi or serial is empty.
        """
        if not imsi or not serial:
            raise ValueError(
                'imsi and serial are required: imsi=%r serial=%r'
                % (imsi, serial),
            )
        with self._lock:
            old_serial = self._store.serial_for(imsi)
            old_imsi = self._store.imsi_for(serial)
            changes = []
            if old_serial is not None and old_serial != serial:
                changes.append(BindingChange(
                    BindingChangeKind.SERIAL_CHANGED, imsi, serial, old_serial,
                ))
            if old_imsi is not None and old_imsi != imsi:
                changes.append(BindingChange(
                    BindingChangeKind.IMSI_CHANGED, imsi, serial, old_imsi,
                ))
            if old_serial is None and old_imsi is None:
                logging.info(
                    'acsd binding: first contact imsi=%s serial=%s',
                    imsi, serial,
                )
            if old_serial != serial or old_imsi != imsi:
                self._store.bind(imsi, serial)
        for change in changes:
            try:
                self._on_change(change)
            except Exception:  # pylint: disable=broad-except
                # A broken sink must not fail the CPE's session.
                logging.exception('acsd binding: change sink failed')
        return changes
