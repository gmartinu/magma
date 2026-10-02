"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Handler selection, and the glue from TR-069 messages to the normalizer.
"""
from typing import Dict, Iterable, Optional, Sequence, Tuple

from magma.acsd.datamodel.generic import GENERIC
from magma.acsd.datamodel.model import ROOT_TR098, ROOT_TR181, Model
from magma.acsd.datamodel.spec import DeviceInfo, Handler


class Registry:
    """Selects the handler of a device, falling back to the generic one."""

    def __init__(
        self,
        handlers: Sequence[Handler] = (),
        fallback: Handler = GENERIC,
    ):
        self._handlers = tuple(handlers)
        self._fallback = fallback

    def select(self, info: DeviceInfo) -> Handler:
        """The best-matching handler; ties go to the one registered first."""
        best, best_score = self._fallback, 0
        for h in self._handlers:
            score = h.match(info)
            if score > best_score:
                best, best_score = h, score
        return best

    def get(self, name: str) -> Handler:
        """
        A handler by name, or the fallback if it is unknown, e.g. a stored
        session whose handler was removed since.
        """
        for h in self._handlers:
            if h.name == name:
                return h
        return self._fallback


def params_of(message) -> Dict[str, str]:
    """Name -> value of an Inform or GetParameterValuesResponse."""
    plist = getattr(message, 'ParameterList', None)
    structs = getattr(plist, 'ParameterValueStruct', None) or ()
    params = {}
    for s in structs:
        name = getattr(s, 'Name', None)
        if not name:
            continue
        value = getattr(s, 'Value', None)
        data = getattr(value, 'Data', value)
        params[name] = '' if data is None else str(data)
    return params


def detect_root(names: Iterable[str]) -> Optional[str]:
    """
    The data model root the parameter names use, or None. TR-098 wins: a
    TR-098 CPE may also list TR-106 "Device." objects, never the reverse.
    """
    root = None
    for n in names:
        if n.startswith(ROOT_TR098):
            return ROOT_TR098
        if n.startswith(ROOT_TR181):
            root = ROOT_TR181
    return root


def device_info(inform, params: Optional[Dict[str, str]] = None) -> DeviceInfo:
    """
    The Inform DeviceId, completed with the model name and firmware the CPE
    reports among its Inform parameters (DeviceId has neither).
    """
    device_id = getattr(inform, 'DeviceId', None)
    params = params_of(inform) if params is None else params

    def from_params(leaf: str) -> str:
        for root in (ROOT_TR181, ROOT_TR098):
            v = params.get(root + 'DeviceInfo.' + leaf)
            if v:
                return v
        return ''

    return DeviceInfo(
        manufacturer=getattr(device_id, 'Manufacturer', None) or '',
        oui=getattr(device_id, 'OUI', None) or '',
        product_class=getattr(device_id, 'ProductClass', None) or '',
        model_name=from_params('ModelName'),
        software_version=from_params('SoftwareVersion'),
    )


DEFAULT_REGISTRY = Registry()


def normalize_inform(
    inform, registry: Registry = DEFAULT_REGISTRY,
) -> Tuple[Handler, Model]:
    """
    Select the handler of the CPE behind an Inform and normalize the Inform.
    The Inform DeviceId fills the identity the parameters leave out. Later
    reads go through the same handler and merge on top:
    `model.merge(handler.normalize(model.root, params_of(gpv_response)))`.
    """
    params = params_of(inform)
    handler = registry.select(device_info(inform, params))
    root = detect_root(params) or ROOT_TR181
    model = handler.normalize(root, params)
    device_id = getattr(inform, 'DeviceId', None)
    for attr, source in _DEVICE_ID_FIELDS:
        if getattr(model.identity, attr) is None:
            setattr(
                model.identity, attr, getattr(device_id, source, None) or None,
            )
    return handler, model


_DEVICE_ID_FIELDS = (
    ('manufacturer', 'Manufacturer'),
    ('oui', 'OUI'),
    ('product_class', 'ProductClass'),
    ('serial_number', 'SerialNumber'),
)
