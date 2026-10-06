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

import re
from typing import Any, Dict, List, Optional, Sequence

from magma.acsd.datamodel import GENERIC, Handler, gpv_batches
from magma.acsd.store import TASK_MAX_ATTEMPTS, TASK_TTL_SEC, AcsStore, Task
from magma.tr069 import models
from spyne.model.complex import ComplexModelBase

REBOOT = 'reboot'
FACTORY_RESET = 'factory_reset'
REFRESH = 'refresh'
GET_PARAMETER_VALUES = 'get_parameter_values'
SET_PARAMETER_VALUES = 'set_parameter_values'
GET_PARAMETER_NAMES = 'get_parameter_names'
TYPES = (
    REBOOT, FACTORY_RESET, REFRESH, GET_PARAMETER_VALUES,
    SET_PARAMETER_VALUES, GET_PARAMETER_NAMES,
)

# TR-069 fault codes acsd acts on.
FAULT_INTERNAL_ERROR = 9002
FAULT_INVALID_ARGUMENTS = 9003
FAULT_RESOURCES_EXCEEDED = 9004
FAULT_INVALID_PARAMETER_NAME = 9005


# Parameters only acsd itself sets (claimed.py, through internal tasks that
# skip validate()): an operator SPV changing one would point the CPE at
# another ACS or swap the credential acsd authenticates it with, locking
# the CPE out until someone reaches it by hand.
_ACSD_OWNED = re.compile(
    r'(^|\.)ManagementServer\.(URL|Username|Password|'
    r'ConnectionRequestUsername|ConnectionRequestPassword)$',
    re.IGNORECASE,
)


class InvalidTask(ValueError):
    pass


def validate(task_type: str, args: Dict[str, Any]) -> None:
    """Reject arguments that do not fit the task type (the Go rules)."""
    names = args.get('parameter_names') or []
    values = args.get('parameter_values') or []
    path = args.get('parameter_path') or ''
    if task_type in (REBOOT, FACTORY_RESET):
        if names or values or path:
            raise InvalidTask('%s takes no parameters' % task_type)
    elif task_type == REFRESH:
        if values or path:
            raise InvalidTask('refresh only takes parameter_names (subtrees)')
        _check_names(names)
    elif task_type == GET_PARAMETER_VALUES:
        if not names:
            raise InvalidTask('get_parameter_values needs parameter_names')
        _check_names(names)
    elif task_type == SET_PARAMETER_VALUES:
        if not values:
            raise InvalidTask('set_parameter_values needs parameter_values')
        for p in values:
            name, typ = p.get('name') or '', p.get('type') or ''
            if not name or name.endswith('.'):
                raise InvalidTask('%r is not a parameter name' % name)
            if typ and not typ.startswith('xsd:'):
                raise InvalidTask('type %r of %s is not an xsd type' % (typ, name))
            if _ACSD_OWNED.search(name):
                raise InvalidTask(
                    '%s is managed by acsd (ACS URL and credentials); use '
                    'acsd_cli.py rotate-credentials or reset-credentials' % name,
                )
    elif task_type == GET_PARAMETER_NAMES:
        if path and not path.endswith('.') and args.get('next_level'):
            raise InvalidTask("next_level needs a partial path ending in '.'")
    else:
        raise InvalidTask(
            'unknown task type %r; want one of %s' % (task_type, ', '.join(TYPES)),
        )


def enqueue_task(
    store: AcsStore,
    cpe_key: str,
    task_type: str,
    parameter_names: Optional[Sequence[str]] = None,
    parameter_values: Optional[Sequence[Dict[str, str]]] = None,
    parameter_path: str = '',
    next_level: bool = False,
    max_attempts: int = TASK_MAX_ATTEMPTS,
    ttl_sec: float = TASK_TTL_SEC,
) -> Task:
    """
    Queue a task for the CPE keyed `cpe_key`; it runs in the CPE's next CWMP
    session. parameter_values items are {'name', 'value', 'type'} with type
    an xsd type ('xsd:string' when left out). Raises InvalidTask.
    """
    args: Dict[str, Any] = {}
    if parameter_names:
        args['parameter_names'] = list(parameter_names)
    if parameter_values:
        args['parameter_values'] = [dict(p) for p in parameter_values]
    if parameter_path:
        args['parameter_path'] = parameter_path
    if next_level:
        args['next_level'] = True
    validate(task_type, args)
    return store.create_task(cpe_key, task_type, args, max_attempts, ttl_sec)


def plan(
    task: Task,
    root: str,
    handler: Handler = GENERIC,
) -> List[ComplexModelBase]:
    """
    The CWMP requests that run a task, one per HTTP exchange. `handler`
    names the subtrees a refresh reads and caps the names per
    GetParameterValues for its CPE model.
    """
    args = task.args
    if task.type == REBOOT:
        # The CPE echoes the command key in its 'M Reboot' event.
        return [models.Reboot(CommandKey=task.task_id[:32])]
    if task.type == FACTORY_RESET:
        return [models.FactoryReset()]
    if task.type == GET_PARAMETER_VALUES:
        return _gpvs(args['parameter_names'], handler)
    if task.type == REFRESH:
        paths = args.get('parameter_names') or handler.refresh_paths(root)
        return _gpvs(paths, handler)
    if task.type == SET_PARAMETER_VALUES:
        return [_spv(args['parameter_values'], task.task_id[:32])]
    if task.type == GET_PARAMETER_NAMES:
        return [
            models.GetParameterNames(
                ParameterPath=args.get('parameter_path') or root,
                NextLevel=bool(args.get('next_level')),
            ),
        ]
    raise InvalidTask('unknown task type %r' % task.type)


def apply(result: Dict[str, Any], response: ComplexModelBase) -> None:
    """Merge the CPE's answer to one request of the plan into result."""
    if isinstance(response, models.GetParameterValuesResponse):
        values = result.setdefault('values', {})
        for p in list_items(response.ParameterList, 'ParameterValueStruct'):
            data = getattr(p.Value, 'Data', None)
            values[p.Name] = '' if data is None else str(data)
    elif isinstance(response, models.GetParameterNamesResponse):
        names = result.setdefault('names', [])
        for p in list_items(response.ParameterList, 'ParameterInfoStruct'):
            names.append({'name': p.Name, 'writable': bool(p.Writable)})
    elif isinstance(response, models.SetParameterValuesResponse):
        # 0: applied; 1: applied once the CPE reboots or the session ends.
        result['status'] = int(response.Status or 0)
    elif not isinstance(
        response, (models.RebootResponse, models.FactoryResetResponse),
    ):
        raise ValueError('unexpected %s' % type(response).__name__)


def retryable(fault_code: int) -> bool:
    """Faults worth trying again in a later session."""
    return fault_code in (FAULT_INTERNAL_ERROR, FAULT_RESOURCES_EXCEEDED)


def tolerated(task_type: str, fault_code: int) -> bool:
    """A refresh skips the subtrees the CPE does not have."""
    return task_type == REFRESH and fault_code == FAULT_INVALID_PARAMETER_NAME


def fault_text(fault: models.Fault) -> str:
    text = fault.FaultString or ''
    details = [
        '%s: %s' % (f.ParameterName, f.FaultCode)
        for f in fault.SetParameterValuesFault or []
    ]
    return '%s (%s)' % (text, ', '.join(details)) if details else text


def _check_names(names: Sequence[str]) -> None:
    for name in names:
        if not isinstance(name, str) or not name.strip():
            raise InvalidTask('parameter names cannot be empty')


def _gpvs(names: Sequence[str], handler: Handler) -> List[models.GetParameterValues]:
    return [_gpv(batch) for batch in gpv_batches(names, handler.quirks)]


def _gpv(names: Sequence[str]) -> models.GetParameterValues:
    return models.GetParameterValues(
        ParameterNames=models.ParameterNames(
            string=list(names), arrayType='xsd:string[%d]' % len(names),
        ),
    )


def _spv(values: Sequence[Dict[str, str]], key: str) -> models.SetParameterValues:
    structs = [
        models.ParameterValueStruct(
            Name=p['name'],
            Value=models.anySimpleType(
                Data=p.get('value', ''), type=p.get('type') or 'xsd:string',
            ),
        )
        for p in values
    ]
    return models.SetParameterValues(
        ParameterList=models.ParameterValueList(
            ParameterValueStruct=structs,
            arrayType='cwmp:ParameterValueStruct[%d]' % len(structs),
        ),
        ParameterKey=models.ParameterKeyType(Data=key, type='xsd:string'),
    )


def list_items(container, name):
    return (getattr(container, name, None) or []) if container else []
