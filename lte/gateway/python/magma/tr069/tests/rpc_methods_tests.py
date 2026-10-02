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
from unittest import TestCase

from magma.tr069 import models
from magma.tr069.rpc_methods import AutoConfigServer
from magma.tr069.spyne_mods import Tr069Application, Tr069Soap11
from spyne.server.wsgi import WsgiApplication

ENVELOPE = (
    '<soap-env:Envelope'
    ' xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/"'
    ' xmlns:cwmp="urn:dslforum-org:cwmp-1-0">'
    '<soap-env:Header><cwmp:ID soap-env:mustUnderstand="1">7</cwmp:ID>'
    '</soap-env:Header><soap-env:Body>%s</soap-env:Body></soap-env:Envelope>'
)


class Recorder:
    """Answers every CPE message with `reply` and records what it got."""

    def __init__(self, reply):
        self.reply = reply
        self.received = []

    def handle_tr069_message(self, ctx, message):
        self.received.append(message)
        return self.reply


def post(body: bytes) -> bytes:
    app = WsgiApplication(
        Tr069Application(
            [AutoConfigServer], models.CWMP_NS,
            in_protocol=Tr069Soap11(validator='soft'),
            out_protocol=Tr069Soap11(),
        ),
    )
    environ = {
        'REQUEST_METHOD': 'POST',
        'CONTENT_TYPE': 'text/xml; charset=utf-8',
        'CONTENT_LENGTH': str(len(body)),
        'wsgi.input': io.BytesIO(body),
        'wsgi.url_scheme': 'http',
        'SERVER_NAME': 'localhost',
        'SERVER_PORT': '80',
        'PATH_INFO': '/',
        'QUERY_STRING': '',
    }
    return b''.join(app(environ, lambda status, headers: None))


class FactoryResetTest(TestCase):
    def tearDown(self):
        AutoConfigServer.set_state_machine_manager(None)

    def test_factory_reset_round_trip(self):
        handler = Recorder(models.FactoryReset())
        AutoConfigServer.set_state_machine_manager(handler)
        out = post((ENVELOPE % '<cwmp:FactoryResetResponse/>').encode())
        self.assertIsInstance(handler.received[0], models.FactoryResetResponse)
        self.assertIn(b'<cwmp:FactoryReset', out)
        self.assertNotIn(b'FactoryResetResponse', out)

    def test_reboot_still_keeps_its_namespace(self):
        handler = Recorder(models.Reboot(CommandKey='k1'))
        AutoConfigServer.set_state_machine_manager(handler)
        out = post((ENVELOPE % '<cwmp:RebootResponse/>').encode())
        self.assertIsInstance(handler.received[0], models.RebootResponse)
        self.assertIn(b'<cwmp:Reboot>', out)
        self.assertIn(b'<CommandKey>k1</CommandKey>', out)
