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

from magma.acsd.bindings import (
    BindingChange,
    BindingChangeKind,
    InMemoryBindingStore,
    SerialBinder,
)

IMSI_A = 'IMSI001010000000001'
IMSI_B = 'IMSI001010000000002'
SERIAL_1 = 'TITAN0001'
SERIAL_2 = 'TITAN0002'


class SerialBinderTest(unittest.TestCase):
    def setUp(self):
        self.store = InMemoryBindingStore()
        self.events = []
        self.binder = SerialBinder(self.store, on_change=self.events.append)

    def test_first_contact_binds_without_event(self):
        self.assertEqual(self.binder.bind(IMSI_A, SERIAL_1), [])
        self.assertEqual(self.store.serial_for(IMSI_A), SERIAL_1)
        self.assertEqual(self.store.imsi_for(SERIAL_1), IMSI_A)
        self.assertEqual(self.events, [])

    def test_same_pair_again_is_silent(self):
        self.binder.bind(IMSI_A, SERIAL_1)
        self.assertEqual(self.binder.bind(IMSI_A, SERIAL_1), [])
        self.assertEqual(self.events, [])

    def test_serial_change_for_known_imsi_emits_event_and_rebinds(self):
        self.binder.bind(IMSI_A, SERIAL_1)
        changes = self.binder.bind(IMSI_A, SERIAL_2)
        expected = BindingChange(
            BindingChangeKind.SERIAL_CHANGED, IMSI_A, SERIAL_2, SERIAL_1,
        )
        self.assertEqual(changes, [expected])
        self.assertEqual(self.events, [expected])
        self.assertEqual(self.store.serial_for(IMSI_A), SERIAL_2)
        self.assertIsNone(self.store.imsi_for(SERIAL_1))

    def test_imsi_change_for_known_serial_emits_event_and_rebinds(self):
        self.binder.bind(IMSI_A, SERIAL_1)
        changes = self.binder.bind(IMSI_B, SERIAL_1)
        expected = BindingChange(
            BindingChangeKind.IMSI_CHANGED, IMSI_B, SERIAL_1, IMSI_A,
        )
        self.assertEqual(changes, [expected])
        self.assertEqual(self.events, [expected])
        self.assertEqual(self.store.imsi_for(SERIAL_1), IMSI_B)
        self.assertIsNone(self.store.serial_for(IMSI_A))

    def test_swap_reports_both_changes(self):
        self.binder.bind(IMSI_A, SERIAL_1)
        self.binder.bind(IMSI_B, SERIAL_2)
        changes = self.binder.bind(IMSI_A, SERIAL_2)
        self.assertEqual(
            {c.kind for c in changes},
            {BindingChangeKind.SERIAL_CHANGED, BindingChangeKind.IMSI_CHANGED},
        )
        self.assertEqual(self.store.serial_for(IMSI_A), SERIAL_2)
        self.assertIsNone(self.store.serial_for(IMSI_B))
        self.assertIsNone(self.store.imsi_for(SERIAL_1))

    def test_empty_values_rejected(self):
        with self.assertRaises(ValueError):
            self.binder.bind(IMSI_A, '')
        with self.assertRaises(ValueError):
            self.binder.bind('', SERIAL_1)

    def test_default_sink_logs_change(self):
        binder = SerialBinder()
        binder.bind(IMSI_A, SERIAL_1)
        with self.assertLogs(level='WARNING') as logs:
            binder.bind(IMSI_A, SERIAL_2)
        self.assertIn('event=serial_changed', logs.output[0])
        self.assertIn('previous=%s' % SERIAL_1, logs.output[0])

    def test_failing_sink_does_not_break_binding(self):
        def broken(_change):
            raise RuntimeError('eventd down')
        binder = SerialBinder(self.store, on_change=broken)
        binder.bind(IMSI_A, SERIAL_1)
        with self.assertLogs(level='ERROR'):
            changes = binder.bind(IMSI_A, SERIAL_2)
        self.assertEqual(len(changes), 1)
        self.assertEqual(self.store.serial_for(IMSI_A), SERIAL_2)


if __name__ == '__main__':
    unittest.main()
