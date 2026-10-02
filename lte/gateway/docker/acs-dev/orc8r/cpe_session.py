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
"""

import ipaddress
import subprocess
import sys

import grpc

sys.path.insert(0, '/magma/lte/gateway/docker/acs-dev')
import smoke_test as st  # noqa: E402
from lte.protos.mobilityd_pb2 import (  # noqa: E402
    AllocateIPRequest,
    ReleaseIPRequest,
)
from lte.protos.mobilityd_pb2_grpc import MobilityServiceStub  # noqa: E402
from lte.protos.subscriberdb_pb2 import SubscriberID  # noqa: E402


def main():
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
        methods, _, status = cpe.run_session()
        cpe.close()
    finally:
        subprocess.run(['ip', 'addr', 'del', ip + '/32', 'dev', 'lo'], check=False)
        mob.ReleaseIPAddress(
            ReleaseIPRequest(sid=sid, ip=resp.ip_list[0], apn=st.APN), timeout=5,
        )
    print('IMSI%s from %s: Inform HTTP %d, acsd sent %s, session end HTTP %d' % (
        st.IMSI, ip, inform.status, [m for m, _ in methods], status,
    ))
    return 0 if inform.status == 200 and status == 204 else 1


if __name__ == '__main__':
    sys.exit(main())
