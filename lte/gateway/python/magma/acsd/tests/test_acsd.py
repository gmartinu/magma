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

import http.client
import json
import os
import unittest
from unittest import mock

import fakeredis
import yaml
from google.protobuf import json_format
from lte.protos.mconfig import mconfigs_pb2
from magma.acsd import main
from magma.acsd.config import (
    DEFAULT_CWMP_PORT,
    DEFAULT_CWMP_WORKERS,
    CwmpBind,
    get_cwmp_bind,
    get_cwmp_workers,
)
from magma.acsd.session import CwmpSessionHandler
from magma.acsd.store import AcsStore


def memory_store():
    return AcsStore(fakeredis.FakeStrictRedis())


MAGMA_ROOT = os.environ.get('MAGMA_ROOT')
CONFIG_DIR = os.path.join(MAGMA_ROOT or '', 'lte/gateway/configs')


class CwmpBindTest(unittest.TestCase):
    def test_yml_port_when_mconfig_port_unset(self):
        bind = get_cwmp_bind(
            {'cwmp_interface': 'mtr0', 'cwmp_address': '10.1.0.1', 'cwmp_port': 48081},
            mconfigs_pb2.AcsD(),
        )
        self.assertEqual(bind, CwmpBind('mtr0', '10.1.0.1', 48081))

    def test_mconfig_port_overrides_yml(self):
        bind = get_cwmp_bind({'cwmp_port': 48081}, mconfigs_pb2.AcsD(port=49000))
        self.assertEqual(bind.port, 49000)

    def test_defaults_for_empty_config(self):
        bind = get_cwmp_bind({}, mconfigs_pb2.AcsD())
        self.assertEqual(bind, CwmpBind('mtr0', '10.1.0.1', DEFAULT_CWMP_PORT))


class CwmpWorkersTest(unittest.TestCase):
    def test_default(self):
        self.assertEqual(get_cwmp_workers({}), DEFAULT_CWMP_WORKERS)

    def test_from_yml(self):
        self.assertEqual(get_cwmp_workers({'cwmp_workers': 4}), 4)

    def test_never_below_one(self):
        self.assertEqual(get_cwmp_workers({'cwmp_workers': 0}), 1)


class CwmpListenerWiringTest(unittest.TestCase):
    def test_listener_answers_empty_post(self):
        thread = main.start_cwmp_listener(
            CwmpBind('lo', '127.0.0.1', 0), CwmpSessionHandler(store=memory_store()), 2,
        )
        server = thread.server
        try:
            conn = http.client.HTTPConnection(
                '127.0.0.1', server.server_address[1], timeout=5,
            )
            conn.request('POST', '/', b'')
            self.assertEqual(conn.getresponse().status, 204)
            conn.close()
        finally:
            with mock.patch.object(main._thread, 'interrupt_main'):
                server.shutdown()
                thread.join(5)
            server.server_close()

    def test_listener_exit_interrupts_main(self):
        with mock.patch.object(main._thread, 'interrupt_main') as interrupt, \
                mock.patch.object(main, 'make_cwmp_server') as make_server:
            make_server.return_value.serve_forever.side_effect = OSError
            main.start_cwmp_listener(
                CwmpBind('lo', '127.0.0.1', 0), CwmpSessionHandler(store=memory_store()), 1,
            ).join(5)
        interrupt.assert_called_once_with()


class SkeletonTest(unittest.TestCase):
    def test_no_operational_states_yet(self):
        self.assertEqual(main._get_operational_states(), [])

    def test_mode_defaults_to_active(self):
        self.assertEqual(mconfigs_pb2.AcsD().mode, mconfigs_pb2.AcsD.ACTIVE)


@unittest.skipUnless(MAGMA_ROOT, 'needs MAGMA_ROOT to read the shipped configs')
class ShippedConfigTest(unittest.TestCase):
    def test_acsd_yml(self):
        with open(os.path.join(CONFIG_DIR, 'acsd.yml')) as f:
            cfg = yaml.safe_load(f)
        bind = get_cwmp_bind(cfg, mconfigs_pb2.AcsD())
        self.assertEqual(bind, CwmpBind('mtr0', '10.1.0.1', 48081))
        self.assertEqual(get_cwmp_workers(cfg), 16)

    def test_gateway_mconfig_entry(self):
        with open(os.path.join(CONFIG_DIR, 'gateway.mconfig')) as f:
            entry = json.load(f)['configs_by_key']['acsd']
        self.assertEqual(entry.pop('@type'), 'type.googleapis.com/magma.mconfig.AcsD')
        mconfig = json_format.ParseDict(entry, mconfigs_pb2.AcsD())
        self.assertEqual(mconfig.mode, mconfigs_pb2.AcsD.ACTIVE)
        self.assertEqual(mconfig.port, 48081)

    def test_off_by_default(self):
        with open(os.path.join(CONFIG_DIR, 'magmad.yml')) as f:
            magmad = yaml.safe_load(f)
        self.assertIn('acsd', magmad['registered_dynamic_services'])
        with open(os.path.join(CONFIG_DIR, 'gateway.mconfig')) as f:
            dynamic = json.load(f)['configs_by_key']['magmad']['dynamic_services']
        self.assertNotIn('acsd', dynamic)


if __name__ == '__main__':
    unittest.main()
