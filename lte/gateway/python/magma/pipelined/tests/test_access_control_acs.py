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

import ipaddress
import unittest
from unittest.mock import MagicMock

from lte.protos.mconfig.mconfigs_pb2 import PipelineD
from magma.pipelined.app.access_control import (
    DEFAULT_ACS_CONNECTION_REQUEST_PORT,
    AccessControlController,
)
from magma.pipelined.openflow.registers import DIRECTION_REG, Direction
from ryu.lib.packet import ether_types
from ryu.lib.packet.in_proto import IPPROTO_TCP
from ryu.ofproto import ofproto_v1_4_parser

MTR_NET = ipaddress.IPv4Network('10.1.0.1')
MTR_MATCH = (MTR_NET.network_address, MTR_NET.netmask)


def _get_config(access_control):
    controller = AccessControlController.__new__(AccessControlController)
    controller.logger = MagicMock()
    config = {
        'setup_type': 'LTE',
        'mtr_interface': 'mtr0',
        'access_control': dict(ip_blocklist=[], **access_control),
    }
    return controller._get_config(config, PipelineD()), controller.logger


class AcsConfigTest(unittest.TestCase):
    """access_control.acs_port parsing; no OVS needed."""

    def test_unset_by_default(self):
        config, _ = _get_config({})
        self.assertIsNone(config.acs_port)
        self.assertIsNone(config.acs_connection_request_port)

    def test_cr_port_defaults_when_acs_port_set(self):
        config, _ = _get_config({'acs_port': 48081})
        self.assertEqual(config.acs_port, 48081)
        self.assertEqual(
            config.acs_connection_request_port,
            DEFAULT_ACS_CONNECTION_REQUEST_PORT,
        )

    def test_cr_port_override(self):
        config, _ = _get_config(
            {'acs_port': 48081, 'acs_connection_request_port': 30005},
        )
        self.assertEqual(config.acs_connection_request_port, 30005)

    def test_cr_port_ignored_without_acs_port(self):
        config, _ = _get_config({'acs_connection_request_port': 7547})
        self.assertIsNone(config.acs_connection_request_port)

    def test_invalid_acs_port_disables_rule(self):
        for bad in (0, 65536, -1, '48081', True, 1.5):
            config, logger = _get_config({'acs_port': bad})
            self.assertIsNone(config.acs_port, bad)
            logger.error.assert_called_once()


class AcsMatchTest(unittest.TestCase):
    """Flows built for the ACS exception; no OVS needed."""

    def _matches(self, cr_port=DEFAULT_ACS_CONNECTION_REQUEST_PORT):
        return [
            m.ryu_match for m in
            AccessControlController._create_magma_match_acs_flows(
                MTR_NET, 48081, cr_port,
            )
        ]

    def test_all_flows_are_uplink_tcp_to_mtr_ip_only(self):
        for match in self._matches():
            self.assertEqual(match[DIRECTION_REG], Direction.OUT.value)
            self.assertEqual(match['eth_type'], ether_types.ETH_TYPE_IP)
            self.assertEqual(match['ip_proto'], IPPROTO_TCP)
            self.assertEqual(match['ipv4_dst'], MTR_MATCH)

    def test_acs_port_flow(self):
        acs = self._matches()[0]
        self.assertEqual(acs['tcp_dst'], 48081)
        self.assertNotIn('tcp_src', acs)
        self.assertNotIn('tcp_flags', acs)

    def test_cr_reply_flows_never_allow_a_bare_syn(self):
        replies = self._matches()[1:]
        self.assertEqual(len(replies), 2)
        flags = {m['tcp_flags'] for m in replies}
        self.assertEqual(flags, {(0x012, 0x012), (0, 0x002)})
        for match in replies:
            self.assertEqual(match['tcp_src'], DEFAULT_ACS_CONNECTION_REQUEST_PORT)
            self.assertNotIn('tcp_dst', match)
            value, mask = match['tcp_flags']
            # A SYN without ACK (new connection) must not match any flow.
            self.assertNotEqual(0x002 & mask, value)

    def test_cr_reply_flows_put_tcp_flags_after_prerequisites(self):
        # OVS rejects a match whose field precedes its prerequisites
        # (OFPBMC_BAD_PREREQ); ryu, not the caller, decides the order.
        for match in self._matches()[1:]:
            fields = [
                f for f, _ in ofproto_v1_4_parser.OFPMatch(**match)._fields2
            ]
            flags_at = fields.index('tcp_flags')
            for prereq in ('eth_type', 'ip_proto', 'tcp_src'):
                self.assertLess(fields.index(prereq), flags_at)

    def test_no_cr_flows_without_cr_port(self):
        self.assertEqual(len(self._matches(cr_port=None)), 1)


if __name__ == "__main__":
    unittest.main()
