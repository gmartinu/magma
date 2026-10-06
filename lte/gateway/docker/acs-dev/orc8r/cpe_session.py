"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

One CWMP session of the smoke test's core CPE (IMSI from SMOKE_IMSI), so a
task queued through the Orc8r REST API runs. Prints what acsd sent and the
final HTTP status. Run it like the smoke test:
  docker compose run --rm --entrypoint python3 cpe-sim \
    /magma/lte/gateway/docker/acs-dev/orc8r/cpe_session.py

--kpis also queues a refresh of the radio KPIs and answers it with RSRP,
RSRQ and SINR drifting around a fair signal, so acs_rsrp_dbm & co. get a
series; with --repeat N --interval S the NMS charts get a curve.
"""

import argparse
import ipaddress
import random
import subprocess
import sys
import time

import grpc

sys.path.insert(0, '/magma/lte/gateway/docker/acs-dev')
import smoke_test as st  # noqa: E402
from lte.protos.mobilityd_pb2 import (  # noqa: E402
    AllocateIPRequest,
    ReleaseIPRequest,
)
from lte.protos.mobilityd_pb2_grpc import MobilityServiceStub  # noqa: E402
from lte.protos.subscriberdb_pb2 import SubscriberID  # noqa: E402
from magma.acsd import tasks  # noqa: E402
from magma.acsd.store import AcsStore  # noqa: E402
from magma.common.redis.client import get_default_client  # noqa: E402

CELL = 'Device.Cellular.Interface.1.'
# Start, low, high of each KPI's drift.
KPIS = {'RSRP': (-92.0, -115.0, -70.0), 'RSRQ': (-10.0, -18.0, -5.0), 'SINR': (12.0, -3.0, 25.0)}
GPV_RESPONSE = (
    '<soap-env:Envelope xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/"'
    ' xmlns:soap-enc="http://schemas.xmlsoap.org/soap/encoding/"'
    ' xmlns:xsd="http://www.w3.org/2001/XMLSchema"'
    ' xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"'
    ' xmlns:cwmp="urn:dslforum-org:cwmp-1-0"><soap-env:Header>'
    '<cwmp:ID soap-env:mustUnderstand="1">kpis</cwmp:ID></soap-env:Header>'
    '<soap-env:Body><cwmp:GetParameterValuesResponse>'
    '<ParameterList soap-enc:arrayType="cwmp:ParameterValueStruct[{n}]">{items}'
    '</ParameterList></cwmp:GetParameterValuesResponse></soap-env:Body></soap-env:Envelope>'
)
ITEM = (
    '<ParameterValueStruct><Name>{name}</Name>'
    '<Value xsi:type="xsd:int">{value}</Value></ParameterValueStruct>'
)


class Radio:
    """KPIs that drift a little each session."""

    def __init__(self):
        self.values = {k: v[0] for k, v in KPIS.items()}

    def step(self):
        for k, (_, low, high) in KPIS.items():
            self.values[k] = min(high, max(low, self.values[k] + random.uniform(-2, 2)))

    def gpv_response(self, request):
        names = [e.text for e in request.iter() if e.tag.rpartition('}')[2] == 'string']
        items = [
            ITEM.format(name=n, value=round(self.values[n[len(CELL):]]))
            for n in names if n.startswith(CELL) and n[len(CELL):] in KPIS
        ]
        return GPV_RESPONSE.format(n=len(items), items=''.join(items)).encode()


def run_session(cpe, radio):
    """st.Cpe.run_session, also answering GetParameterValues."""
    methods, body = [], b''
    for _ in range(10):
        resp, data = cpe.post(body, st.XML if body else None)
        if resp.status != 200:
            return methods, resp.status
        method, request = st._rpc_of(data)  # pylint: disable=protected-access
        methods.append(method)
        if method == 'GetParameterValues' and radio:
            body = radio.gpv_response(request)
        elif method in st.ANSWERS:
            body = st.ANSWERS[method]
        else:
            return methods, resp.status
    return methods, 0


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--kpis', action='store_true', help='refresh and answer the radio KPIs')
    parser.add_argument('--repeat', type=int, default=1)
    parser.add_argument('--interval', type=float, default=30)
    args = parser.parse_args()
    radio = Radio() if args.kpis else None
    ok = True
    for i in range(args.repeat):
        if i:
            time.sleep(args.interval)
        ok = session(radio) and ok
    return 0 if ok else 1


def session(radio):
    if radio:
        radio.step()
        tasks.enqueue_task(
            AcsStore(get_default_client()), 'IMSI' + st.IMSI, tasks.REFRESH,
            parameter_names=[CELL + k for k in KPIS],
        )
    mob = MobilityServiceStub(grpc.insecure_channel(st.MOBILITYD))
    sid = SubscriberID(id=st.IMSI, type=SubscriberID.IMSI)
    resp = mob.AllocateIPAddress(
        AllocateIPRequest(sid=sid, version=AllocateIPRequest.IPV4, apn=st.APN),
        timeout=5,
    )
    ip = str(ipaddress.ip_address(resp.ip_list[0].address))
    subprocess.run(['ip', 'addr', 'add', ip + '/32', 'dev', 'lo'], check=False)
    try:
        cpe = st.Cpe(ip, st.dev_credential())
        _, inform, _ = cpe.inform()
        methods, status = run_session(cpe, radio)
        cpe.close()
    finally:
        subprocess.run(['ip', 'addr', 'del', ip + '/32', 'dev', 'lo'], check=False)
        mob.ReleaseIPAddress(
            ReleaseIPRequest(sid=sid, ip=resp.ip_list[0], apn=st.APN), timeout=5,
        )
    print('IMSI%s from %s: Inform HTTP %d, acsd sent %s, session end HTTP %d' % (
        st.IMSI, ip, inform.status, methods, status,
    ))
    if radio:
        print('  answered %s' % {k: round(v) for k, v in radio.values.items()})
    return inform.status == 200 and status == 204


if __name__ == '__main__':
    sys.exit(main())
