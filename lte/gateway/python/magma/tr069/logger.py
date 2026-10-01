"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Logger used by the shared TR-069 package. Each service that hosts an ACS
(enodebd, acsd) plugs in its own logger with set_logger(), so TR-069 logs
land in that service's log file.
"""

import logging
from typing import Any


class _LoggerProxy:
    """Forwards logging calls to the backend chosen by the hosting service."""

    def __init__(self) -> None:
        self._backend: Any = logging.getLogger('magma.tr069')

    def set_backend(self, backend: Any) -> None:
        self._backend = backend

    def __getattr__(self, name: str) -> Any:
        return getattr(self._backend, name)


logger = _LoggerProxy()


def set_logger(backend: Any) -> None:
    """
    Route TR-069 logs to backend, which can be a logging.Logger or any object
    with the same debug/info/warning/error/exception methods.
    """
    logger.set_backend(backend)
