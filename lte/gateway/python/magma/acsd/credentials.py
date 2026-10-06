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

The same rotation sets the CPE's ConnectionRequestUsername/Password (the
username is the ACS one, the password its own), so both are promoted, or
dropped, together under one generation. Unlike the ACS password, the
Connection Request password is kept in clear: acsd is the Digest client
there, and the realm the CPE challenges with is only known when it does.
It only lets its holder ask the CPE to open a session to the ACS URL the
CPE already has, where the ACS credential is still required.

Two processes write these keys: acsd (rotations, promotions on the CWMP
path) and acsd_cli.py (reset-credentials, rotate-credentials, claim
changes). Every read-modify-write of a credential therefore holds a lock
in Redis as well as the process lock, so a CLI reset cannot be undone by
a promotion that read the credential before it, and a promotion cannot
be lost to a CLI write based on the credential from before it, which
would leave the CPE on a credential acsd no longer knows.
"""

import secrets
import threading
import time
from contextlib import contextmanager
from dataclasses import asdict, dataclass
from typing import Callable, NamedTuple, Optional, Tuple

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
    cr_password: str = ''
    pending_cr_password: str = ''

    @property
    def rotated(self) -> bool:
        """Has a per-CPE credential, so the bootstrap one is refused."""
        return bool(self.ha1)

    @property
    def connection_request(self) -> Optional[Tuple[str, str]]:
        """(username, password) for a Connection Request, if acsd set one."""
        if self.ha1 and self.cr_password:
            return self.username, self.cr_password
        return None


class Owner(NamedTuple):
    cpe_key: str
    # ACTIVE or PENDING
    which: str
    ha1: Ha1


# Long enough for any update to finish; a holder that dies frees it then.
LOCK_TTL_MS = 5000
LOCK_WAIT_SEC = 5.0
_LOCK_POLL_SEC = 0.01


class CredentialsBusy(RuntimeError):
    """The Redis lock of the credentials could not be taken in time."""


class RedisMutex:
    """
    A lock shared by every process on the Redis, held under a random token
    so a holder only ever frees its own hold. SET NX PX and WATCH/MULTI
    only: no Lua, which the test Redis lacks.
    """

    def __init__(
        self, client, key: str,
        ttl_ms: int = LOCK_TTL_MS, wait_sec: float = LOCK_WAIT_SEC,
    ):
        self._client, self._key = client, key
        self._ttl_ms, self._wait = ttl_ms, wait_sec

    @contextmanager
    def held(self):
        token = secrets.token_hex(16)
        deadline = time.monotonic() + self._wait
        while not self._client.set(self._key, token, nx=True, px=self._ttl_ms):
            if time.monotonic() >= deadline:
                raise CredentialsBusy('%s is held by another process' % self._key)
            time.sleep(_LOCK_POLL_SEC)
        try:
            yield
        finally:
            self._release(token)

    def _release(self, token: str) -> None:
        with self._client.pipeline() as pipe:
            try:
                pipe.watch(self._key)
                current = pipe.get(self._key)
                if current is not None and _text(current) == token:
                    pipe.multi()
                    pipe.delete(self._key)
                    pipe.execute()
            except Exception:  # pylint: disable=broad-except
                # Changed under us: it expired and someone else holds it.
                pass


def _text(value) -> str:
    return value.decode() if isinstance(value, bytes) else str(value)


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
        self._mutex = RedisMutex(client, '%s:credentials_lock' % prefix)
        self._depth = threading.local()

    @contextmanager
    def _locked(self):
        """Held around every read-modify-write, against both acsd's
        threads and the other process (see the module docstring).
        Reentrant within a thread, as the process lock is."""
        with self._lock:
            depth = getattr(self._depth, 'value', 0)
            self._depth.value = depth + 1
            try:
                if depth:
                    yield
                else:
                    with self._mutex.held():
                        yield
            finally:
                self._depth.value = depth

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
        with self._locked():
            cred = self.get(cpe_key)
            self._users.pop(cred.pending_username, None)
            generation = max(cred.generation, cred.pending_generation) + 1
            username = '%s.%d' % (cpe_key, generation)
            password = random_password(password_length)
            cred.pending_username = username
            cred.pending_ha1 = str(ha1_of(username, self.realm, password))
            cred.pending_generation = generation
            cred.pending_cr_password = random_password(password_length)
            cred.rotation_task_id = ''
            self._save(cred)
            self._users[username] = cpe_key
            return cred, password

    def set_rotation_task(self, cpe_key: str, task_id: str) -> None:
        with self._locked():
            cred = self.get(cpe_key)
            cred.rotation_task_id = task_id
            self._save(cred)

    def promote(self, cpe_key: str, generation: int) -> bool:
        """
        Make the pending credential of `generation` the active one and drop
        the previous one. False when that generation is not pending (it was
        promoted already, or replaced by a later rotation).
        """
        with self._locked():
            cred = self.get(cpe_key)
            if not cred.pending_ha1 or cred.pending_generation != generation:
                return False
            if cred.username:
                self._users.pop(cred.username, None)
            cred.username, cred.ha1 = cred.pending_username, cred.pending_ha1
            cred.generation = cred.pending_generation
            cred.cr_password = cred.pending_cr_password
            cred.pending_username, cred.pending_ha1 = '', ''
            cred.pending_cr_password = ''
            cred.rotation_task_id = ''
            self._save(cred)
            return True

    def abort_rotation(self, cpe_key: str, generation: int) -> None:
        """Drop the pending credential of `generation` if still pending."""
        with self._locked():
            cred = self.get(cpe_key)
            if cred.pending_generation != generation or not cred.pending_ha1:
                return
            self._users.pop(cred.pending_username, None)
            cred.pending_username, cred.pending_ha1 = '', ''
            cred.pending_cr_password = ''
            cred.rotation_task_id = ''
            self._save(cred)

    def drop_connection_request(self, cpe_key: str) -> bool:
        """
        Forget the Connection Request password, so the CPE's next session
        rotates both its credentials. False when it had none.
        """
        with self._locked():
            cred = self.get(cpe_key)
            if not cred.cr_password:
                return False
            cred.cr_password = ''
            self._save(cred)
            return True

    def reset(self, cpe_key: str) -> None:
        """
        Forget every credential of the CPE, so it may bootstrap again: after
        a factory reset, or when its claim goes away.
        """
        with self._locked():
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
