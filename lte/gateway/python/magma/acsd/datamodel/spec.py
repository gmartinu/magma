"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Table-driven handlers that map one family of CPEs onto the Model.
"""
import functools
import ipaddress
import math
import re
from dataclasses import dataclass, field
from typing import (
    Any,
    Callable,
    Dict,
    FrozenSet,
    List,
    Mapping,
    Optional,
    Pattern,
    Protocol,
    Sequence,
    Tuple,
)

from magma.acsd.datamodel.model import Field, Model

# DEFAULT_PASSWORD_LENGTH fits the 256-character limit of TR-069 with room
# for CPEs that store less.
DEFAULT_PASSWORD_LENGTH = 32

INSTANCE = '{i}'


@dataclass(frozen=True)
class DeviceInfo:
    """What a CPE says about itself in an Inform; picks its handler."""
    manufacturer: str = ''
    oui: str = ''
    product_class: str = ''
    model_name: str = ''
    software_version: str = ''


@dataclass(frozen=True)
class Quirks:
    """Per-model deviations the ACS core and the normalizer honor."""
    # Cap on names per GetParameterValues; 0 means no cap.
    max_gpv_names: int = 0
    # Length of generated ACS credentials; 0 means DEFAULT_PASSWORD_LENGTH.
    password_length: int = 0
    # Keep the bootstrap credentials on CPEs that reject a new password.
    no_credential_rotation: bool = False
    # Multiplier applied to numeric fields, for modems that report e.g.
    # RSRQ in tenths of a dB.
    scale: Mapping[Field, float] = field(default_factory=dict)

    @property
    def effective_password_length(self) -> int:
        return self.password_length or DEFAULT_PASSWORD_LENGTH


def gpv_batches(paths: Sequence[str], quirks: Quirks) -> List[List[str]]:
    """
    Split refresh paths into GetParameterValues requests: each partial path
    (ending in ".") alone, so a subtree the CPE lacks fails alone; leaf names
    together, at most quirks.max_gpv_names per request.
    """
    batches = [[p] for p in paths if p.endswith('.')]
    leaves = [p for p in paths if not p.endswith('.')]
    size = quirks.max_gpv_names or len(leaves) or 1
    batches.extend(
        leaves[i:i + size] for i in range(0, len(leaves), size)
    )
    return batches


@dataclass(frozen=True)
class FieldPaths:
    """
    The parameter names a field is read from, tried in order. A name may
    contain "{i}", which matches any instance number, lowest first. `guess`
    marks paths not yet confirmed against a real CPE capture.
    """
    field: Field
    candidates: Tuple[str, ...]
    guess: bool = False


def paths(f: Field, *candidates: str, guess: bool = False) -> FieldPaths:
    return FieldPaths(f, tuple(candidates), guess)


class Handler(Protocol):
    name: str
    quirks: Quirks

    def match(self, info: DeviceInfo) -> int:
        """How well the handler fits the device; 0 means not at all."""

    def refresh_paths(self, root: str) -> Sequence[str]:
        """Parameter names or partial paths read to refresh the model."""

    def normalize(self, root: str, params: Mapping[str, str]) -> Model:
        """Map the CPE parameters under `root` onto a Model."""


@dataclass(frozen=True)
class Spec:
    """
    A table-driven Handler. Match criteria left empty match anything. A spec
    with a `base` tries its own candidates for a field first, then the
    base's, so a vendor spec only lists what differs from the standard.
    """
    name: str
    ouis: FrozenSet[str] = frozenset()
    product_classes: FrozenSet[str] = frozenset()
    model_pattern: Optional[Pattern] = None
    priority: int = 0
    refresh: Mapping[str, Tuple[str, ...]] = field(default_factory=dict)
    fields: Mapping[str, Tuple[FieldPaths, ...]] = field(default_factory=dict)
    quirks: Quirks = Quirks()
    base: Optional['Spec'] = None

    def match(self, info: DeviceInfo) -> int:
        score = 1 + self.priority
        if self.ouis:
            if info.oui.upper() not in {o.upper() for o in self.ouis}:
                return 0
            score += 10
        if self.product_classes:
            wanted = {p.casefold() for p in self.product_classes}
            if info.product_class.casefold() not in wanted:
                return 0
            score += 10
        if self.model_pattern is not None:
            if not (
                self.model_pattern.search(info.model_name)
                or self.model_pattern.search(info.product_class)
            ):
                return 0
            score += 10
        return score

    def refresh_paths(self, root: str) -> Sequence[str]:
        if root in self.refresh or self.base is None:
            return self.refresh.get(root, ())
        return self.base.refresh_paths(root)

    def field_paths(self, root: str) -> Tuple[FieldPaths, ...]:
        """This spec's table for root, merged onto its base's."""
        own = self.fields.get(root, ())
        if self.base is None:
            return own
        mine = {fp.field: fp for fp in own}
        merged = []
        for fp in self.base.field_paths(root):
            o = mine.pop(fp.field, None)
            if o is None:
                merged.append(fp)
            else:
                merged.append(FieldPaths(
                    fp.field, o.candidates + fp.candidates, o.guess,
                ))
        return tuple(merged) + tuple(o for o in own if o.field in mine)

    def guessed_fields(self, root: str) -> FrozenSet[Field]:
        return frozenset(
            fp.field for fp in self.field_paths(root) if fp.guess
        )

    def normalize(self, root: str, params: Mapping[str, str]) -> Model:
        model = Model(root=root)
        names: Optional[List[str]] = None
        for fp in self.field_paths(root):
            parse = _PARSERS[fp.field]
            scale = self.quirks.scale.get(fp.field)
            for candidate in fp.candidates:
                if INSTANCE in candidate:
                    if names is None:
                        names = sorted(params, key=_natural_key)
                    pattern = _instance_pattern(candidate)
                    found = [n for n in names if pattern.match(n)]
                else:
                    found = [candidate] if candidate in params else []
                if any(
                    _apply(model, fp.field, parse, params[n], scale)
                    for n in found
                ):
                    break
        return model


def _apply(model, f, parse, raw, scale) -> bool:
    value = parse((raw or '').strip(), model)
    if value is None:
        return False
    if scale is not None:
        value = value * scale
    model.set(f, value)
    return True


@functools.lru_cache(maxsize=None)
def _instance_pattern(path: str) -> Pattern:
    return re.compile(
        '^' + re.escape(path).replace(re.escape(INSTANCE), '[0-9]+') + '$',
    )


def _natural_key(name: str):
    # Interface.2 sorts before Interface.10.
    return [
        (0, int(p), '') if p.isdigit() else (1, 0, p)
        for p in name.split('.')
    ]


Parser = Callable[[str, Model], Any]


def _str(v: str, _: Model) -> Optional[str]:
    return v or None


def _int(v: str, _: Model) -> Optional[int]:
    try:
        return int(v)
    except ValueError:
        return None


def _float(v: str, _: Model) -> Optional[float]:
    try:
        n = float(v)
    except ValueError:
        return None
    return n if math.isfinite(n) else None


def _bool(v: str, _: Model) -> Optional[bool]:
    # xsd:boolean, as TR-069 encodes it.
    return {'1': True, 'true': True, '0': False, 'false': False}.get(
        v.lower(),
    )


def _ip(v6: bool, exclude: Optional[Field] = None) -> Parser:
    """
    A usable address of the family: the CPE lists its loopback and
    link-local interfaces next to the real ones. `exclude` skips the
    address already taken by another field, e.g. the LAN one when the WAN
    address is read from the same IP.Interface table.
    """
    def parse(v: str, model: Model) -> Optional[str]:
        first = v.split(',')[0].strip()
        try:
            a = ipaddress.ip_address(first)
        except ValueError:
            return None
        if (a.version == 6) != v6 or a.is_loopback or a.is_link_local \
                or a.is_unspecified:
            return None
        if exclude is not None and model.get(exclude) == first:
            return None
        return first
    return parse


_PARSERS: Dict[Field, Parser] = {f: _str for f in Field}
_PARSERS.update({
    Field.UPTIME: _int,
    Field.LAN_HOST_COUNT: _int,
    Field.WIFI_CHANNEL: _int,
    Field.MS_PERIODIC_INFORM_INTERVAL: _int,
    Field.RSRP: _float,
    Field.RSRQ: _float,
    Field.SINR: _float,
    Field.RSSI: _float,
    Field.LAN_DHCP_SERVER_ENABLE: _bool,
    Field.WIFI_ENABLE: _bool,
    Field.MS_PERIODIC_INFORM_ENABLE: _bool,
    Field.LAN_IPV4: _ip(False),
    Field.WAN_IPV4: _ip(False, exclude=Field.LAN_IPV4),
    Field.WAN_IPV6: _ip(True),
})
