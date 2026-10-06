"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Claimed mode: identity and credentials of CPEs outside the Magma core.

There is no IMSI behind the source IP, so the identity is the claim the
Inform DeviceId names, and it only counts when the Digest username belongs
to that claim: the shared bootstrap username until the CPE has a per-CPE
credential, that credential afterwards. The bootstrap session queues a
rotation that sets the per-CPE credential by SetParameterValues through the
ordinary task queue; it becomes the only accepted one once the CPE applies
it.

At BOOTSTRAP the session also sets a short PeriodicInformInterval: a carrier
NAT usually keeps acsd from sending a Connection Request, so a queued task
waits for the CPE's next periodic Inform, and the CPE's factory interval
(often a day) would be the task latency.
"""

import dataclasses
import hmac
import logging
import threading
from typing import Dict, List, Optional, Sequence, Tuple

from magma.acsd import tasks
from magma.acsd.claims import ClaimRegistry
from magma.acsd.credentials import PENDING, CredentialStore
from magma.acsd.datamodel import Handler
from magma.acsd.digest import Ha1
from magma.acsd.store import TASK_IN_PROGRESS, TASK_PENDING, AcsStore, Task
from spyne.model.complex import ComplexModelBase

# Internal task type: not in tasks.TYPES, so no external caller can queue it.
ROTATE_CREDENTIALS = 'rotate_credentials'
CONFIGURE_PERIODIC_INFORM = 'configure_periodic_inform'
INTERNAL_TYPES = (ROTATE_CREDENTIALS, CONFIGURE_PERIODIC_INFORM)
ROTATION_TTL_SEC = 24 * 3600
EVENT_BOOTSTRAP = '0 BOOTSTRAP'


class ClaimedMode:
    """
    The claimed listener's CredentialProvider and session identity, and the
    credential rotation the session handler runs for it.

    Rotation passwords live only in this process until the CPE has them. A
    rotation planned after an acsd restart cannot be sent, so it fails and
    the next bootstrap session starts a new one.
    """

    def __init__(
        self,
        claims: ClaimRegistry,
        credentials: CredentialStore,
        store: AcsStore,
        bootstrap_username: str,
        bootstrap_password: str,
        periodic_inform_interval: int = 0,
    ):
        """
        `periodic_inform_interval` (seconds) is set on every claimed CPE that
        Informs with BOOTSTRAP; 0 leaves the CPE's own.
        """
        self._claims = claims
        self._credentials = credentials
        self._store = store
        self._bootstrap_username = bootstrap_username
        self._bootstrap_password = bootstrap_password
        self._periodic_inform_interval = max(0, int(periodic_inform_interval))
        self._lock = threading.Lock()
        # cpe_key -> (generation, password) of the rotation in flight.
        self._passwords: Dict[str, Tuple[int, str]] = {}

    @property
    def claims(self) -> ClaimRegistry:
        return self._claims

    @property
    def credentials(self) -> CredentialStore:
        return self._credentials

    def is_bootstrap(self, username: Optional[str]) -> bool:
        return bool(self._bootstrap_username) and hmac.compare_digest(
            (username or '').encode(), self._bootstrap_username.encode(),
        )

    def lookup(self, username: str, source_ip: str) -> Optional[str]:
        """
        CredentialProvider: the bootstrap password, or the HA1 of a per-CPE
        username. Which CPE may use it is checked on the Inform, in
        identify(), once the DeviceId is known.
        """
        if self.is_bootstrap(username):
            return self._bootstrap_password or None
        owner = self._credentials.owner(username)
        return Ha1(owner.ha1) if owner else None

    def identify(
        self, source_ip: str, inform, username: Optional[str],
    ) -> Optional[str]:
        """The cpe_key of a claimed CPE, or None to refuse the session."""
        device_id = getattr(inform, 'DeviceId', None)
        oui = getattr(device_id, 'OUI', None) or ''
        product_class = getattr(device_id, 'ProductClass', None) or ''
        serial = getattr(device_id, 'SerialNumber', None) or ''
        claim = self._claims.match(oui, product_class, serial)
        if claim is None:
            logging.warning(
                'acsd claimed: refusing %s, device %s/%s/%s is not claimed',
                source_ip, oui, product_class, serial,
            )
            return None
        key = claim.cpe_key
        if not username:
            logging.warning('acsd claimed: refusing %s without Digest', key)
            return None
        if self.is_bootstrap(username):
            if self._credentials.get(key).rotated:
                logging.warning(
                    'acsd claimed: refusing bootstrap credential from %s (%s): '
                    'it has a per-CPE credential', key, source_ip,
                )
                return None
            return key
        owner = self._credentials.owner(username)
        if owner is None or owner.cpe_key != key:
            logging.warning(
                'acsd claimed: refusing %s (%s): user %r is not its credential',
                key, source_ip, username,
            )
            return None
        if owner.which == PENDING:
            # The CPE applied a rotation whose answer acsd never got.
            generation = self._credentials.get(key).pending_generation
            if self._credentials.promote(key, generation):
                self._forget(key, generation)
                logging.info('acsd claimed: %s confirmed credential %s', key, username)
        return key

    def session_started(
        self,
        cpe_key: str,
        username: Optional[str],
        handler: Handler,
        events: Sequence[str] = (),
        root: str = 'Device.',
    ) -> None:
        """
        Queue what a claimed CPE needs first: a credential rotation while it
        is on the bootstrap credential, then, on BOOTSTRAP, the periodic
        Inform settings.
        """
        self._maybe_rotate(cpe_key, username, handler)
        if EVENT_BOOTSTRAP in events:
            self._configure_periodic_inform(cpe_key, root)

    def _maybe_rotate(
        self, cpe_key: str, username: Optional[str], handler: Handler,
    ) -> None:
        """
        Rotate a CPE on the bootstrap credential, and one on its own that
        has no Connection Request credential: rotated before acsd set those,
        or told to by `acsd_cli.py rotate-credentials`.
        """
        bootstrap = self.is_bootstrap(username)
        if not bootstrap and self._credentials.get(cpe_key).cr_password:
            return
        if handler.quirks.no_credential_rotation:
            logging.info(
                'acsd claimed: %s keeps the bootstrap credential (handler %s '
                'does not rotate)', cpe_key, handler.name,
            )
            return
        with self._lock:
            cred = self._credentials.get(cpe_key)
            if cred.rotation_task_id and self._live(cred.rotation_task_id):
                held = self._passwords.get(cpe_key)
                if held and held[0] == cred.pending_generation:
                    return
                # Queued by an earlier acsd process; its password is gone.
                self._store.fail_task(
                    cred.rotation_task_id, tasks.FAULT_INTERNAL_ERROR,
                    'rotation password lost (acsd restarted)', False,
                )
            cred, password = self._credentials.begin_rotation(
                cpe_key, handler.quirks.effective_password_length,
            )
            generation = cred.pending_generation
            self._passwords[cpe_key] = (generation, password)
            task = self._store.create_task(
                cpe_key, ROTATE_CREDENTIALS,
                {'username': cred.pending_username, 'generation': generation},
                ttl_sec=ROTATION_TTL_SEC,
            )
            self._credentials.set_rotation_task(cpe_key, task.task_id)
        logging.info(
            'acsd claimed: %s %s; rotation %s queued', cpe_key,
            'on the bootstrap credential' if bootstrap
            else 'without a Connection Request credential',
            task.task_id,
        )

    def _configure_periodic_inform(self, cpe_key: str, root: str) -> None:
        """
        Every BOOTSTRAP, even when acsd set it before: a factory reset is
        what usually brings one, and it restores the factory interval.
        """
        if not self._periodic_inform_interval:
            return
        live = any(
            t.type == CONFIGURE_PERIODIC_INFORM
            and t.status in (TASK_PENDING, TASK_IN_PROGRESS)
            for t in self._store.list_tasks(cpe_key)
        )
        if live:
            return
        ms = root + 'ManagementServer.'
        task = self._store.create_task(
            cpe_key, CONFIGURE_PERIODIC_INFORM,
            {'parameter_values': [
                {'name': ms + 'PeriodicInformEnable', 'value': 'true', 'type': 'xsd:boolean'},
                {
                    'name': ms + 'PeriodicInformInterval',
                    'value': str(self._periodic_inform_interval),
                    'type': 'xsd:unsignedInt',
                },
            ]},
            ttl_sec=ROTATION_TTL_SEC,
        )
        logging.info(
            'acsd claimed: %s bootstrapped; periodic Inform every %ds queued (%s)',
            cpe_key, self._periodic_inform_interval, task.task_id,
        )

    def plan(self, task: Task, root: str) -> List[ComplexModelBase]:
        """The requests of an internal task (one of INTERNAL_TYPES)."""
        if task.type == CONFIGURE_PERIODIC_INFORM:
            return tasks.plan(
                dataclasses.replace(task, type=tasks.SET_PARAMETER_VALUES), root,
            )
        return self._plan_rotation(task, root)

    def recorded_values(self, task: Task) -> Dict[str, str]:
        """
        What a finished internal task set that belongs in the CPE's snapshot,
        so its model, online state and next Inform reflect it. Credentials
        never do.
        """
        if task.type != CONFIGURE_PERIODIC_INFORM:
            return {}
        return {p['name']: p['value'] for p in task.args.get('parameter_values', [])}

    def _plan_rotation(self, task: Task, root: str) -> List[ComplexModelBase]:
        """The SetParameterValues of a rotation; [] when already applied."""
        generation = int(task.args.get('generation', 0))
        cred = self._credentials.get(task.cpe_key)
        if cred.generation >= generation:
            return []
        with self._lock:
            held = self._passwords.get(task.cpe_key)
        if held is None or held[0] != generation:
            raise tasks.InvalidTask('rotation password lost (acsd restarted?)')
        if cred.pending_generation != generation or not cred.pending_cr_password:
            raise tasks.InvalidTask('rotation replaced or aborted')
        ms = root + 'ManagementServer.'
        username = task.args['username']
        # One SPV, so the CPE applies both credentials or neither.
        spv = dataclasses.replace(
            task,
            type=tasks.SET_PARAMETER_VALUES,
            args={'parameter_values': [
                {'name': ms + 'Username', 'value': username},
                {'name': ms + 'Password', 'value': held[1]},
                {'name': ms + 'ConnectionRequestUsername', 'value': username},
                {'name': ms + 'ConnectionRequestPassword', 'value': cred.pending_cr_password},
            ]},
        )
        return tasks.plan(spv, root)

    def task_finished(self, task: Task) -> None:
        if task.type == tasks.FACTORY_RESET:
            # The CPE comes back with its factory (bootstrap) credential.
            self.reset(task.cpe_key)
            return
        if task.type != ROTATE_CREDENTIALS:
            return
        generation = int(task.args.get('generation', 0))
        if self._credentials.promote(task.cpe_key, generation):
            logging.info(
                'acsd claimed: %s now on per-CPE credential %s',
                task.cpe_key, task.args.get('username'),
            )
        self._forget(task.cpe_key, generation)

    def task_failed(self, task: Task) -> None:
        """A failed rotation leaves the CPE on the bootstrap credential."""
        if task.type != ROTATE_CREDENTIALS:
            return
        failed = self._store.get_task(task.task_id)
        if failed is not None and failed.status == TASK_PENDING:
            # Requeued for a retry; the pending credential stays.
            return
        generation = int(task.args.get('generation', 0))
        self._credentials.abort_rotation(task.cpe_key, generation)
        self._forget(task.cpe_key, generation)

    def reset(self, cpe_key: str) -> None:
        """Let a CPE bootstrap again, e.g. after a factory reset."""
        self._credentials.reset(cpe_key)
        with self._lock:
            self._passwords.pop(cpe_key, None)

    def _live(self, task_id: str) -> bool:
        task = self._store.get_task(task_id)
        return task is not None and task.status in (TASK_PENDING, TASK_IN_PROGRESS)

    def _forget(self, cpe_key: str, generation: int) -> None:
        with self._lock:
            held = self._passwords.get(cpe_key)
            if held is not None and held[0] == generation:
                del self._passwords[cpe_key]
