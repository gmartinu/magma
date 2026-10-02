#!/usr/bin/env python3
"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

acsd dev-environment smoke test. Runs inside the shared network namespace
(compose service `cpe-sim`), so 127.0.0.1 reaches acsd and mobilityd.

It drives both connection modes: a core CPE, named by the IMSI behind its
UE IP on the plain CWMP listener, and a claimed CPE, named by its claim on
the HTTPS WAN listener. Then it runs a task on each through the CpeManager
gRPC API and reads acsd's Service303 metrics and operational state.
"""

import http.client
import ipaddress
import os
import ssl
import subprocess
import sys
import uuid
import xml.etree.ElementTree as ET

import grpc
import yaml
from google.protobuf.empty_pb2 import Empty
from lte.protos import cpe_acs_pb2 as cpe_pb
from lte.protos.cpe_acs_pb2_grpc import CpeManagerStub
from lte.protos.mobilityd_pb2 import AllocateIPRequest, ReleaseIPRequest
from lte.protos.mobilityd_pb2_grpc import MobilityServiceStub
from lte.protos.subscriberdb_pb2 import SubscriberID
from magma.acsd import tasks
from magma.acsd.claims import cpe_key_of
from magma.acsd.credentials import CredentialStore
from magma.acsd.digest import digest_response, parse_digest
from magma.acsd.mobilityd_client import MobilitydClient
from magma.acsd.store import TASK_DONE, AcsStore
from magma.common.redis.client import get_default_client
from orc8r.protos.service303_pb2 import ServiceInfo
from orc8r.protos.service303_pb2_grpc import Service303Stub

IMSI = os.environ.get('SMOKE_IMSI', '001010000000001')
APN = 'internet'
ACSD_303 = '127.0.0.1:50086'
MOBILITYD = '127.0.0.1:60051'
CWMP_PORT = 48081
WAN_PORT = 48443
WAN_CERT = '/var/opt/magma/certs/acsd_wan.crt'
ACSD_CLI = '/magma/lte/gateway/python/scripts/acsd_cli.py'
# The fixture's DeviceId, claimed for the claimed-mode half.
CLAIM_ID = 'sim-smoke'
CLAIM_KEY = cpe_key_of(CLAIM_ID)
MS = 'Device.ManagementServer.'
FIXTURE = '/magma/lte/gateway/python/magma/acsd/tests/fixtures/sim4000_inform.xml'
DEV_ACSD_YML = '/var/opt/magma/configs/acsd.yml'
XML = {'Content-Type': 'text/xml; charset=utf-8'}
REBOOT_RESPONSE = (
    b'<soap-env:Envelope'
    b' xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/"'
    b' xmlns:cwmp="urn:dslforum-org:cwmp-1-0"><soap-env:Header>'
    b'<cwmp:ID soap-env:mustUnderstand="1">smoke</cwmp:ID></soap-env:Header>'
    b'<soap-env:Body><cwmp:RebootResponse/></soap-env:Body></soap-env:Envelope>'
)
SPV_RESPONSE = (
    b'<soap-env:Envelope'
    b' xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/"'
    b' xmlns:cwmp="urn:dslforum-org:cwmp-1-0"><soap-env:Header>'
    b'<cwmp:ID soap-env:mustUnderstand="1">smoke</cwmp:ID></soap-env:Header>'
    b'<soap-env:Body><cwmp:SetParameterValuesResponse><Status>0</Status>'
    b'</cwmp:SetParameterValuesResponse></soap-env:Body></soap-env:Envelope>'
)
ANSWERS = {'Reboot': REBOOT_RESPONSE, 'SetParameterValues': SPV_RESPONSE}

results = []


def check(name, ok, detail=''):
    results.append(ok)
    print('%s  %s  %s' % ('PASS' if ok else 'FAIL', name, detail))


class Cpe:
    """One CWMP session from source_ip on one keep-alive connection,
    answering the Digest challenge like a CPE does."""

    def __init__(self, source_ip, credential, conn=None):
        self.username, self.password = credential
        self.conn = conn or http.client.HTTPConnection(
            source_ip, CWMP_PORT, timeout=10, source_address=(source_ip, 0),
        )
        self.challenge = None
        self.nc = 0

    def post(self, body, headers=None):
        self.conn.request('POST', '/', body, headers or {})
        resp = self.conn.getresponse()
        return resp, resp.read()

    def post_authenticated(self, body, headers=None):
        """POST, answering one 401 with Digest; returns the 401 as well."""
        resp, data = self.post(body, headers)
        if resp.status != 401:
            return None, resp, data
        challenge = resp
        self.challenge = parse_digest(
            resp.getheader('WWW-Authenticate', '').partition(' ')[2],
        )
        auth = dict(headers or {}, Authorization=self._authorization())
        resp, data = self.post(body, auth)
        return challenge, resp, data

    def _authorization(self):
        c = self.challenge
        self.nc += 1
        nc, cnonce = '%08x' % self.nc, uuid.uuid4().hex
        response = digest_response(
            'MD5', self.username, c['realm'], self.password, 'POST', '/',
            c['nonce'], nc, cnonce, 'auth',
        )
        return (
            'Digest username="%s", realm="%s", nonce="%s", uri="/", '
            'algorithm=MD5, qop=auth, nc=%s, cnonce="%s", response="%s"' % (
                self.username, c['realm'], c['nonce'], nc, cnonce, response,
            )
        )

    def inform(self):
        with open(FIXTURE, 'rb') as f:
            return self.post_authenticated(f.read(), XML)

    def run_session(self):
        """
        After the InformResponse: empty POST, then answer every request
        acsd sends until it ends the session. Returns the methods acsd
        sent (with each task id seen), the values of any
        SetParameterValues, and the final HTTP status.
        """
        methods, values, body = [], {}, b''
        for _ in range(10):
            resp, data = self.post(body, XML if body else None)
            if resp.status != 200:
                return methods, values, resp.status
            method, request = _rpc_of(data)
            methods.append((method, data))
            if method == 'SetParameterValues':
                values.update(
                    (p.findtext('Name'), p.findtext('Value'))
                    for p in request.iter('ParameterValueStruct')
                )
            if method not in ANSWERS:
                return methods, values, resp.status
            body = ANSWERS[method]
        return methods, values, 0

    def close(self):
        self.conn.close()


def claimed_cpe(credential):
    """A CPE on the claimed listener, trusting only the dev cert."""
    context = ssl.create_default_context(cafile=WAN_CERT)
    conn = http.client.HTTPSConnection(
        '127.0.0.1', WAN_PORT, timeout=10, context=context,
    )
    return Cpe('127.0.0.1', credential, conn)


def _rpc_of(data):
    """The (method name, element) of the CWMP request in a SOAP body."""
    body = ET.fromstring(data).find('{http://schemas.xmlsoap.org/soap/envelope/}Body')
    request = list(body)[0]
    return request.tag.rpartition('}')[2], request


def dev_config():
    with open(DEV_ACSD_YML) as f:
        return yaml.safe_load(f)


def dev_credential():
    auth = dev_config()['cwmp_auth']
    return auth['username'], auth['password']


def bootstrap_credential():
    wan = dev_config()['cwmp_wan']
    return wan['bootstrap_username'], wan['bootstrap_password']


def check_sessions(ip, imsi, credential):
    store = AcsStore(get_default_client())
    # Left by an aborted run; it would answer the empty POST below.
    leftover = store.pending_count(imsi)
    cpe = Cpe(ip, credential)
    challenge, resp, body = cpe.inform()
    check(
        'Inform without credentials gets a Digest challenge',
        challenge is not None and challenge.status == 401
        and 'Digest' in challenge.getheader('WWW-Authenticate', ''),
        'HTTP %s' % (challenge.status if challenge else resp.status),
    )
    check(
        'Authenticated Inform from allocated IP accepted',
        resp.status == 200 and b'InformResponse' in body,
        'HTTP %d' % resp.status,
    )
    resp, _ = cpe.post(b'')
    check(
        'Empty POST with no task ends the session', resp.status == 204,
        'HTTP %d, %d tasks left by an earlier run' % (resp.status, leftover),
    )
    cpe.close()

    model = store.get_model(imsi)
    wan = (model.model.get('wan') or {}).get('ipv4_address') if model else None
    check(
        'Normalized model stored, WAN = source IP', wan == ip,
        'handler=%s wan=%s' % (model.handler if model else None, wan),
    )

    task = tasks.enqueue_task(store, imsi, tasks.REBOOT)
    cpe = Cpe(ip, credential)
    _, resp, body = cpe.inform()
    resp, body = cpe.post(b'')
    check(
        'Queued Reboot sent on the next session',
        resp.status == 200 and b'Reboot' in body and task.task_id.encode() in body,
        'task %s, HTTP %d' % (task.task_id, resp.status),
    )
    resp, _ = cpe.post(REBOOT_RESPONSE, XML)
    status = store.get_task(task.task_id).status
    check(
        'RebootResponse completes the task', resp.status == 204 and status == TASK_DONE,
        'HTTP %d, task %s' % (resp.status, status),
    )
    cpe.close()


def check_unallocated_ip(credential):
    stray = '192.168.128.250'
    subprocess.run(['ip', 'addr', 'add', stray + '/32', 'dev', 'lo'], check=False)
    try:
        cpe = Cpe(stray, credential)
        _, resp, _ = cpe.inform()
        cpe.close()
        check('Authenticated Inform from unallocated IP refused', resp.status == 403, 'HTTP %d' % resp.status)
    finally:
        subprocess.run(['ip', 'addr', 'del', stray + '/32', 'dev', 'lo'], check=False)


def acsd_cli(*args):
    return subprocess.run(
        ['python3', ACSD_CLI] + list(args), capture_output=True, text=True,
    )


def check_claimed():
    """
    Claim the fixture's device, bootstrap it over HTTPS and let acsd rotate
    it to its own credential. Returns that credential.
    """
    # Start each run from a fresh claim: removing it also drops the per-CPE
    # credential of the last run, so the CPE bootstraps again.
    acsd_cli('claim-remove', CLAIM_ID)
    added = acsd_cli(
        'claim-add', '--oui', '00A1B2', '--product-class', 'SIM4000',
        '--serial', 'SIM0001', '--id', CLAIM_ID,
    )
    check(
        'acsd_cli.py claim-add for the simulator', added.returncode == 0,
        (added.stdout or added.stderr).strip(),
    )

    cpe = claimed_cpe(bootstrap_credential())
    challenge, resp, body = cpe.inform()
    check(
        'Claimed: HTTPS Inform with the bootstrap credential accepted',
        challenge is not None and challenge.status == 401
        and resp.status == 200 and b'InformResponse' in body,
        'HTTP %s then %d' % (challenge.status if challenge else '-', resp.status),
    )
    methods, values, status = cpe.run_session()
    cpe.close()
    username, password = values.get(MS + 'Username'), values.get(MS + 'Password')
    check(
        'Claimed: rotation SetParameterValues, then 204',
        status == 204 and bool(username and password)
        and username != bootstrap_credential()[0],
        'sent %s, HTTP %d, new user %s' % ([m for m, _ in methods], status, username),
    )
    creds = CredentialStore(get_default_client(), '')
    check(
        'Claimed: per-CPE credential promoted', creds.get(CLAIM_KEY).rotated,
        CLAIM_KEY,
    )

    model = AcsStore(get_default_client()).get_model(CLAIM_KEY)
    wan = (model.model.get('wan') or {}).get('ipv4_address') if model else None
    check(
        'Claimed: model stored, WAN address not the (NAT) source IP',
        model is not None and wan != '127.0.0.1',
        'handler=%s wan=%s' % (model.handler if model else None, wan),
    )

    cpe = claimed_cpe(bootstrap_credential())
    _, resp, _ = cpe.inform()
    cpe.close()
    check(
        'Claimed: bootstrap credential refused once rotated', resp.status == 403,
        'HTTP %d' % resp.status,
    )

    cpe = claimed_cpe((username, password))
    _, resp, body = cpe.inform()
    _, _, status = cpe.run_session()
    cpe.close()
    check(
        'Claimed: per-CPE credential accepted',
        resp.status == 200 and b'InformResponse' in body and status == 204,
        'HTTP %d, session end HTTP %d' % (resp.status, status),
    )
    return username, password


def check_grpc(ip, imsi, credential, per_cpe):
    """List both CPEs, queue a Reboot on each and let their sessions run it."""
    stub = CpeManagerStub(grpc.insecure_channel(ACSD_303))
    cpes = {c.cpe_key: c for c in stub.ListCpes(cpe_pb.ListCpesRequest(), timeout=5).cpes}
    core, claimed = cpes.get(imsi), cpes.get(CLAIM_KEY)
    check(
        'gRPC ListCpes: core and claimed CPE, each with its mode',
        core is not None and core.mode == cpe_pb.CPE_MODE_CORE
        and core.imsi == imsi
        and claimed is not None and claimed.mode == cpe_pb.CPE_MODE_CLAIMED,
        ', '.join(
            '%s=%s' % (k, cpe_pb.CpeMode.Name(c.mode)) for k, c in sorted(cpes.items())
        ),
    )

    queued = {
        key: stub.EnqueueTask(cpe_pb.EnqueueTaskRequest(
            cpe_key=key, type=cpe_pb.CPE_TASK_TYPE_REBOOT,
        ), timeout=5)
        for key in (imsi, CLAIM_KEY)
    }
    check(
        'gRPC EnqueueTask Reboot on both',
        all(t.status == cpe_pb.CPE_TASK_STATUS_PENDING for t in queued.values()),
        ', '.join('%s=%s' % (k, t.task_id) for k, t in queued.items()),
    )

    for key, cpe in ((imsi, Cpe(ip, credential)), (CLAIM_KEY, claimed_cpe(per_cpe))):
        task_id = queued[key].task_id
        _, resp, _ = cpe.inform()
        methods, _, status = cpe.run_session()
        cpe.close()
        ran = any(m == 'Reboot' and task_id.encode() in d for m, d in methods)
        got = stub.GetTask(cpe_pb.GetTaskRequest(task_id=task_id), timeout=5)
        check(
            'gRPC task runs in the next session, GetTask done (%s)' % key,
            resp.status == 200 and ran and status == 204
            and got.status == cpe_pb.CPE_TASK_STATUS_DONE,
            'sent %s, HTTP %d, task %s' % (
                [m for m, _ in methods], status, cpe_pb.CpeTaskStatus.Name(got.status),
            ),
        )


def check_service303(imsi):
    stub = Service303Stub(grpc.insecure_channel(ACSD_303))
    families = {f.name for f in stub.GetMetrics(Empty(), timeout=5).family}
    wanted = ('acs_informs_total', 'acs_online_cpes')
    # prometheus_client names a counter's family without the _total suffix.
    missing = [
        n for n in wanted
        if n not in families and n[:-len('_total')] not in families
    ]
    check(
        'Service303 GetMetrics has acs_informs_total and acs_online_cpes',
        not missing, 'missing %s' % missing if missing else
        '%d acs_* families' % sum(1 for f in families if f.startswith('acs_')),
    )
    states = stub.GetOperationalStates(Empty(), timeout=5).states
    rows = {s.deviceID for s in states if s.type == 'cpe_acs'}
    check(
        'Service303 GetOperationalStates has cpe_acs for both CPEs',
        {imsi, CLAIM_KEY} <= rows, 'cpe_acs rows %s' % sorted(rows),
    )


def main():
    info = Service303Stub(grpc.insecure_channel(ACSD_303)).GetServiceInfo(
        Empty(), timeout=5,
    )
    check(
        'acsd Service303 GetServiceInfo', info.state == ServiceInfo.ALIVE,
        'state=%s health=%s' % (
            ServiceInfo.ServiceState.Name(info.state),
            ServiceInfo.ApplicationHealth.Name(info.health),
        ),
    )

    mob = MobilityServiceStub(grpc.insecure_channel(MOBILITYD))
    sid = SubscriberID(id=IMSI, type=SubscriberID.IMSI)
    resp = mob.AllocateIPAddress(
        AllocateIPRequest(sid=sid, version=AllocateIPRequest.IPV4, apn=APN),
        timeout=5,
    )
    ip = str(ipaddress.ip_address(resp.ip_list[0].address))
    check('mobilityd AllocateIPAddress', bool(ip), 'IMSI%s -> %s' % (IMSI, ip))

    imsi = MobilitydClient().get_imsi_for_ip(ip)
    check(
        'acsd MobilitydClient.get_imsi_for_ip', imsi is not None and IMSI in imsi,
        '%s -> %s' % (ip, imsi),
    )

    subprocess.run(['ip', 'addr', 'add', ip + '/32', 'dev', 'lo'], check=False)
    try:
        credential = dev_credential()
        check_sessions(ip, imsi, credential)
        check_unallocated_ip(credential)
        per_cpe = check_claimed()
        check_grpc(ip, imsi, credential, per_cpe)
        check_service303(imsi)
    finally:
        subprocess.run(['ip', 'addr', 'del', ip + '/32', 'dev', 'lo'], check=False)
        mob.ReleaseIPAddress(
            ReleaseIPRequest(sid=sid, ip=resp.ip_list[0], apn=APN), timeout=5,
        )

    print('%d/%d passed' % (sum(results), len(results)))
    return 0 if all(results) else 1


if __name__ == '__main__':
    sys.exit(main())
