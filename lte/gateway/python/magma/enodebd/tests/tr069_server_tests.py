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
import unittest
from unittest import mock

from magma.enodebd.logger import EnodebdLogger
from magma.enodebd.tr069 import server
from magma.tr069 import logger as tr069_logger


class EnodebdTr069ServerTests(unittest.TestCase):
    def tearDown(self):
        tr069_logger.set_logger(logging.getLogger('magma.tr069'))

    @mock.patch('magma.enodebd.tr069.server._tr069_server')
    def test_starts_shared_server_with_enodebd_config(self, shared_server):
        manager = mock.Mock()

        server.tr069_server(manager)

        shared_server.assert_called_once_with(
            manager, 'enodebd', device_name='eNodeB',
        )
        self.assertIs(tr069_logger.logger.info, EnodebdLogger.info)


if __name__ == '__main__':
    unittest.main()
