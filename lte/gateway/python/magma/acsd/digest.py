"""
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

HTTP Digest authentication (RFC 7616, RFC 2617) of CPEs to acsd.

Digest is a check on top of the CPE identity, not a replacement for it: a
request must carry valid credentials here, and its source IP must still
resolve to a subscriber in the session handler.
"""

import hashlib
import hmac
import logging
import secrets
import threading
import time
from collections import OrderedDict
from typing import Callable, Dict, List, NamedTuple, Optional, Protocol
from urllib.parse import urlsplit

# WSGI environ key holding a dict that lives as long as the TCP connection.
# The listener sets it; Digest marks it once a request on it authenticates.
CONNECTION_STATE = 'acsd.connection'
# WSGI environ key set to the authenticated Digest username.
DIGEST_USERNAME = 'acsd.digest_username'

HTTP_401 = '401 Unauthorized'

_HASHES: Dict[str, Callable] = {
    'MD5': hashlib.md5,
    'SHA-256': hashlib.sha256,
}


class Ha1(str):
    """
    A stored MD5 HA1, md5(username:realm:password), returned by a
    CredentialProvider in place of the password so acsd never has to keep
    per-CPE passwords. It is password-equivalent for Digest, so it is a
    secret all the same.
    """


def ha1_of(username: str, realm: str, password: str) -> Ha1:
    a1 = '%s:%s:%s' % (username, realm, password)
    return Ha1(hashlib.md5(a1.encode()).hexdigest())


class CredentialProvider(Protocol):
    """Where acsd finds the password a CPE should authenticate with."""

    def lookup(self, username: str, source_ip: str) -> Optional[str]:
        """
        The password (or its Ha1) of `username` for the CPE at
        `source_ip`, or None to refuse it. Per-CPE providers can resolve
        the source IP to the IMSI and accept only the username bound to it.
        """


class StaticCredentialProvider:
    """One bootstrap username and password shared by every CPE."""

    def __init__(self, username: str, password: str):
        self._username = username
        self._password = password

    def lookup(self, username: str, source_ip: str) -> Optional[str]:
        if not self._username or not self._password:
            return None
        if not hmac.compare_digest(username.encode(), self._username.encode()):
            return None
        return self._password


class _Nonce:
    __slots__ = ('issued', 'last_nc')

    def __init__(self, issued: float):
        self.issued = issued
        self.last_nc = 0


class NonceStore:
    """
    Nonces issued by this process, with the highest nonce-count seen on
    each, so a captured request cannot be replayed. acsd is a single process
    per gateway, so memory is enough; after a restart CPEs are told their
    nonce is stale and retry with a new one.
    """

    def __init__(
        self,
        ttl: float,
        max_nonces: int = 4096,
        clock: Callable[[], float] = time.monotonic,
    ):
        self._ttl = ttl
        self._max = max_nonces
        self._clock = clock
        self._lock = threading.Lock()
        self._nonces: 'OrderedDict[str, _Nonce]' = OrderedDict()

    def issue(self) -> str:
        nonce = secrets.token_urlsafe(24)
        with self._lock:
            self._nonces[nonce] = _Nonce(self._clock())
            while len(self._nonces) > self._max:
                self._nonces.popitem(last=False)
        return nonce

    def use(self, nonce: str, nc: Optional[int]) -> Optional[str]:
        """
        Record a use of `nonce` with nonce-count `nc` (None without qop).
        Returns None when the use is fresh, or why it is not.
        """
        with self._lock:
            entry = self._nonces.get(nonce)
            if entry is None:
                return 'unknown nonce'
            if self._clock() - entry.issued > self._ttl:
                del self._nonces[nonce]
                return 'expired nonce'
            if nc is None:
                # Without qop there is no nonce-count, so a nonce is good
                # for one request only.
                del self._nonces[nonce]
                return None
            if nc <= entry.last_nc:
                return 'replayed nonce-count'
            entry.last_nc = nc
            return None


class AuthResult(NamedTuple):
    username: Optional[str]
    # Why the request was refused; None when it authenticated.
    reason: Optional[str] = None
    # The credentials were right but the nonce was not usable: the CPE
    # should retry with the new nonce without asking for a password.
    stale: bool = False

    @property
    def ok(self) -> bool:
        return self.reason is None


class DigestAuthenticator:
    """Checks Digest credentials and builds the challenges for CPEs."""

    def __init__(
        self,
        realm: str,
        credentials: CredentialProvider,
        nonces: NonceStore,
    ):
        self.realm = realm
        self._credentials = credentials
        self._nonces = nonces

    def challenge(self, stale: bool = False) -> str:
        value = 'Digest realm="%s", qop="auth", nonce="%s", algorithm=MD5' % (
            self.realm, self._nonces.issue(),
        )
        return value + ', stale=true' if stale else value

    def authenticate(
        self,
        method: str,
        request_uri: str,
        header: Optional[str],
        source_ip: str,
    ) -> AuthResult:
        if not header:
            return AuthResult(None, 'no credentials')
        scheme, _, rest = header.strip().partition(' ')
        if scheme.lower() != 'digest':
            return AuthResult(None, 'unsupported scheme %r' % scheme[:16])
        p = parse_digest(rest)
        username = p.get('username', '')
        error = self._check_params(p, request_uri)
        if error:
            return AuthResult(username or None, error)
        password = self._credentials.lookup(username, source_ip)
        if password is None:
            return AuthResult(username, 'unknown username')
        algorithm = p.get('algorithm') or 'MD5'
        if isinstance(password, Ha1) and algorithm.upper() != 'MD5':
            return AuthResult(username, 'stored credential needs MD5')
        expected = digest_response(
            algorithm, username, self.realm, password,
            method, p['uri'], p['nonce'], p.get('nc'), p.get('cnonce'),
            p.get('qop'),
        )
        if not hmac.compare_digest(expected, p['response'].lower()):
            return AuthResult(username, 'wrong response')
        nc = int(p['nc'], 16) if p.get('qop') else None
        why = self._nonces.use(p['nonce'], nc)
        if why:
            return AuthResult(username, why, stale=True)
        return AuthResult(username)

    def _check_params(self, p: Dict[str, str], request_uri: str) -> Optional[str]:
        for key in ('username', 'realm', 'nonce', 'uri', 'response'):
            if not p.get(key):
                return 'missing %s' % key
        if p['realm'] != self.realm:
            return 'wrong realm'
        if (p.get('algorithm') or 'MD5').upper() not in _HASHES:
            return 'unsupported algorithm'
        qop = p.get('qop')
        if qop is not None:
            if qop != 'auth':
                return 'unsupported qop'
            if not p.get('cnonce') or not _is_nc(p.get('nc', '')):
                return 'missing cnonce or nc'
        if not _same_uri(p['uri'], request_uri):
            return 'uri does not match the request'
        return None


def digest_response(
    algorithm: str,
    username: str,
    realm: str,
    password: str,
    method: str,
    uri: str,
    nonce: str,
    nc: Optional[str] = None,
    cnonce: Optional[str] = None,
    qop: Optional[str] = None,
) -> str:
    """
    The Digest `response` value a client sends (RFC 7616 section 3.4.1).
    `password` may be an Ha1, with MD5.
    """
    new = _HASHES[algorithm.upper()]

    def h(s: str) -> str:
        return new(s.encode()).hexdigest()

    if isinstance(password, Ha1):
        ha1 = str(password)
    else:
        ha1 = h('%s:%s:%s' % (username, realm, password))
    ha2 = h('%s:%s' % (method, uri))
    if qop:
        return h(':'.join((ha1, nonce, nc or '', cnonce or '', qop, ha2)))
    return h('%s:%s:%s' % (ha1, nonce, ha2))


def parse_digest(s: str) -> Dict[str, str]:
    """
    Split the comma-separated key=value pairs of a Digest header, where
    values may be quoted and quoted values may contain commas.
    """
    out: Dict[str, str] = {}
    i, n = 0, len(s)
    while i < n:
        while i < n and s[i] in ' \t,':
            i += 1
        eq = s.find('=', i)
        if eq < 0:
            break
        key = s[i:eq].strip().lower()
        i = eq + 1
        while i < n and s[i] in ' \t':
            i += 1
        if i < n and s[i] == '"':
            i += 1
            chars: List[str] = []
            while i < n and s[i] != '"':
                if s[i] == '\\' and i + 1 < n:
                    i += 1
                chars.append(s[i])
                i += 1
            i += 1
            value = ''.join(chars)
        else:
            comma = s.find(',', i)
            end = n if comma < 0 else comma
            value, i = s[i:end].strip(), end
        out[key] = value
    return out


def _is_nc(nc: str) -> bool:
    return len(nc) == 8 and all(c in '0123456789abcdefABCDEF' for c in nc)


def _same_uri(digest_uri: str, request_uri: str) -> bool:
    # Some clients send the absolute URL; only path and query must match.
    a, b = urlsplit(digest_uri), urlsplit(request_uri)
    return (a.path or '/', a.query) == (b.path or '/', b.query)


def request_uri_of(environ: dict) -> str:
    path = environ.get('SCRIPT_NAME', '') + environ.get('PATH_INFO', '')
    query = environ.get('QUERY_STRING')
    return (path or '/') + ('?' + query if query else '')


class DigestAuthMiddleware:
    """
    WSGI middleware refusing requests without valid Digest credentials,
    before any SOAP parsing. A TCP connection that authenticated once stays
    authenticated: TR-069 CPEs usually only send credentials on the Inform
    of a session and keep the connection for the rest of it.
    """

    def __init__(self, app: Callable, authenticator: DigestAuthenticator):
        self._app = app
        self._auth = authenticator

    def __call__(self, environ: dict, start_response: Callable):
        conn = environ.get(CONNECTION_STATE)
        header = environ.get('HTTP_AUTHORIZATION')
        if not header and conn and conn.get(DIGEST_USERNAME):
            environ[DIGEST_USERNAME] = conn[DIGEST_USERNAME]
            return self._app(environ, start_response)

        source_ip = environ.get('REMOTE_ADDR', '')
        result = self._auth.authenticate(
            environ.get('REQUEST_METHOD', ''), request_uri_of(environ),
            header, source_ip,
        )
        if result.ok:
            if conn is not None:
                conn[DIGEST_USERNAME] = result.username
            environ[DIGEST_USERNAME] = result.username
            return self._app(environ, start_response)

        if conn is not None:
            conn.pop(DIGEST_USERNAME, None)
        _log_refusal(source_ip, result)
        _drain_body(environ)
        start_response(HTTP_401, [
            ('WWW-Authenticate', self._auth.challenge(stale=result.stale)),
            ('Content-Length', '0'),
        ])
        return [b'']


def _log_refusal(source_ip: str, result: AuthResult) -> None:
    if result.reason == 'no credentials':
        # The first request of every session; only a CPE that never
        # follows up with credentials is a problem.
        logging.info('Digest challenge sent to CPE %s', source_ip)
    elif result.stale:
        logging.info(
            'CPE %s (user %r): %s; asking it to retry',
            source_ip, result.username, result.reason,
        )
    else:
        logging.warning(
            'Refusing CWMP request from %s (user %r): %s',
            source_ip, result.username, result.reason,
        )


def _drain_body(environ: dict) -> None:
    # Unread body bytes would be parsed as the next request on the
    # keep-alive connection the CPE re-POSTs on.
    length = int(environ.get('CONTENT_LENGTH') or 0)
    if length > 0:
        environ['wsgi.input'].read(length)
