"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Maps vendor TR-069 parameters onto the normalized CPE model through
per-model handlers, selected by the Inform DeviceId.
"""
from magma.acsd.datamodel.generic import GENERIC, GENERIC_HANDLER_NAME
from magma.acsd.datamodel.model import ROOT_TR098, ROOT_TR181, Field, Model
from magma.acsd.datamodel.registry import (
    DEFAULT_REGISTRY,
    Registry,
    detect_root,
    device_id_identity,
    device_info,
    fill_identity,
    normalize_inform,
    params_of,
)
from magma.acsd.datamodel.spec import (
    DeviceInfo,
    FieldPaths,
    Handler,
    Quirks,
    Spec,
    gpv_batches,
    paths,
)

__all__ = [
    'DEFAULT_REGISTRY', 'GENERIC', 'GENERIC_HANDLER_NAME', 'ROOT_TR098',
    'ROOT_TR181', 'DeviceInfo', 'Field', 'FieldPaths', 'Handler', 'Model',
    'Quirks', 'Registry', 'Spec', 'detect_root', 'device_id_identity',
    'device_info', 'fill_identity', 'gpv_batches', 'normalize_inform',
    'params_of', 'paths',
]
