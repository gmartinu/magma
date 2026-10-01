"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Starts enodebd's TR-069 server on the shared magma.tr069 ACS stack.
"""

from magma.enodebd.logger import EnodebdLogger
from magma.enodebd.state_machines.enb_acs_manager import StateMachineManager
from magma.tr069.logger import set_logger
from magma.tr069.server import tr069_server as _tr069_server


def tr069_server(state_machine_manager: StateMachineManager) -> None:
    """ Serve TR-069 for enodebd, binding to enodebd.yml's tr069 section """
    set_logger(EnodebdLogger)
    _tr069_server(state_machine_manager, "enodebd", device_name="eNodeB")
