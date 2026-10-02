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

import ipaddress
import logging
import threading
import time
import uuid
from typing import Any, Callable, Dict, List, Optional

from magma.acsd import tasks
from magma.acsd.claimed import ROTATE_CREDENTIALS
from magma.acsd.config import LISTENER_MODE, MODE_CLAIMED, MODE_CORE
from magma.acsd.datamodel import (
    DEFAULT_REGISTRY,
    ROOT_TR181,
    Field,
    Handler,
    Model,
    Registry,
    detect_root,
    device_id_identity,
    device_info,
    fill_identity,
)
from magma.acsd.digest import DIGEST_USERNAME
from magma.acsd.store import (
    SESSION_COMPLETED,
    TASK_FAILED,
    TASK_IN_PROGRESS,
    AcsStore,
    Session,
    Task,
)
from magma.tr069 import models
from spyne.model.complex import ComplexModelBase
from spyne.server.wsgi import WsgiMethodContext

# Names the CPE behind `source_ip`: returns the key the session is held
# under (the cpe_key: the IMSI in core mode), or None to refuse the session
# (HTTP 403). Called once per session, on its Inform.
IdentifyFn = Callable[[str, models.Inform], Optional[str]]

HTTP_403 = '403 Forbidden'
REAP_INTERVAL_SEC = 30.0


def accept_all(source_ip: str, inform: models.Inform) -> Optional[str]:
    """Default identity check: every CPE is accepted, keyed by its IP."""
    return source_ip


def source_ip_of(ctx: WsgiMethodContext) -> str:
    return ctx.transport.req_env.get('REMOTE_ADDR', '')


def session_key_of(env: dict) -> str:
    """
    Core sessions are keyed by source IP: mtr0 addresses are unique per UE.
    Claimed CPEs may share a carrier NAT address, so their session is the
    TCP connection; a CPE that reconnects mid-session has its task retried
    in the next session.
    """
    source_ip = env.get('REMOTE_ADDR', '')
    if env.get(LISTENER_MODE, MODE_CORE) != MODE_CLAIMED:
        return source_ip
    return 'claimed/%s/%s' % (source_ip, env.get('REMOTE_PORT', ''))


class CwmpSessionHandler:
    """
    CWMP session flow for acsd: answer the Inform, then, from the CPE's
    empty POST on, send the tasks queued for its cpe_key one RPC per HTTP
    exchange, record each answer or Fault on the task, and end the session
    (204) once the queue is empty.

    Session state lives in the store, keyed by source IP on the core
    listener; that is sound because the identity is derived from the source
    IP, so a later message from the same IP belongs to the same CPE. On the
    claimed listener it is keyed by TCP connection (session_key_of). Keeping it next to the task
    claims means a session that times out, or dies with acsd, requeues its
    task. Thread-safe, so the listener's worker threads can share one
    instance.
    """

    def __init__(
        self,
        identify: IdentifyFn = accept_all,
        *,
        store: AcsStore,
        registry: Registry = DEFAULT_REGISTRY,
        clock: Callable[[], float] = time.monotonic,
        claimed=None,
    ):
        """
        `claimed` (a claimed.ClaimedMode) serves the requests of a claimed
        listener; without it they are refused.
        """
        self._identify = identify
        self._claimed = claimed
        self._store = store
        self._registry = registry
        self._clock = clock
        self._reap_lock = threading.Lock()
        self._next_reap = 0.0

    @property
    def store(self) -> AcsStore:
        return self._store

    def session_identity(self, key: str) -> Optional[str]:
        """The identity of the session open under `key`, if any."""
        session = self._store.get_session(key)
        return session.cpe_key if session else None

    def handle_tr069_message(
        self,
        ctx: WsgiMethodContext,
        tr069_message: ComplexModelBase,
    ) -> ComplexModelBase:
        source_ip = source_ip_of(ctx)
        if isinstance(tr069_message, models.Inform):
            return self._handle_inform(ctx, source_ip, tr069_message)
        session = self._store.get_session(session_key_of(ctx.transport.req_env))
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
        env = ctx.transport.req_env
        key = session_key_of(env)
        mode = env.get(LISTENER_MODE, MODE_CORE)
        username = env.get(DIGEST_USERNAME)
        if mode == MODE_CLAIMED:
            identity = (
                self._claimed.identify(source_ip, inform, username)
                if self._claimed else None
            )
        else:
            identity = self._identify(source_ip, inform)
        if not identity:
            self._store.end_session(key, 'session refused')
            logging.warning(
                'Refusing CWMP session from %s (serial %s)', source_ip, serial,
            )
            ctx.transport.resp_code = HTTP_403
            return models.DummyInput()
        self._maybe_reap()
        # A new Inform means the CPE gave up on any session it had open.
        self._store.end_session(key, 'CPE started a new session')
        self._store.end_cpe_sessions(identity, 'CPE started a new session')
        values = _inform_values(inform)
        handler = self._registry.select(device_info(inform, values))
        session = Session(
            session_id=uuid.uuid4().hex,
            cpe_key=identity,
            source_ip=source_ip,
            root=detect_root(values) or ROOT_TR181,
            model_handler=handler.name,
            created=time.time(),
            session_key=key if key != source_ip else '',
            mode=mode,
        )
        self._store.put_session(session)
        self._store.count_inform(identity)
        if values:
            self._store.merge_parameters(identity, values)
        self._update_model(session, device_id_identity(inform))
        if mode == MODE_CLAIMED:
            self._claimed.session_started(identity, username, handler)
        logging.info(
            'Inform from %s (serial %s, identity %s, handler %s, '
            '%d tasks pending)', source_ip, serial, identity, handler.name,
            self._store.pending_count(identity),
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
            session.faults += 1
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
            self._store.close_session(session, SESSION_COMPLETED, 'session ended')
            logging.info('Session of %s done', session.cpe_key)
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
            task = self._store.claim_next_task(session.cpe_key, session.session_id)
            if task is None:
                return None
            try:
                plan = self._plan(task, session)
            except (tasks.InvalidTask, KeyError) as err:
                self._store.fail_task(
                    task.task_id, tasks.FAULT_INVALID_ARGUMENTS, str(err), False,
                )
                session.tasks_failed += 1
                self._task_failed(session, task)
                continue
            if not plan:
                self._finish(session, task, {})
                continue
            logging.info(
                'Running task %s (%s) on %s, attempt %d',
                task.task_id, task.type, session.cpe_key, task.attempts,
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
        if session.mode == MODE_CLAIMED and task.type == ROTATE_CREDENTIALS:
            return self._claimed.plan(task, session.root)
        return tasks.plan(task, session.root, self._handler(session))

    def _handler(self, session: Session) -> Handler:
        return self._registry.get(session.model_handler)

    def _finish(self, session: Session, task: Task, result: Dict[str, Any]) -> None:
        if result.get('values'):
            self._store.merge_parameters(session.cpe_key, result['values'])
            self._update_model(session)
        self._store.complete_task(task.task_id, result)
        session.tasks_done += 1
        logging.info('Task %s (%s) on %s done', task.task_id, task.type, session.cpe_key)
        if session.mode == MODE_CLAIMED:
            self._claimed.task_finished(task)

    def _fail(
        self, session: Session, task: Task, code: int, text: str, retry: bool,
    ) -> None:
        self._clear_task(session)
        failed = self._store.fail_task(task.task_id, code, text, retry)
        if failed.status == TASK_FAILED:
            session.tasks_failed += 1
        logging.warning(
            'Task %s (%s) on %s fault %d: %s; now %s',
            task.task_id, task.type, session.cpe_key, code, text, failed.status,
        )
        self._task_failed(session, task)

    def _task_failed(self, session: Session, task: Task) -> None:
        if session.mode == MODE_CLAIMED:
            self._claimed.task_failed(task)

    def _update_model(
        self, session: Session, identity: Optional[Dict[str, str]] = None,
    ) -> None:
        """
        Rebuild the CPE's normalized model from its whole parameter
        snapshot. `identity` is what the Inform DeviceId says; without it the
        identity of the stored model fills the fields the snapshot lacks.
        """
        handler = self._handler(session)
        snapshot = self._store.get_parameters(session.cpe_key)
        try:
            model = handler.normalize(session.root, snapshot.values)
        except Exception:  # pylint: disable=broad-except
            # A broken vendor table must not cost the CPE its session.
            logging.exception(
                'Handler %s cannot normalize %s', handler.name, session.cpe_key,
            )
            return
        if identity is None:
            stored = self._store.get_model(session.cpe_key)
            identity = stored.model.get('identity', {}) if stored else {}
        fill_identity(model, identity)
        if session.mode != MODE_CLAIMED:
            # Behind a NAT the source IP is the carrier's, not the CPE's
            # WAN address; the handler-reported one stands.
            _set_wan_address(model, session.source_ip)
        self._store.put_model(session.cpe_key, handler.name, model.to_dict())

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


def _set_wan_address(model: Model, source_ip: str) -> None:
    """
    The CPE reaches acsd from its WAN address, so that is the one to show,
    not the handler's pick among the IP.Interface entries.
    """
    try:
        address = ipaddress.ip_address(source_ip)
    except ValueError:
        return
    if address.version == 6 and address.ipv4_mapped:
        address = address.ipv4_mapped
    if address.version == 4:
        model.set(Field.WAN_IPV4, str(address))
    else:
        model.set(Field.WAN_IPV6, str(address))
