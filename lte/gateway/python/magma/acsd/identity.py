"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

CWMP session identity: the CPE is the IMSI behind the session's source IP.

There is no TLS or Digest in the MVP, so the source IP is the only thing
the CPE cannot forge: pipelined drops UE traffic whose source IP is not the
one mobilityd allocated to that UE. Every failure to resolve therefore
refuses the session (fail closed). A CPE whose session is refused retries
the Inform with backoff (TR-069 3.2.1.1), so a mobilityd outage delays a
CPE but never lets an unidentified one in.
"""
import logging
from typing import Optional

from magma.acsd.mobilityd_client import MobilitydClient, MobilitydUnavailable


class CpeIdentifier:
    """
    The `identify(source_ip) -> imsi | None` hook of the CWMP listener.

    Call it once per session, at session start, and keep the result for the
    session: one mobilityd RPC per session, and the IMSI cannot change under
    a session that is already running. None means refuse the session.
    """

    def __init__(self, mobilityd: Optional[MobilitydClient] = None):
        self._mobilityd = mobilityd or MobilitydClient()

    def __call__(self, source_ip: str) -> Optional[str]:
        try:
            imsi = self._mobilityd.get_imsi_for_ip(source_ip)
        except ValueError:
            logging.warning(
                'acsd identity: refusing session, bad source_ip=%r',
                source_ip,
            )
            return None
        except MobilitydUnavailable as err:
            logging.error(
                'acsd identity: refusing session, source_ip=%s '
                'mobilityd unavailable: %s', source_ip, err,
            )
            return None
        if imsi is None:
            logging.warning(
                'acsd identity: refusing session, source_ip=%s '
                'has no IMSI in mobilityd', source_ip,
            )
            return None
        logging.info(
            'acsd identity: source_ip=%s imsi=%s', source_ip, imsi,
        )
        return imsi
