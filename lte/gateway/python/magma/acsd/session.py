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

import logging
import threading
import time
import uuid
from typing import Any, Callable, Dict, List, Optional

from magma.acsd import tasks
from magma.acsd.store import TASK_IN_PROGRESS, AcsStore, Session, Task
from magma.tr069 import models
from spyne.model.complex import ComplexModelBase
from spyne.server.wsgi import WsgiMethodContext

# Names the CPE behind `source_ip`: returns the key the session is held
# under (the IMSI, in acsd), or None to refuse the session
# (HTTP 403). Called once per session, on its Inform.
IdentifyFn = Callable[[str, models.Inform], Optional[str]]

HTTP_403 = '403 Forbidden'
REAP_INTERVAL_SEC = 30.0


def accept_all(source_ip: str, inform: models.Inform) -> Optional[str]:
    """Default identity check: every CPE is accepted, keyed by its IP."""
    return source_ip


def source_ip_of(ctx: WsgiMethodContext) -> str:
    return ctx.transport.req_env.get('REMOTE_ADDR', '')


class CwmpSessionHandler:
    """
    CWMP session flow for acsd: answer the Inform, then, from the CPE's
    empty POST on, send the tasks queued for its IMSI one RPC per HTTP
    exchange, record each answer or Fault on the task, and end the session
    (204) once the queue is empty.

    Session state lives in the store, keyed by source IP; that is sound
    because the identity is derived from the source IP, so a later message
    from the same IP belongs to the same CPE. Keeping it next to the task
    claims means a session that times out, or dies with acsd, requeues its
    task. Thread-safe, so the listener's worker threads can share one
    instance.
    """

    def __init__(
        self,
        identify: IdentifyFn = accept_all,
        *,
        store: AcsStore,
        refresh_paths: tasks.RefreshPathsFn = tasks.default_refresh_paths,
        clock: Callable[[], float] = time.monotonic,
    ):
        self._identify = identify
        self._store = store
        self._refresh_paths = refresh_paths
        self._clock = clock
        self._reap_lock = threading.Lock()
        self._next_reap = 0.0

    @property
    def store(self) -> AcsStore:
        return self._store

    def session_identity(self, source_ip: str) -> Optional[str]:
        """The identity of the session open from source_ip, if any."""
        session = self._store.get_session(source_ip)
        return session.imsi if session else None

    def handle_tr069_message(
        self,
        ctx: WsgiMethodContext,
        tr069_message: ComplexModelBase,
    ) -> ComplexModelBase:
        source_ip = source_ip_of(ctx)
        if isinstance(tr069_message, models.Inform):
            return self._handle_inform(ctx, source_ip, tr069_message)
        session = self._store.get_session(source_ip)
        if session is None:
            if not isinstance(tr069_message, models.DummyInput):
                logging.info(
                    'CPE %s sent %s outside a session; ending it',
                    source_ip, type(tr069_message).__name__,
                )
            return models.DummyInput()
        if session.pending_method:
            self._handle_answer(session, tr069_message)
        elif not isinstance(tr069_message, models.DummyInput):
            logging.info(
                'CPE %s sent %s outside a task',
                source_ip, type(tr069_message).__name__,
            )
        return self._next(session)

    def _handle_inform(
        self,
        ctx: WsgiMethodContext,
        source_ip: str,
        inform: models.Inform,
    ) -> ComplexModelBase:
        serial = _serial_of(inform)
        identity = self._identify(source_ip, inform)
        if not identity:
            self._store.end_session(source_ip, 'session refused')
            logging.warning(
                'Refusing CWMP session from %s (serial %s)', source_ip, serial,
            )
            ctx.transport.resp_code = HTTP_403
            return models.DummyInput()
        self._maybe_reap()
        # A new Inform means the CPE gave up on any session it had open.
        self._store.end_session(source_ip, 'CPE started a new session')
        self._store.end_imsi_sessions(identity, 'CPE started a new session')
        values = _inform_values(inform)
        self._store.put_session(
            Session(
                session_id=uuid.uuid4().hex,
                imsi=identity,
                source_ip=source_ip,
                root=_root_of(values),
                created=time.time(),
            ),
        )
        self._store.count_inform(identity)
        if values:
            self._store.merge_parameters(identity, values)
        logging.info(
            'Inform from %s (serial %s, identity %s, %d tasks pending)',
            source_ip, serial, identity, self._store.pending_count(identity),
        )
        return models.InformResponse(MaxEnvelopes=1)

    def _handle_answer(self, session: Session, message: ComplexModelBase) -> None:
        method = session.pending_method
        session.pending_method = ''
        task = self._current_task(session)
        if task is None:
            return
        plan = self._plan(task, session)
        fault = None
        if isinstance(message, models.DummyInput):
            fault = (
                tasks.FAULT_INTERNAL_ERROR,
                'CPE sent an empty POST instead of answering %s' % method,
                True,
            )
        elif isinstance(message, models.Fault):
            code = int(message.FaultCode or 0)
            fault = (code, tasks.fault_text(message), tasks.retryable(code))
        elif type(message).__name__ != method + 'Response':
            fault = (
                tasks.FAULT_INTERNAL_ERROR,
                'CPE answered %s with %s' % (method, type(message).__name__),
                True,
            )

        result: Dict[str, Any] = dict(task.result)
        if fault and tasks.tolerated(task.type, fault[0]):
            result.setdefault('faults', []).append(
                {'request': method, 'fault_code': fault[0], 'fault_string': fault[1]},
            )
        elif fault:
            self._fail(session, task, *fault)
            return
        else:
            try:
                tasks.apply(result, message)
            except ValueError as err:
                self._fail(session, task, tasks.FAULT_INTERNAL_ERROR, str(err), True)
                return

        session.task_step += 1
        if session.task_step < len(plan):
            self._store.save_task_result(task.task_id, result)
            return
        self._clear_task(session)
        self._finish(session, task, result)

    def _next(self, session: Session) -> ComplexModelBase:
        """Send the next request of the session, or end it."""
        request = self._next_request(session)
        if request is None:
            self._store.end_session(session.source_ip, 'session ended')
            logging.info('Session of %s done', session.imsi)
            return models.DummyInput()
        session.pending_method = type(request).__name__
        # Stored before the request goes out, so the answer finds it.
        self._store.put_session(session)
        return request

    def _next_request(self, session: Session) -> Optional[ComplexModelBase]:
        task = self._current_task(session)
        if task is not None:
            plan = self._plan(task, session)
            if session.task_step < len(plan):
                return plan[session.task_step]
            self._clear_task(session)
        while True:
            task = self._store.claim_next_task(session.imsi, session.session_id)
            if task is None:
                return None
            try:
                plan = self._plan(task, session)
            except (tasks.InvalidTask, KeyError) as err:
                self._store.fail_task(
                    task.task_id, tasks.FAULT_INVALID_ARGUMENTS, str(err), False,
                )
                continue
            if not plan:
                self._finish(session, task, {})
                continue
            logging.info(
                'Running task %s (%s) on %s, attempt %d',
                task.task_id, task.type, session.imsi, task.attempts,
            )
            session.pending_task_id, session.task_step = task.task_id, 0
            return plan[0]

    def _current_task(self, session: Session) -> Optional[Task]:
        """The task the session is running, unless it was requeued or
        dropped meanwhile."""
        if not session.pending_task_id:
            return None
        task = self._store.get_task(session.pending_task_id)
        if (
            task is None or task.status != TASK_IN_PROGRESS
            or task.session_id != session.session_id
        ):
            self._clear_task(session)
            return None
        return task

    def _plan(self, task: Task, session: Session) -> List[ComplexModelBase]:
        return tasks.plan(task, session.root, self._refresh_paths)

    def _finish(self, session: Session, task: Task, result: Dict[str, Any]) -> None:
        if result.get('values'):
            self._store.merge_parameters(session.imsi, result['values'])
        self._store.complete_task(task.task_id, result)
        logging.info('Task %s (%s) on %s done', task.task_id, task.type, session.imsi)

    def _fail(
        self, session: Session, task: Task, code: int, text: str, retry: bool,
    ) -> None:
        self._clear_task(session)
        failed = self._store.fail_task(task.task_id, code, text, retry)
        logging.warning(
            'Task %s (%s) on %s fault %d: %s; now %s',
            task.task_id, task.type, session.imsi, code, text, failed.status,
        )

    @staticmethod
    def _clear_task(session: Session) -> None:
        session.pending_task_id, session.task_step = '', 0

    def _maybe_reap(self) -> None:
        with self._reap_lock:
            now = self._clock()
            if now < self._next_reap:
                return
            self._next_reap = now + REAP_INTERVAL_SEC
        res = self._store.reap_expired()
        if res.sessions or res.requeued_tasks or res.expired_tasks:
            logging.info(
                'Reaped %d sessions; %d tasks requeued, %d expired',
                res.sessions, res.requeued_tasks, res.expired_tasks,
            )


def _serial_of(inform: models.Inform) -> str:
    device_id = getattr(inform, 'DeviceId', None)
    return getattr(device_id, 'SerialNumber', None) or '?'


def _inform_values(inform: models.Inform) -> Dict[str, str]:
    values = {}
    for p in tasks.list_items(getattr(inform, 'ParameterList', None), 'ParameterValueStruct'):
        if p.Name:
            data = getattr(p.Value, 'Data', None)
            values[p.Name] = '' if data is None else str(data)
    return values


def _root_of(values: Dict[str, str]) -> str:
    if any(name.startswith('InternetGatewayDevice.') for name in values):
        return 'InternetGatewayDevice.'
    return 'Device.'
