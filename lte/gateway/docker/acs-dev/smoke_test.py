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
"""

import http.client
import ipaddress
import os
import subprocess
import sys
import uuid

import grpc
import yaml
from google.protobuf.empty_pb2 import Empty
from lte.protos.mobilityd_pb2 import AllocateIPRequest, ReleaseIPRequest
from lte.protos.mobilityd_pb2_grpc import MobilityServiceStub
from lte.protos.subscriberdb_pb2 import SubscriberID
from magma.acsd import tasks
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

results = []


def check(name, ok, detail=''):
    results.append(ok)
    print('%s  %s  %s' % ('PASS' if ok else 'FAIL', name, detail))


class Cpe:
    """One CWMP session from source_ip on one keep-alive connection,
    answering the Digest challenge like a CPE does."""

    def __init__(self, source_ip, credential):
        self.username, self.password = credential
        self.conn = http.client.HTTPConnection(
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

    def close(self):
        self.conn.close()


def dev_credential():
    with open(DEV_ACSD_YML) as f:
        auth = yaml.safe_load(f)['cwmp_auth']
    return auth['username'], auth['password']


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
    finally:
        subprocess.run(['ip', 'addr', 'del', ip + '/32', 'dev', 'lo'], check=False)
        mob.ReleaseIPAddress(
            ReleaseIPRequest(sid=sid, ip=resp.ip_list[0], apn=APN), timeout=5,
        )

    print('%d/%d passed' % (sum(results), len(results)))
    return 0 if all(results) else 1


if __name__ == '__main__':
    sys.exit(main())
