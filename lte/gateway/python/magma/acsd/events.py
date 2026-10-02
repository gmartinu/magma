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
eventd events of acsd: `cpe_session_completed` when a CWMP session ends
(whatever the result) and `cpe_task_failed` when a task fails for good.
Schemas: lte/swagger/cpe_acs_events.v1.yml.
"""

import json
import logging
import queue
import threading
from typing import Any, Callable, Dict, Optional

from magma.acsd.cpe_state import imsi_of
from magma.acsd.store import TASK_FAILED, SessionOutcome, StoreListener, Task
from magma.eventd.eventd_client import log_event
from orc8r.protos.eventd_pb2 import Event
from prometheus_client import Counter

STREAM_NAME = 'acsd'
CPE_SESSION_COMPLETED = 'cpe_session_completed'
CPE_TASK_FAILED = 'cpe_task_failed'
MAX_QUEUED_EVENTS = 1000

EVENTS_DROPPED = Counter(
    'acs_events_dropped_total', 'eventd events dropped because the queue to eventd was full.',
)


class EventEmitter:
    """
    Sends events to eventd from one background thread. log_event is a
    blocking gRPC call with a 10 s timeout, and events are raised under the
    store lock on CWMP worker threads, so a slow eventd must not reach them:
    past MAX_QUEUED_EVENTS, events are dropped and counted.
    """

    def __init__(
        self,
        log: Callable[[Event], None] = log_event,
        max_queued: int = MAX_QUEUED_EVENTS,
    ):
        self._log = log
        self._queue: 'queue.Queue[Optional[Event]]' = queue.Queue(max_queued)
        self._thread: Optional[threading.Thread] = None

    def start(self) -> 'EventEmitter':
        self._thread = threading.Thread(target=self._run, name='acsd-events', daemon=True)
        self._thread.start()
        return self

    def stop(self) -> None:
        """Send what is queued, then end the thread."""
        if self._thread:
            self._queue.put(None)
            self._thread.join()

    def emit(self, event_type: str, tag: str, value: Dict[str, Any]) -> None:
        event = Event(
            stream_name=STREAM_NAME, event_type=event_type, tag=tag,
            value=json.dumps(value, sort_keys=True),
        )
        try:
            self._queue.put_nowait(event)
        except queue.Full:
            EVENTS_DROPPED.inc()

    def _run(self) -> None:
        while True:
            event = self._queue.get()
            if event is None:
                return
            try:
                self._log(event)
            except Exception:  # pylint: disable=broad-except
                logging.exception('Sending %s to eventd failed', event.event_type)


class AcsEvents(StoreListener):
    """Turns store notifications into eventd events, tagged by cpe_key."""

    def __init__(self, emitter: EventEmitter):
        self._emitter = emitter

    def session_ended(self, outcome: SessionOutcome) -> None:
        self._emitter.emit(CPE_SESSION_COMPLETED, outcome.cpe_key, {
            'cpe_key': outcome.cpe_key,
            'imsi': imsi_of(outcome.cpe_key, {}),
            'session_id': outcome.session_id,
            'result': outcome.result,
            'reason': outcome.reason,
            'started': outcome.started,
            'ended': outcome.ended,
            'tasks_done': outcome.tasks_done,
            'tasks_failed': outcome.tasks_failed,
            'faults': outcome.faults,
        })

    def task_finished(self, task: Task) -> None:
        if task.status != TASK_FAILED:
            return
        self._emitter.emit(CPE_TASK_FAILED, task.cpe_key, {
            'cpe_key': task.cpe_key,
            'imsi': imsi_of(task.cpe_key, {}),
            'task_id': task.task_id,
            'task_type': task.type,
            'attempts': task.attempts,
            'max_attempts': task.max_attempts,
            'fault_code': task.fault_code,
            'fault_string': task.fault_string,
        })
