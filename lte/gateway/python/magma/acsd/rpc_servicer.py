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
CpeManager gRPC service of acsd: queue tasks and read CPE state, for
Orc8r over SyncRPC (the ctraced pattern) and for local tooling.
"""

import logging
from typing import Optional

import grpc
from lte.protos import cpe_acs_pb2 as pb
from lte.protos.cpe_acs_pb2_grpc import (
    CpeManagerServicer,
    add_CpeManagerServicer_to_server,
)
from magma.acsd import tasks
from magma.acsd.connreq import ConnectionRequester
from magma.acsd.cpe_state import (
    MODE_CLAIMED,
    MODE_CORE,
    CpeView,
    CpeViews,
)
from magma.acsd.reach import CONNECTION_REQUEST, NEXT_INFORM
from magma.acsd.store import (
    TASK_MAX_ATTEMPTS,
    TASK_PENDING,
    TASK_TTL_SEC,
    AcsStore,
    Task,
)

_MODES = {MODE_CORE: pb.CPE_MODE_CORE, MODE_CLAIMED: pb.CPE_MODE_CLAIMED}
_REACH = {
    CONNECTION_REQUEST: pb.CPE_REACH_CONNECTION_REQUEST,
    NEXT_INFORM: pb.CPE_REACH_NEXT_INFORM,
}
_TYPE_PREFIX = 'CPE_TASK_TYPE_'
_STATUS_PREFIX = 'CPE_TASK_STATUS_'
CONNECTION_REQUEST_UNIMPLEMENTED = (
    'this acsd sends no Connection Requests; queued tasks run at the '
    "CPE's next periodic Inform"
)
FROZEN = 'acsd is frozen (entitlement expired): it queues no tasks'


class CpeManagerRpcServicer(CpeManagerServicer):
    """
    Serves CpeManager from the store the CWMP handler uses: the process
    lock that makes the store's compound updates atomic is per instance,
    so a second AcsStore would race the handler.
    """

    def __init__(
        self,
        store: AcsStore,
        views: CpeViews,
        frozen: bool = False,
        requester: Optional[ConnectionRequester] = None,
    ):
        """`requester` sends Connection Requests; without it there are none."""
        self._store = store
        self._views = views
        self._requester = requester
        # A frozen acsd (expired entitlement) queues nothing for its CPEs.
        self._frozen = frozen

    def add_to_server(self, server) -> None:
        add_CpeManagerServicer_to_server(self, server)

    def EnqueueTask(self, request: pb.EnqueueTaskRequest, context) -> pb.CpeTask:
        if not request.cpe_key:
            return _error(context, grpc.StatusCode.INVALID_ARGUMENT, 'cpe_key is required', pb.CpeTask())
        if self._frozen:
            return _error(context, grpc.StatusCode.FAILED_PRECONDITION, FROZEN, pb.CpeTask())
        ttl_sec = request.ttl_sec or TASK_TTL_SEC
        try:
            task = tasks.enqueue_task(
                self._store,
                request.cpe_key,
                _task_type(request.type),
                parameter_names=list(request.parameter_names),
                parameter_values=[_parameter_value(p) for p in request.parameter_values],
                parameter_path=request.parameter_path,
                next_level=request.next_level,
                max_attempts=request.max_attempts or TASK_MAX_ATTEMPTS,
                ttl_sec=ttl_sec if ttl_sec > 0 else 0,
            )
        except tasks.InvalidTask as err:
            return _error(context, grpc.StatusCode.INVALID_ARGUMENT, str(err), pb.CpeTask())
        logging.info('Queued task %s (%s) for %s', task.task_id, task.type, task.cpe_key)
        if self._requester is not None:
            self._requester.request_soon(task.cpe_key)
        return task_to_proto(task, self._views.get(task.cpe_key))

    def GetTask(self, request: pb.GetTaskRequest, context) -> pb.CpeTask:
        task = self._store.get_task(request.task_id) if request.task_id else None
        if task is None:
            return _error(
                context, grpc.StatusCode.NOT_FOUND,
                'no task %r' % request.task_id, pb.CpeTask(),
            )
        return task_to_proto(task, self._views.get(task.cpe_key))

    def ListCpes(self, request: pb.ListCpesRequest, context) -> pb.ListCpesResponse:
        return pb.ListCpesResponse(cpes=[cpe_to_proto(v) for v in self._views.list()])

    def GetCpe(self, request: pb.GetCpeRequest, context) -> pb.Cpe:
        view = self._views.get(request.cpe_key) if request.cpe_key else None
        if view is None:
            return _error(
                context, grpc.StatusCode.NOT_FOUND,
                'no CPE %r' % request.cpe_key, pb.Cpe(),
            )
        cpe = cpe_to_proto(view)
        cpe.model.update(view.model)
        cpe.tasks.extend(
            task_to_proto(t, view) for t in self._store.list_tasks(view.cpe_key)
        )
        if request.include_parameters:
            cpe.parameters.update(self._store.get_parameters(view.cpe_key).values)
        return cpe

    def ConnectionRequest(
        self, request: pb.ConnectionRequestRequest, context,
    ) -> pb.ConnectionRequestResponse:
        empty = pb.ConnectionRequestResponse()
        if not request.cpe_key:
            return _error(context, grpc.StatusCode.INVALID_ARGUMENT, 'cpe_key is required', empty)
        if self._requester is None:
            return _error(
                context, grpc.StatusCode.UNIMPLEMENTED, CONNECTION_REQUEST_UNIMPLEMENTED, empty,
            )
        if self._frozen:
            return _error(context, grpc.StatusCode.FAILED_PRECONDITION, FROZEN, empty)
        view = self._views.get(request.cpe_key)
        if view is None:
            return _error(
                context, grpc.StatusCode.NOT_FOUND, 'no CPE %r' % request.cpe_key, empty,
            )
        outcome = self._requester.request(request.cpe_key)
        return pb.ConnectionRequestResponse(
            sent=outcome.sent,
            reach=_REACH.get(outcome.reach.how, pb.CPE_REACH_UNSPECIFIED),
            reason=outcome.reach.reason,
            next_inform=view.next_inform,
            detail=outcome.detail,
        )


def task_to_proto(task: Task, view: Optional[CpeView] = None) -> pb.CpeTask:
    """
    The task with what its CPE's view says: the IMSI, and for a pending
    task how and when it will run. No view before the CPE's first Inform.
    """
    out = pb.CpeTask(
        task_id=task.task_id,
        cpe_key=task.cpe_key,
        imsi=view.imsi if view else '',
        type=_enum(pb.CpeTaskType, _TYPE_PREFIX, task.type),
        status=_enum(pb.CpeTaskStatus, _STATUS_PREFIX, task.status),
        attempts=task.attempts,
        max_attempts=task.max_attempts,
        fault_code=task.fault_code,
        fault_string=task.fault_string,
        created=task.created,
        updated=task.updated,
        deadline=task.deadline,
    )
    out.args.update(task.args)
    out.result.update(task.result)
    if view is not None and task.status == TASK_PENDING:
        out.reach = _REACH.get(view.reach, pb.CPE_REACH_UNSPECIFIED)
        out.next_inform = view.next_inform
    return out


def cpe_to_proto(view: CpeView) -> pb.Cpe:
    """The ListCpes entry of a CPE: no model, tasks or parameters."""
    cpe = pb.Cpe(
        cpe_key=view.cpe_key,
        mode=_MODES.get(view.mode, pb.CPE_MODE_UNSPECIFIED),
        imsi=view.imsi,
        serial_number=view.serial_number,
        oui=view.oui,
        product_class=view.product_class,
        software_version=view.software_version,
        handler=view.handler,
        last_inform=view.last_inform,
        informs_total=view.informs_total,
        online=view.online,
        pending_tasks=view.pending_tasks,
        reach=_REACH.get(view.reach, pb.CPE_REACH_UNSPECIFIED),
        reach_reason=view.reach_reason,
        next_inform=view.next_inform,
    )
    if view.last_session:
        session = dict(view.last_session)
        # The CPE carries these itself; the source IP is not part of the API.
        session.pop('cpe_key', None)
        session.pop('mode', None)
        session.pop('source_ip', None)
        cpe.last_session.CopyFrom(pb.CpeSession(**session))
    return cpe


def _task_type(value: int) -> str:
    if value == pb.CPE_TASK_TYPE_UNSPECIFIED or value not in pb.CpeTaskType.values():
        raise tasks.InvalidTask('task type is required')
    return pb.CpeTaskType.Name(value)[len(_TYPE_PREFIX):].lower()


def _enum(enum, prefix: str, name: str) -> int:
    try:
        return enum.Value(prefix + name.upper())
    except ValueError:
        return 0


def _parameter_value(p: pb.CpeParameterValue):
    value = {'name': p.name, 'value': p.value}
    if p.type:
        value['type'] = p.type
    return value


def _error(context, code: grpc.StatusCode, details: str, empty):
    context.set_code(code)
    context.set_details(details)
    return empty
