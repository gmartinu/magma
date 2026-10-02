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

import json
import os
import unittest

import yaml
from google.protobuf import json_format
from lte.protos.mconfig import mconfigs_pb2
from magma.acsd import main
from magma.acsd.config import DEFAULT_CWMP_PORT, CwmpBind, get_cwmp_bind

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
