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
import io
import logging
import unittest
from unittest import mock

from magma.tr069 import logger as tr069_logger
from magma.tr069 import server


class Tr069ServerTests(unittest.TestCase):
    def setUp(self):
        self.log = mock.Mock()
        tr069_logger.set_logger(self.log)

    def tearDown(self):
        tr069_logger.set_logger(logging.getLogger('magma.tr069'))

    @mock.patch('magma.tr069.server._thread')
    @mock.patch('magma.tr069.server.make_server')
    @mock.patch('magma.tr069.server.get_ip_from_if', return_value='10.1.0.1')
    @mock.patch('magma.tr069.server.load_service_config')
    def _start(self, load_config, _ip, make_server, _thread, **kwargs):
        load_config.return_value = {
            'tr069': {'interface': 'mtr0', 'port': 48080},
        }
        server.tr069_server(mock.Mock(), 'acsd', **kwargs)
        load_config.assert_called_once_with('acsd')
        return make_server.call_args[0][4]

    def _log_broken_pipe(self, handler_cls):
        handler = handler_cls.__new__(handler_cls)
        handler.rfile = io.BytesIO(b'POST / HTTP/1.1\r\n\r\n')
        handler.wfile = io.BytesIO()
        handler.client_address = ('10.1.0.5', 7547)
        handler.server = mock.Mock()
        with mock.patch.object(handler_cls, 'parse_request', return_value=True), \
                mock.patch.object(handler_cls, 'get_environ', return_value={}), \
                mock.patch('magma.tr069.server.ServerHandler') as srv_handler:
            srv_handler.return_value.run.side_effect = BrokenPipeError
            handler.handle_single()
        self.log.warning.assert_called_once_with(
            '%s - %s', '10.1.0.5', mock.ANY,
        )
        return self.log.warning.call_args[0][2]

    def test_closed_connection_log_uses_callers_device_name(self):
        handler_cls = self._start(device_name='eNodeB')

        self.assertEqual(
            self._log_broken_pipe(handler_cls),
            'eNodeB has unexpectedly closed the TCP connection.',
        )

    def test_closed_connection_log_defaults_to_cpe(self):
        handler_cls = self._start()

        self.assertEqual(
            self._log_broken_pipe(handler_cls),
            'CPE has unexpectedly closed the TCP connection.',
        )


if __name__ == '__main__':
    unittest.main()
