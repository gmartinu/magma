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

import unittest
from types import SimpleNamespace
from unittest import mock

from magma.acsd.session import HTTP_403, CwmpSessionHandler, accept_all
from magma.tr069 import models


def _ctx(source_ip='192.168.128.12'):
    return SimpleNamespace(
        transport=SimpleNamespace(
            req_env={'REMOTE_ADDR': source_ip}, resp_code=None,
        ),
    )


def _inform(serial='SIM0001'):
    return models.Inform(
        DeviceId=models.DeviceIdStruct(SerialNumber=serial), MaxEnvelopes=1,
    )


class CwmpSessionHandlerTest(unittest.TestCase):
    def test_inform_gets_inform_response(self):
        ctx = _ctx()
        resp = CwmpSessionHandler().handle_tr069_message(ctx, _inform())
        self.assertIsInstance(resp, models.InformResponse)
        self.assertEqual(resp.MaxEnvelopes, 1)
        self.assertIsNone(ctx.transport.resp_code)

    def test_empty_post_ends_session(self):
        resp = CwmpSessionHandler().handle_tr069_message(
            _ctx(), models.DummyInput(),
        )
        self.assertIsInstance(resp, models.DummyInput)

    def test_unsolicited_message_ends_session(self):
        resp = CwmpSessionHandler().handle_tr069_message(
            _ctx(), models.GetParameterValuesResponse(),
        )
        self.assertIsInstance(resp, models.DummyInput)

    def test_identify_gets_source_ip_and_inform(self):
        identify = mock.Mock(return_value='IMSI001010000000001')
        inform = _inform()
        CwmpSessionHandler(identify).handle_tr069_message(
            _ctx('192.168.128.40'), inform,
        )
        identify.assert_called_once_with('192.168.128.40', inform)

    def test_identify_only_runs_on_inform(self):
        identify = mock.Mock(return_value='IMSI001010000000001')
        CwmpSessionHandler(identify).handle_tr069_message(
            _ctx(), models.DummyInput(),
        )
        identify.assert_not_called()

    def test_refused_session_is_403_without_inform_response(self):
        ctx = _ctx()
        resp = CwmpSessionHandler(lambda ip, inform: None) \
            .handle_tr069_message(ctx, _inform())
        self.assertIsInstance(resp, models.DummyInput)
        self.assertEqual(ctx.transport.resp_code, HTTP_403)

    def test_inform_without_device_id(self):
        resp = CwmpSessionHandler().handle_tr069_message(
            _ctx(), models.Inform(),
        )
        self.assertIsInstance(resp, models.InformResponse)

    def test_accept_all_keys_by_source_ip(self):
        self.assertEqual(accept_all('10.0.0.1', _inform()), '10.0.0.1')


class SessionIdentityTest(unittest.TestCase):
    IMSI = 'IMSI001010000000001'

    def setUp(self):
        self.identity = self.IMSI
        self.handler = CwmpSessionHandler(lambda ip, inform: self.identity)

    def test_identity_held_after_inform(self):
        self.handler.handle_tr069_message(_ctx('10.1.0.5'), _inform())
        self.assertEqual(self.handler.session_identity('10.1.0.5'), self.IMSI)
        self.assertIsNone(self.handler.session_identity('10.1.0.6'))

    def test_session_end_drops_identity(self):
        self.handler.handle_tr069_message(_ctx('10.1.0.5'), _inform())
        self.handler.handle_tr069_message(
            _ctx('10.1.0.5'), models.DummyInput(),
        )
        self.assertIsNone(self.handler.session_identity('10.1.0.5'))

    def test_refused_inform_drops_previous_identity(self):
        self.handler.handle_tr069_message(_ctx('10.1.0.5'), _inform())
        self.identity = None
        self.handler.handle_tr069_message(_ctx('10.1.0.5'), _inform())
        self.assertIsNone(self.handler.session_identity('10.1.0.5'))
