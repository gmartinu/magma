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
import os
import subprocess
import sys
import unittest
from unittest import mock

from magma.tr069 import logger as tr069_logger


class Tr069LoggerTests(unittest.TestCase):
    def tearDown(self):
        tr069_logger.set_logger(logging.getLogger('magma.tr069'))

    def test_default_backend_is_package_logger(self):
        self.assertIs(
            tr069_logger.logger.info.__self__,
            logging.getLogger('magma.tr069'),
        )

    def test_set_logger_routes_calls_to_backend(self):
        backend = mock.Mock()
        tr069_logger.set_logger(backend)

        tr069_logger.logger.warning('msg %s', 'arg')

        backend.warning.assert_called_once_with('msg %s', 'arg')


class Tr069PackageIsolationTests(unittest.TestCase):
    def test_package_does_not_import_enodebd(self):
        code = (
            'import sys\n'
            'import magma.tr069.models, magma.tr069.rpc_methods\n'
            'import magma.tr069.server, magma.tr069.spyne_mods\n'
            'print(any(m.startswith("magma.enodebd") for m in sys.modules))\n'
        )
        env = dict(os.environ, PYTHONPATH=os.pathsep.join(sys.path))
        out = subprocess.run(
            [sys.executable, '-c', code],
            env=env, capture_output=True, text=True, check=True,
        )
        self.assertEqual(out.stdout.strip(), 'False')


if __name__ == '__main__':
    unittest.main()
