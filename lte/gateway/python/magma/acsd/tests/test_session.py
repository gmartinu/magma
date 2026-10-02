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
        identify = mock.Mock(return_value=True)
        inform = _inform()
        CwmpSessionHandler(identify).handle_tr069_message(
            _ctx('192.168.128.40'), inform,
        )
        identify.assert_called_once_with('192.168.128.40', inform)

    def test_identify_only_runs_on_inform(self):
        identify = mock.Mock(return_value=True)
        CwmpSessionHandler(identify).handle_tr069_message(
            _ctx(), models.DummyInput(),
        )
        identify.assert_not_called()

    def test_refused_session_is_403_without_inform_response(self):
        ctx = _ctx()
        resp = CwmpSessionHandler(lambda ip, inform: False) \
            .handle_tr069_message(ctx, _inform())
        self.assertIsInstance(resp, models.DummyInput)
        self.assertEqual(ctx.transport.resp_code, HTTP_403)

    def test_inform_without_device_id(self):
        resp = CwmpSessionHandler().handle_tr069_message(
            _ctx(), models.Inform(),
        )
        self.assertIsInstance(resp, models.InformResponse)

    def test_accept_all(self):
        self.assertTrue(accept_all('10.0.0.1', _inform()))
