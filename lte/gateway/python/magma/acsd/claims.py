"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Claims: CPEs outside the Magma core that acsd agrees to manage.

A claimed CPE has no IMSI, so the operator names it ahead of time by its
TR-069 DeviceId (OUI, ProductClass, SerialNumber). Its per-CPE records are
keyed by CLAIM_PREFIX + claim id, which can never equal an IMSI key.

Claims are added locally today (Python API and acsd_cli.py); Stage 3 feeds
them from Orc8r through replace_all().
"""

import re
import threading
import time
import uuid
from dataclasses import asdict, dataclass
from typing import Callable, Iterable, List, Optional

from magma.common.redis.containers import RedisHashDict
from magma.common.redis.serializers import (
    get_json_deserializer,
    get_json_serializer,
)

CLAIM_PREFIX = 'CLAIM'
_CLAIM_ID = re.compile(r'^[A-Za-z0-9_-]{1,64}$')


class ClaimError(ValueError):
    pass


@dataclass
class Claim:
    claim_id: str
    oui: str
    product_class: str
    serial: str
    network: str = ''
    label: str = ''
    created: float = 0.0

    @property
    def cpe_key(self) -> str:
        return cpe_key_of(self.claim_id)


def cpe_key_of(claim_id: str) -> str:
    return CLAIM_PREFIX + claim_id


def is_claim_key(cpe_key: str) -> bool:
    return cpe_key.startswith(CLAIM_PREFIX)


def device_key(oui: str, product_class: str, serial: str) -> str:
    """
    OUI is hex, so it is compared without case; ProductClass and serial are
    opaque vendor strings and compared exactly.
    """
    return '\x1f'.join((oui.strip().upper(), product_class.strip(), serial.strip()))


class ClaimRegistry:
    """Claims in Redis next to the AcsStore records, under the same prefix."""

    def __init__(
        self,
        client,
        prefix: str = 'acsd',
        clock: Callable[[], float] = time.time,
    ):
        self._clock = clock
        self._lock = threading.RLock()

        def hash_dict(name):
            return RedisHashDict(
                client, '%s:%s' % (prefix, name),
                get_json_serializer(), get_json_deserializer(),
            )
        self._claims = hash_dict('claims')
        # device_key -> claim id, so an Inform finds its claim in one read.
        self._by_device = hash_dict('claims_by_device')

    def add(
        self,
        oui: str,
        product_class: str,
        serial: str,
        claim_id: str = '',
        network: str = '',
        label: str = '',
    ) -> Claim:
        """Claim a CPE. Raises ClaimError on a bad or duplicate claim."""
        claim_id = claim_id or uuid.uuid4().hex[:12]
        if not _CLAIM_ID.match(claim_id):
            raise ClaimError('claim id %r: want 1-64 of [A-Za-z0-9_-]' % claim_id)
        if not oui.strip() or not serial.strip():
            raise ClaimError('a claim needs an OUI and a serial number')
        key = device_key(oui, product_class, serial)
        with self._lock:
            if claim_id in self._claims:
                raise ClaimError('claim %s already exists' % claim_id)
            if key in self._by_device:
                raise ClaimError(
                    'device %s/%s/%s is already claimed by %s' % (
                        oui, product_class, serial, self._by_device[key],
                    ),
                )
            claim = Claim(
                claim_id=claim_id,
                oui=oui.strip().upper(),
                product_class=product_class.strip(),
                serial=serial.strip(),
                network=network,
                label=label,
                created=self._clock(),
            )
            self._claims[claim_id] = asdict(claim)
            self._by_device[key] = claim_id
            return claim

    def remove(self, claim_id: str) -> Optional[Claim]:
        with self._lock:
            raw = self._claims.pop(claim_id, None)
            if raw is None:
                return None
            claim = Claim(**raw)
            self._by_device.pop(
                device_key(claim.oui, claim.product_class, claim.serial), None,
            )
            return claim

    def get(self, claim_id: str) -> Optional[Claim]:
        raw = self._claims.get(claim_id)
        return Claim(**raw) if raw else None

    def list(self) -> List[Claim]:
        return sorted(
            (Claim(**raw) for raw in self._claims.values()),
            key=lambda c: (c.created, c.claim_id),
        )

    def match(self, oui: str, product_class: str, serial: str) -> Optional[Claim]:
        """The claim of the CPE an Inform DeviceId names, if any."""
        claim_id = self._by_device.get(device_key(oui, product_class, serial))
        return self.get(claim_id) if claim_id else None

    def replace_all(self, claims: Iterable[Claim]) -> List[str]:
        """
        Make `claims` the whole set, for a feed that owns them (Orc8r,
        Stage 3). Returns the claim ids that were removed or now name
        another device: the caller drops their credentials.
        """
        wanted = {c.claim_id: c for c in claims}
        with self._lock:
            reset = [cid for cid in self._claims.keys() if cid not in wanted]
            for claim_id in reset:
                self.remove(claim_id)
            for claim in wanted.values():
                current = self.get(claim.claim_id)
                if current is not None and _same_device(current, claim):
                    current.network, current.label = claim.network, claim.label
                    self._claims[claim.claim_id] = asdict(current)
                    continue
                if current is not None:
                    self.remove(claim.claim_id)
                    reset.append(claim.claim_id)
                self.add(
                    claim.oui, claim.product_class, claim.serial,
                    claim.claim_id, claim.network, claim.label,
                )
            return reset


def _same_device(a: Claim, b: Claim) -> bool:
    return (
        device_key(a.oui, a.product_class, a.serial)
        == device_key(b.oui, b.product_class, b.serial)
    )
