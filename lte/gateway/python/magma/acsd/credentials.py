"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Per-CPE ACS credentials of claimed CPEs (H.1b).

Only the Digest HA1 is stored, never the password. Every rotation gets a
new username (cpe_key.generation), so an authenticated username alone says
which credential the CPE used: the active one, or the pending one it
applied in a session whose SPV answer acsd never saw.
"""

import secrets
import threading
import time
from dataclasses import asdict, dataclass
from typing import Callable, NamedTuple, Optional

from magma.acsd.digest import Ha1, ha1_of
from magma.common.redis.containers import RedisHashDict
from magma.common.redis.serializers import (
    get_json_deserializer,
    get_json_serializer,
)

ACTIVE = 'active'
PENDING = 'pending'


@dataclass
class CpeCredential:
    cpe_key: str
    username: str = ''
    ha1: str = ''
    generation: int = 0
    pending_username: str = ''
    pending_ha1: str = ''
    pending_generation: int = 0
    rotation_task_id: str = ''
    updated: float = 0.0

    @property
    def rotated(self) -> bool:
        """Has a per-CPE credential, so the bootstrap one is refused."""
        return bool(self.ha1)


class Owner(NamedTuple):
    cpe_key: str
    # ACTIVE or PENDING
    which: str
    ha1: Ha1


def random_password(length: int) -> str:
    # URL-safe so no CPE web UI or SOAP escaping mangles it.
    return secrets.token_urlsafe(length)[:length]


class CredentialStore:
    def __init__(
        self,
        client,
        realm: str,
        prefix: str = 'acsd',
        clock: Callable[[], float] = time.time,
    ):
        self.realm = realm
        self._clock = clock
        self._lock = threading.RLock()

        def hash_dict(name):
            return RedisHashDict(
                client, '%s:%s' % (prefix, name),
                get_json_serializer(), get_json_deserializer(),
            )
        self._creds = hash_dict('credentials')
        # username -> cpe_key, for active and pending usernames.
        self._users = hash_dict('credential_users')

    def get(self, cpe_key: str) -> CpeCredential:
        raw = self._creds.get(cpe_key)
        return CpeCredential(**raw) if raw else CpeCredential(cpe_key)

    def owner(self, username: str) -> Optional[Owner]:
        """Whose credential `username` is, and whether it is in use yet."""
        cpe_key = self._users.get(username)
        if not cpe_key:
            return None
        cred = self.get(cpe_key)
        if username == cred.username and cred.ha1:
            return Owner(cpe_key, ACTIVE, Ha1(cred.ha1))
        if username == cred.pending_username and cred.pending_ha1:
            return Owner(cpe_key, PENDING, Ha1(cred.pending_ha1))
        return None

    def begin_rotation(self, cpe_key: str, password_length: int):
        """
        Make a new pending credential, replacing any earlier pending one.
        Returns (credential, password); the password is not kept here.
        """
        with self._lock:
            cred = self.get(cpe_key)
            self._users.pop(cred.pending_username, None)
            generation = max(cred.generation, cred.pending_generation) + 1
            username = '%s.%d' % (cpe_key, generation)
            password = random_password(password_length)
            cred.pending_username = username
            cred.pending_ha1 = str(ha1_of(username, self.realm, password))
            cred.pending_generation = generation
            cred.rotation_task_id = ''
            self._save(cred)
            self._users[username] = cpe_key
            return cred, password

    def set_rotation_task(self, cpe_key: str, task_id: str) -> None:
        with self._lock:
            cred = self.get(cpe_key)
            cred.rotation_task_id = task_id
            self._save(cred)

    def promote(self, cpe_key: str, generation: int) -> bool:
        """
        Make the pending credential of `generation` the active one and drop
        the previous one. False when that generation is not pending (it was
        promoted already, or replaced by a later rotation).
        """
        with self._lock:
            cred = self.get(cpe_key)
            if not cred.pending_ha1 or cred.pending_generation != generation:
                return False
            if cred.username:
                self._users.pop(cred.username, None)
            cred.username, cred.ha1 = cred.pending_username, cred.pending_ha1
            cred.generation = cred.pending_generation
            cred.pending_username, cred.pending_ha1 = '', ''
            cred.rotation_task_id = ''
            self._save(cred)
            return True

    def abort_rotation(self, cpe_key: str, generation: int) -> None:
        """Drop the pending credential of `generation` if still pending."""
        with self._lock:
            cred = self.get(cpe_key)
            if cred.pending_generation != generation or not cred.pending_ha1:
                return
            self._users.pop(cred.pending_username, None)
            cred.pending_username, cred.pending_ha1 = '', ''
            cred.rotation_task_id = ''
            self._save(cred)

    def reset(self, cpe_key: str) -> None:
        """
        Forget every credential of the CPE, so it may bootstrap again: after
        a factory reset, or when its claim goes away.
        """
        with self._lock:
            raw = self._creds.pop(cpe_key, None)
            if raw is None:
                return
            cred = CpeCredential(**raw)
            for username in (cred.username, cred.pending_username):
                if username:
                    self._users.pop(username, None)

    def _save(self, cred: CpeCredential) -> None:
        cred.updated = self._clock()
        self._creds[cred.cpe_key] = asdict(cred)
