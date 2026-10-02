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

import dataclasses
import unittest
from types import SimpleNamespace

import fakeredis
from magma.acsd import tasks
from magma.acsd.claimed import ROTATE_CREDENTIALS, ClaimedMode
from magma.acsd.claims import ClaimRegistry
from magma.acsd.config import LISTENER_MODE, MODE_CLAIMED
from magma.acsd.credentials import CredentialStore
from magma.acsd.datamodel import GENERIC, Quirks, Registry
from magma.acsd.digest import DIGEST_USERNAME, ha1_of
from magma.acsd.session import HTTP_403, CwmpSessionHandler
from magma.acsd.store import TASK_DONE, TASK_FAILED, AcsStore
from magma.tr069 import models

REALM = 'magma-acs'
BOOT_USER, BOOT_PASSWORD = 'bootstrap', 'boot-secret'
OUI, PRODUCT, SERIAL = '00A1B2', 'Titan4000', 'SN0001'
KEY = 'CLAIMtitan-1'
NAT_IP = '203.0.113.7'
MS = 'Device.ManagementServer.'
WAN_PARAM = 'InternetGatewayDevice.WANDevice.1.WANConnectionDevice.1.' \
    'WANIPConnection.1.ExternalIPAddress'


def _inform(serial=SERIAL, **params):
    return models.Inform(
        DeviceId=models.DeviceIdStruct(
            OUI=OUI, ProductClass=PRODUCT, SerialNumber=serial,
        ),
        MaxEnvelopes=1,
        ParameterList=models.ParameterValueList(ParameterValueStruct=[
            models.ParameterValueStruct(
                Name=name, Value=models.anySimpleType(Data=value),
            )
            for name, value in params.items()
        ]),
    )


def _spv_values(request):
    return {
        p.Name: p.Value.Data
        for p in request.ParameterList.ParameterValueStruct
    }


class ClaimedSessionTest(unittest.TestCase):
    """Drives the handler as the claimed listener would, after Digest."""

    def setUp(self):
        self.redis = fakeredis.FakeStrictRedis()
        self.store = AcsStore(self.redis)
        self.claims = ClaimRegistry(self.redis)
        self.claims.add(OUI, PRODUCT, SERIAL, claim_id='titan-1')
        self.creds = CredentialStore(self.redis, REALM)
        self.handler = self._handler()

    def _handler(self, registry=None):
        self.mode = ClaimedMode(
            self.claims, self.creds, self.store, BOOT_USER, BOOT_PASSWORD,
        )
        kwargs = {'registry': registry} if registry else {}
        return CwmpSessionHandler(
            lambda ip, inform: self.fail('core identity used'),
            store=self.store, claimed=self.mode, **kwargs,
        )

    def _ctx(self, username, port=40000, ip=NAT_IP):
        env = {
            'REMOTE_ADDR': ip, 'REMOTE_PORT': str(port),
            LISTENER_MODE: MODE_CLAIMED,
        }
        if username:
            env[DIGEST_USERNAME] = username
        return SimpleNamespace(
            transport=SimpleNamespace(req_env=env, resp_code=None),
        )

    def send(self, message, username=BOOT_USER, port=40000):
        ctx = self._ctx(username, port)
        return self.handler.handle_tr069_message(ctx, message), ctx

    def bootstrap_until_spv(self, **params):
        resp, ctx = self.send(_inform(**params))
        self.assertIsInstance(resp, models.InformResponse)
        self.assertIsNone(ctx.transport.resp_code)
        spv, _ = self.send(models.DummyInput())
        self.assertIsInstance(spv, models.SetParameterValues)
        return spv

    def test_unclaimed_device_is_refused(self):
        resp, ctx = self.send(_inform(serial='OTHER'))
        self.assertIsInstance(resp, models.DummyInput)
        self.assertEqual(ctx.transport.resp_code, HTTP_403)

    def test_request_without_digest_user_is_refused(self):
        _, ctx = self.send(_inform(), username=None)
        self.assertEqual(ctx.transport.resp_code, HTTP_403)

    def test_without_claimed_mode_claimed_requests_are_refused(self):
        handler = CwmpSessionHandler(store=self.store)
        ctx = self._ctx(BOOT_USER)
        handler.handle_tr069_message(ctx, _inform())
        self.assertEqual(ctx.transport.resp_code, HTTP_403)

    def test_bootstrap_rotates_to_a_per_cpe_credential(self):
        spv = self.bootstrap_until_spv()
        values = _spv_values(spv)
        self.assertEqual(set(values), {MS + 'Username', MS + 'Password'})
        username, password = values[MS + 'Username'], values[MS + 'Password']
        self.assertEqual(username, KEY + '.1')
        self.assertEqual(len(password), 32)
        # Not in use until the CPE accepts it.
        self.assertFalse(self.creds.get(KEY).rotated)

        end, _ = self.send(models.SetParameterValuesResponse(Status=0))
        self.assertIsInstance(end, models.DummyInput)
        cred = self.creds.get(KEY)
        self.assertTrue(cred.rotated)
        self.assertEqual(cred.ha1, ha1_of(username, REALM, password))
        self.assertEqual(self.mode.lookup(username, NAT_IP), cred.ha1)
        [task] = self.store.list_tasks(KEY)
        self.assertEqual((task.type, task.status), (ROTATE_CREDENTIALS, TASK_DONE))
        self.assertNotIn(password, repr(task))

        # From now on only the per-CPE credential gets in.
        _, ctx = self.send(_inform(), port=40001)
        self.assertEqual(ctx.transport.resp_code, HTTP_403)
        resp, ctx = self.send(_inform(), username=username, port=40002)
        self.assertIsInstance(resp, models.InformResponse)
        end, _ = self.send(models.DummyInput(), username=username, port=40002)
        self.assertIsInstance(end, models.DummyInput)
        self.assertEqual(len(self.store.list_tasks(KEY)), 1)

    def test_tr098_root_rotates_under_internet_gateway_device(self):
        spv = self.bootstrap_until_spv(
            **{'InternetGatewayDevice.ManagementServer.URL': 'https://acs'},
        )
        self.assertEqual(
            set(_spv_values(spv)),
            {
                'InternetGatewayDevice.ManagementServer.Username',
                'InternetGatewayDevice.ManagementServer.Password',
            },
        )

    def test_refused_rotation_keeps_bootstrap_and_retries_next_session(self):
        self.bootstrap_until_spv()
        self.send(models.Fault(FaultCode=9001, FaultString='denied'))
        self.assertFalse(self.creds.get(KEY).rotated)
        self.assertIsNone(self.creds.owner(KEY + '.1'))
        self.assertEqual(self.store.list_tasks(KEY)[0].status, TASK_FAILED)
        spv = self.bootstrap_until_spv()
        self.assertEqual(_spv_values(spv)[MS + 'Username'], KEY + '.2')

    def test_retryable_fault_keeps_the_pending_credential(self):
        self.bootstrap_until_spv()
        self.send(models.Fault(FaultCode=9002, FaultString='busy'))
        self.assertIsNotNone(self.creds.owner(KEY + '.1'))
        spv = self.bootstrap_until_spv()
        self.assertEqual(_spv_values(spv)[MS + 'Username'], KEY + '.1')

    def test_lost_answer_is_confirmed_by_the_new_credential(self):
        spv = self.bootstrap_until_spv()
        username = _spv_values(spv)[MS + 'Username']
        # The CPE applied it but the answer never arrived; it comes back
        # with the new credential on a new connection.
        resp, ctx = self.send(_inform(), username=username, port=40001)
        self.assertIsInstance(resp, models.InformResponse)
        self.assertTrue(self.creds.get(KEY).rotated)
        # The requeued rotation has nothing left to send.
        end, _ = self.send(models.DummyInput(), username=username, port=40001)
        self.assertIsInstance(end, models.DummyInput)
        self.assertEqual(self.store.list_tasks(KEY)[0].status, TASK_DONE)

    def test_credential_of_another_claim_is_refused(self):
        self.claims.add(OUI, PRODUCT, 'SN0002', claim_id='titan-2')
        self.creds.begin_rotation('CLAIMtitan-2', 16)
        self.creds.promote('CLAIMtitan-2', 1)
        _, ctx = self.send(_inform(), username='CLAIMtitan-2.1')
        self.assertEqual(ctx.transport.resp_code, HTTP_403)

    def test_quirks_set_password_length_or_skip_rotation(self):
        short = dataclasses.replace(
            GENERIC, name='short', quirks=Quirks(password_length=12),
        )
        self.handler = self._handler(Registry(fallback=short))
        spv = self.bootstrap_until_spv()
        self.assertEqual(len(_spv_values(spv)[MS + 'Password']), 12)

        self.claims.add(OUI, PRODUCT, 'SN0003', claim_id='titan-3')
        stuck = dataclasses.replace(
            GENERIC, name='stuck', quirks=Quirks(no_credential_rotation=True),
        )
        self.handler = self._handler(Registry(fallback=stuck))
        self.send(_inform(serial='SN0003'), port=41000)
        end, _ = self.send(models.DummyInput(), port=41000)
        self.assertIsInstance(end, models.DummyInput)
        self.assertEqual(self.store.list_tasks('CLAIMtitan-3'), [])

    def test_restarted_acsd_replaces_a_rotation_it_cannot_send(self):
        self.bootstrap_until_spv()
        # A new process: the session is gone and the password with it.
        self.store.end_all_sessions('acsd restarted')
        self.handler = self._handler()
        spv = self.bootstrap_until_spv()
        self.assertEqual(_spv_values(spv)[MS + 'Username'], KEY + '.2')
        first, second = self.store.list_tasks(KEY)
        self.assertEqual(first.status, TASK_FAILED)
        self.assertEqual(second.args['generation'], 2)

    def test_factory_reset_lets_the_cpe_bootstrap_again(self):
        self.bootstrap_until_spv()
        self.send(models.SetParameterValuesResponse(Status=0))
        username = KEY + '.1'
        tasks.enqueue_task(self.store, KEY, tasks.FACTORY_RESET)
        self.send(_inform(), username=username, port=40001)
        req, _ = self.send(models.DummyInput(), username=username, port=40001)
        self.assertIsInstance(req, models.FactoryReset)
        self.send(models.FactoryResetResponse(), username=username, port=40001)
        self.assertFalse(self.creds.get(KEY).rotated)
        resp, _ = self.send(_inform(), port=40002)
        self.assertIsInstance(resp, models.InformResponse)

    def test_wan_address_is_the_cpe_reported_one_not_the_nat(self):
        self.send(_inform(**{WAN_PARAM: '100.64.1.2'}))
        model = self.store.get_model(KEY).model
        self.assertEqual(model['wan']['ipv4_address'], '100.64.1.2')

    def test_cpes_behind_one_nat_get_separate_sessions(self):
        self.claims.add(OUI, PRODUCT, 'SN0002', claim_id='titan-2')
        self.send(_inform(), port=40000)
        self.send(_inform(serial='SN0002'), port=40001)
        keys = sorted(s.cpe_key for s in self.store.list_sessions())
        self.assertEqual(keys, [KEY, 'CLAIMtitan-2'])
        spv1, _ = self.send(models.DummyInput(), port=40000)
        spv2, _ = self.send(models.DummyInput(), port=40001)
        self.assertEqual(_spv_values(spv1)[MS + 'Username'], KEY + '.1')
        self.assertEqual(_spv_values(spv2)[MS + 'Username'], 'CLAIMtitan-2.1')


if __name__ == '__main__':
    unittest.main()
