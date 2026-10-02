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

import threading
import time
import uuid
from dataclasses import asdict, dataclass, field
from typing import Any, Callable, Dict, List, Optional

from magma.common.redis.containers import RedisHashDict
from magma.common.redis.serializers import (
    get_json_deserializer,
    get_json_serializer,
)

SESSION_TIMEOUT_SEC = 120
TASK_MAX_ATTEMPTS = 3
TASK_TTL_SEC = 7 * 24 * 3600
INFORM_WINDOW_SEC = 3600
# Done, failed and expired tasks kept per CPE so their outcome can be read.
FINISHED_TASKS_KEPT = 20

TASK_PENDING = 'pending'
TASK_IN_PROGRESS = 'in_progress'
TASK_DONE = 'done'
TASK_FAILED = 'failed'
TASK_EXPIRED = 'expired'
FINISHED = (TASK_DONE, TASK_FAILED, TASK_EXPIRED)
TASK_STATUSES = (TASK_PENDING, TASK_IN_PROGRESS) + FINISHED

# How a session ended: acsd ran the queue dry, the CPE went quiet past the
# session timeout, or something else cut it short (a new Inform, a restart).
SESSION_COMPLETED = 'completed'
SESSION_TIMED_OUT = 'timed_out'
SESSION_INTERRUPTED = 'interrupted'


class TaskNotFound(KeyError):
    pass


@dataclass
class Task:
    """An operation queued for a CPE, run in its next session."""
    task_id: str
    cpe_key: str
    type: str
    args: Dict[str, Any] = field(default_factory=dict)
    status: str = TASK_PENDING
    attempts: int = 0
    max_attempts: int = TASK_MAX_ATTEMPTS
    # The session running the task, or the one whose retryable fault
    # requeued it (so that session does not pick it up again).
    session_id: str = ''
    fault_code: int = 0
    fault_string: str = ''
    result: Dict[str, Any] = field(default_factory=dict)
    created: float = 0.0
    updated: float = 0.0
    # 0 means no deadline.
    deadline: float = 0.0


@dataclass
class Session:
    """
    An open CWMP session, keyed by the CPE's source IP since acsd tells CPEs
    apart by address (no cookies). session_key overrides that key for
    claimed CPEs, which may share a NAT address.
    """
    session_id: str
    cpe_key: str
    source_ip: str
    # Data model root from the Inform: 'Device.' or 'InternetGatewayDevice.'.
    root: str = 'Device.'
    # Name of the datamodel handler picked from the Inform DeviceId.
    model_handler: str = ''
    pending_task_id: str = ''
    pending_method: str = ''
    task_step: int = 0
    created: float = 0.0
    expires: float = 0.0
    session_key: str = ''
    # Identity mode of the listener the session runs on (config.MODE_*).
    mode: str = 'core'
    tasks_done: int = 0
    tasks_failed: int = 0
    faults: int = 0

    @property
    def key(self) -> str:
        return self.session_key or self.source_ip


@dataclass
class ParameterSnapshot:
    cpe_key: str
    values: Dict[str, str] = field(default_factory=dict)
    updated: float = 0.0


@dataclass
class CpeModel:
    """The normalized model of a CPE (datamodel.Model.to_dict())."""
    cpe_key: str
    handler: str = ''
    model: Dict[str, Any] = field(default_factory=dict)
    updated: float = 0.0


@dataclass
class InformCount:
    # Informs in the current fixed window, and when that window ends.
    count: int
    window_end: float
    total: int
    last_inform: float


@dataclass
class SessionOutcome:
    """How the last CWMP session of a CPE ended."""
    cpe_key: str
    session_id: str
    result: str
    reason: str = ''
    started: float = 0.0
    ended: float = 0.0
    tasks_done: int = 0
    tasks_failed: int = 0
    faults: int = 0


class StoreListener:
    """
    Told of session ends and of tasks reaching a final status, whichever
    path got them there. Called under the store lock: it must not block.
    """

    def session_ended(self, outcome: SessionOutcome) -> None:
        pass

    def task_finished(self, task: 'Task') -> None:
        pass


@dataclass
class ReapResult:
    sessions: int = 0
    requeued_tasks: int = 0
    expired_tasks: int = 0


class AcsStore:
    """
    acsd state in Redis: open sessions, the per-CPE task queue, the last
    parameter snapshot, the normalized model and inform counters.

    Every per-CPE record is keyed by an opaque `cpe_key`: the IMSI
    ("IMSI<digits>") for CPEs on the Magma core, a claim key for claimed
    CPEs outside it. The store never parses it.

    Task semantics follow the Go ACSStorage of the cloud ACS (claim,
    retry, fail, timeout).

    acsd is the only writer of these keys, so compound updates are made
    atomic with a process lock rather than Redis transactions; every single
    write still lands in Redis, so a restarted acsd picks up where it was.
    """

    def __init__(
        self,
        client,
        prefix: str = 'acsd',
        clock: Callable[[], float] = time.time,
        session_timeout_sec: float = SESSION_TIMEOUT_SEC,
        listener: Optional[StoreListener] = None,
    ):
        self._clock = clock
        self._listener = listener or StoreListener()
        self._lock = threading.RLock()
        self.session_timeout_sec = session_timeout_sec

        def hash_dict(name):
            return RedisHashDict(
                client, '%s:%s' % (prefix, name),
                get_json_serializer(), get_json_deserializer(),
            )
        self._sessions = hash_dict('sessions')
        self._tasks = hash_dict('tasks')
        # cpe_key -> task IDs in creation order, which is the run order.
        self._queues = hash_dict('queues')
        self._params = hash_dict('params')
        self._models = hash_dict('models')
        self._informs = hash_dict('informs')
        self._outcomes = hash_dict('outcomes')

    # Sessions

    def put_session(self, session: Session) -> None:
        """Store a session, pushing its expiry one timeout from now."""
        session.expires = self._clock() + self.session_timeout_sec
        self._sessions[session.key] = asdict(session)

    def get_session(self, key: str) -> Optional[Session]:
        """The unexpired session under `key` (its source IP), or None."""
        raw = self._sessions.get(key)
        if raw is None:
            return None
        session = Session(**raw)
        if session.expires <= self._clock():
            return None
        return session

    def list_sessions(self) -> List[Session]:
        return [Session(**raw) for raw in self._sessions.values()]

    def end_session(
        self, key: str, reason: str, result: str = SESSION_INTERRUPTED,
    ) -> int:
        """
        Close the session under `key` and requeue the task it was running.
        Returns the number of tasks requeued or failed.
        """
        with self._lock:
            raw = self._sessions.get(key)
            if raw is None:
                return 0
            return self.close_session(Session(**raw), result, reason)

    def close_session(self, session: Session, result: str, reason: str) -> int:
        """
        end_session for a session the caller holds: its copy wins over the
        stored one, which lags the counters of the exchange in flight.
        """
        with self._lock:
            self._sessions.pop(session.key, None)
            released = self._release_tasks(
                session.cpe_key, {session.session_id}, reason,
            )
            self._record_outcome(session, result, reason)
            return released

    def end_cpe_sessions(self, cpe_key: str, reason: str) -> int:
        """Close every session of a CPE, e.g. when it starts a new one."""
        with self._lock:
            keys = [s.key for s in self.list_sessions() if s.cpe_key == cpe_key]
            return sum(self.end_session(k, reason) for k in keys)

    def end_all_sessions(self, reason: str) -> int:
        """Close every session; acsd does this on start, since a restarted
        process cannot continue the HTTP exchanges of the previous one."""
        with self._lock:
            keys = [s.key for s in self.list_sessions()]
            ended = sum(self.end_session(k, reason) for k in keys)
            return ended + self._release_orphans(reason)

    def reap_expired(self) -> ReapResult:
        """
        End expired sessions, requeue tasks left in progress by a session
        that no longer exists and expire pending tasks past their deadline.
        """
        with self._lock:
            res = ReapResult()
            now = self._clock()
            for session in self.list_sessions():
                if session.expires <= now:
                    self._sessions.pop(session.key, None)
                    self._record_outcome(
                        session, SESSION_TIMED_OUT, 'session timed out',
                    )
                    res.sessions += 1
            res.requeued_tasks = self._release_orphans('session timed out')
            for task in self._all_tasks():
                if task.status == TASK_PENDING and _past(task.deadline, now):
                    task.status, task.updated = TASK_EXPIRED, now
                    self._save(task)
                    self._listener.task_finished(task)
                    res.expired_tasks += 1
            return res

    # Tasks

    def create_task(
        self,
        cpe_key: str,
        task_type: str,
        args: Optional[Dict[str, Any]] = None,
        max_attempts: int = TASK_MAX_ATTEMPTS,
        ttl_sec: float = TASK_TTL_SEC,
    ) -> Task:
        """Queue a task behind the other tasks of the CPE. ttl_sec <= 0
        means the task never expires."""
        with self._lock:
            now = self._clock()
            task = Task(
                task_id=uuid.uuid4().hex,
                cpe_key=cpe_key,
                type=task_type,
                args=dict(args or {}),
                max_attempts=max(1, max_attempts),
                created=now,
                updated=now,
                deadline=now + ttl_sec if ttl_sec > 0 else 0.0,
            )
            self._save(task)
            queue = self._queues.get(cpe_key, [])
            queue.append(task.task_id)
            self._queues[cpe_key] = self._prune(queue)
            return task

    def get_task(self, task_id: str) -> Optional[Task]:
        raw = self._tasks.get(task_id)
        return Task(**raw) if raw is not None else None

    def list_tasks(self, cpe_key: str) -> List[Task]:
        """The tasks of a CPE in run order."""
        tasks = (self.get_task(t) for t in self._queues.get(cpe_key, []))
        return [t for t in tasks if t is not None]

    def pending_count(self, cpe_key: str) -> int:
        return sum(
            t.status in (TASK_PENDING, TASK_IN_PROGRESS)
            for t in self.list_tasks(cpe_key)
        )

    def claim_next_task(self, cpe_key: str, session_id: str) -> Optional[Task]:
        """
        Mark the oldest runnable task of the CPE in progress in the session
        and return it, or None. A task requeued by a fault in this session
        is left for the next one.
        """
        with self._lock:
            now = self._clock()
            for task in self.list_tasks(cpe_key):
                if task.status != TASK_PENDING or task.session_id == session_id:
                    continue
                if _past(task.deadline, now):
                    continue
                task.status, task.session_id = TASK_IN_PROGRESS, session_id
                task.attempts += 1
                task.updated = now
                self._save(task)
                return task
            return None

    def save_task_result(self, task_id: str, result: Dict[str, Any]) -> None:
        """Keep the partial result of a task that takes several RPCs."""
        with self._lock:
            task = self._must_get(task_id)
            task.result, task.updated = result, self._clock()
            self._save(task)

    def complete_task(self, task_id: str, result: Dict[str, Any]) -> Task:
        with self._lock:
            task = self._must_get(task_id)
            task.status, task.result, task.session_id = TASK_DONE, result, ''
            task.fault_code, task.fault_string = 0, ''
            task.updated = self._clock()
            self._save(task)
            self._listener.task_finished(task)
            return task

    def fail_task(
        self,
        task_id: str,
        fault_code: int,
        fault_string: str,
        retryable: bool,
    ) -> Task:
        """
        Record a fault. A retryable fault requeues the task while it has
        attempts left and is before its deadline; otherwise it fails.
        """
        with self._lock:
            task = self._must_get(task_id)
            now = self._clock()
            if (
                retryable and task.attempts < task.max_attempts
                and not _past(task.deadline, now)
            ):
                # The session ID stays so this session does not retry it.
                task.status = TASK_PENDING
            else:
                task.status, task.session_id = TASK_FAILED, ''
            task.fault_code, task.fault_string = fault_code, fault_string
            task.updated = now
            self._save(task)
            if task.status == TASK_FAILED:
                self._listener.task_finished(task)
            return task

    # Parameters and informs

    def merge_parameters(
        self, cpe_key: str, values: Dict[str, str],
    ) -> ParameterSnapshot:
        """Merge values the CPE reported onto its last snapshot."""
        with self._lock:
            snapshot = self.get_parameters(cpe_key)
            snapshot.values.update(values)
            snapshot.updated = self._clock()
            self._params[cpe_key] = asdict(snapshot)
            return snapshot

    def get_parameters(self, cpe_key: str) -> ParameterSnapshot:
        raw = self._params.get(cpe_key)
        return ParameterSnapshot(**raw) if raw else ParameterSnapshot(cpe_key)

    def put_model(
        self, cpe_key: str, handler: str, model: Dict[str, Any],
    ) -> CpeModel:
        cpe = CpeModel(cpe_key, handler, model, self._clock())
        self._models[cpe_key] = asdict(cpe)
        return cpe

    def get_model(self, cpe_key: str) -> Optional[CpeModel]:
        raw = self._models.get(cpe_key)
        return CpeModel(**raw) if raw else None

    def count_inform(
        self, cpe_key: str, window_sec: float = INFORM_WINDOW_SEC,
    ) -> InformCount:
        """Count an Inform in fixed windows of window_sec."""
        with self._lock:
            now = self._clock()
            raw = self._informs.get(cpe_key)
            counts = InformCount(**raw) if raw else InformCount(0, 0.0, 0, 0.0)
            if counts.window_end <= now:
                counts.count, counts.window_end = 0, now + window_sec
            counts.count += 1
            counts.total += 1
            counts.last_inform = now
            self._informs[cpe_key] = asdict(counts)
            return counts

    def get_inform_count(self, cpe_key: str) -> Optional[InformCount]:
        raw = self._informs.get(cpe_key)
        return InformCount(**raw) if raw else None

    # CPE views

    def list_cpe_keys(self) -> List[str]:
        """Every CPE that has sent an Inform."""
        return sorted(self._informs.keys())

    def get_last_session(self, cpe_key: str) -> Optional[SessionOutcome]:
        raw = self._outcomes.get(cpe_key)
        return SessionOutcome(**raw) if raw else None

    def task_counts(self) -> Dict[str, int]:
        """Stored tasks of every CPE by status."""
        counts = dict.fromkeys(TASK_STATUSES, 0)
        for task in self._all_tasks():
            counts[task.status] = counts.get(task.status, 0) + 1
        return counts

    # Internals

    def _record_outcome(self, session: Session, result: str, reason: str) -> None:
        outcome = SessionOutcome(
            cpe_key=session.cpe_key,
            session_id=session.session_id,
            result=result,
            reason=reason,
            started=session.created,
            ended=self._clock(),
            tasks_done=session.tasks_done,
            tasks_failed=session.tasks_failed,
            faults=session.faults,
        )
        self._outcomes[session.cpe_key] = asdict(outcome)
        self._listener.session_ended(outcome)

    def _save(self, task: Task) -> None:
        self._tasks[task.task_id] = asdict(task)

    def _must_get(self, task_id: str) -> Task:
        task = self.get_task(task_id)
        if task is None:
            raise TaskNotFound(task_id)
        return task

    def _all_tasks(self) -> List[Task]:
        return [Task(**raw) for raw in self._tasks.values()]

    def _release_tasks(self, cpe_key: str, session_ids, reason: str) -> int:
        """Treat tasks left in progress by the sessions as a retryable
        fault, as the Go storage does for orphaned tasks."""
        released = 0
        for task in self.list_tasks(cpe_key):
            if task.status == TASK_IN_PROGRESS and task.session_id in session_ids:
                self._requeue_orphan(task, reason)
                released += 1
        return released

    def _release_orphans(self, reason: str) -> int:
        live = {s.session_id for s in self.list_sessions()}
        released = 0
        for task in self._all_tasks():
            if task.status == TASK_IN_PROGRESS and task.session_id not in live:
                self._requeue_orphan(task, reason)
                released += 1
        return released

    def _requeue_orphan(self, task: Task, reason: str) -> None:
        now = self._clock()
        if task.attempts < task.max_attempts and not _past(task.deadline, now):
            task.status = TASK_PENDING
        else:
            task.status = TASK_FAILED
        task.session_id, task.fault_code, task.fault_string = '', 0, reason
        task.updated = now
        self._save(task)
        if task.status == TASK_FAILED:
            self._listener.task_finished(task)

    def _prune(self, queue: List[str]) -> List[str]:
        """Drop the oldest finished tasks beyond FINISHED_TASKS_KEPT."""
        finished = [
            tid for tid in queue
            if (self.get_task(tid) or Task('', '', '', status=TASK_DONE)).status
            in FINISHED
        ]
        drop = set(finished[:max(0, len(finished) - FINISHED_TASKS_KEPT)])
        for tid in drop:
            self._tasks.pop(tid, None)
        return [tid for tid in queue if tid not in drop]


def _past(deadline: float, now: float) -> bool:
    return deadline > 0 and deadline <= now
