/*
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package auth implements the HTTP Digest (RFC 2617) and Basic
// authentication of CPEs to the ACS.
package auth

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"magma/orc8r/cloud/go/clock"
)

// BasicPolicy says when a CPE may authenticate with Basic, which sends the
// password in clear.
type BasicPolicy string

const (
	// BasicNever only accepts Digest.
	BasicNever BasicPolicy = "never"
	// BasicTLSOnly accepts Basic only on requests that reached the ACS over
	// TLS. This is the default.
	BasicTLSOnly BasicPolicy = "tls_only"
	// BasicAlways also accepts Basic over plain HTTP. Lab use only.
	BasicAlways BasicPolicy = "always"
)

// ParseBasicPolicy parses a policy name; "" is BasicTLSOnly.
func ParseBasicPolicy(s string) (BasicPolicy, error) {
	switch p := BasicPolicy(strings.ToLower(s)); p {
	case "":
		return BasicTLSOnly, nil
	case BasicNever, BasicTLSOnly, BasicAlways:
		return p, nil
	}
	return "", fmt.Errorf("unknown basic auth policy %q", s)
}

const hashPrefix = "md5$"

// HashPassword returns what the ACS stores for a password: the Digest HA1,
// MD5(username:realm:password). It is enough to verify both Digest and Basic
// without keeping the password, but it is bound to the realm, so changing the
// realm invalidates every stored hash.
func HashPassword(username, realm, password string) string {
	return hashPrefix + md5hex(username+":"+realm+":"+password)
}

var (
	// ErrNoCredentials means the request carries no Authorization header.
	ErrNoCredentials = errors.New("no credentials")
	// ErrStaleNonce means a Digest nonce was issued by the ACS but expired;
	// the CPE is told to retry with a fresh one.
	ErrStaleNonce = errors.New("stale nonce")
	// ErrBasicNotAllowed means Basic was used where the policy forbids it.
	ErrBasicNotAllowed = errors.New("basic auth not allowed on this request")
)

// Authenticator parses and checks CPE credentials. It keeps no per-request
// state: Digest nonces are MACed timestamps, so any replica sharing the secret
// can verify a nonce another one issued.
type Authenticator struct {
	realm    string
	basic    BasicPolicy
	secret   []byte
	nonceTTL time.Duration
}

func NewAuthenticator(realm string, basic BasicPolicy, secret []byte, nonceTTL time.Duration) *Authenticator {
	return &Authenticator{realm: realm, basic: basic, secret: secret, nonceTTL: nonceTTL}
}

func (a *Authenticator) Realm() string { return a.realm }

// BasicAllowed reports whether the policy lets a request use Basic.
func (a *Authenticator) BasicAllowed(secure bool) bool {
	return a.basic == BasicAlways || (a.basic == BasicTLSOnly && secure)
}

// Attempt is a parsed Authorization header, checked against a stored hash
// with Verify.
type Attempt struct {
	Username string
	// Scheme is "digest" or "basic".
	Scheme   string
	realm    string
	password string
	digest   map[string]string
	method   string
}

// Parse reads the Authorization header of a request. secure says whether the
// request reached the ACS over TLS.
func (a *Authenticator) Parse(r *http.Request, secure bool) (*Attempt, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return nil, ErrNoCredentials
	}
	scheme, rest, _ := strings.Cut(h, " ")
	switch strings.ToLower(scheme) {
	case "basic":
		if !a.BasicAllowed(secure) {
			return nil, ErrBasicNotAllowed
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rest))
		if err != nil {
			return nil, fmt.Errorf("bad basic credentials: %w", err)
		}
		user, pass, ok := strings.Cut(string(raw), ":")
		if !ok {
			return nil, errors.New("bad basic credentials")
		}
		return &Attempt{Username: user, Scheme: "basic", realm: a.realm, password: pass}, nil
	case "digest":
		p := parseDigest(rest)
		if p["username"] == "" || p["nonce"] == "" || p["response"] == "" {
			return nil, errors.New("incomplete digest credentials")
		}
		if p["realm"] != a.realm {
			return nil, fmt.Errorf("digest realm %q is not %q", p["realm"], a.realm)
		}
		if alg := p["algorithm"]; alg != "" && !strings.EqualFold(alg, "MD5") {
			return nil, fmt.Errorf("unsupported digest algorithm %q", alg)
		}
		if qop := p["qop"]; qop != "" && qop != "auth" {
			return nil, fmt.Errorf("unsupported digest qop %q", qop)
		}
		if p["uri"] != r.RequestURI && p["uri"] != r.URL.RequestURI() {
			return nil, fmt.Errorf("digest uri %q does not match the request", p["uri"])
		}
		if err := a.checkNonce(p["nonce"]); err != nil {
			return nil, err
		}
		return &Attempt{Username: p["username"], Scheme: "digest", realm: a.realm, digest: p, method: r.Method}, nil
	}
	return nil, fmt.Errorf("unsupported auth scheme %q", scheme)
}

// Verify checks the attempt against a hash from HashPassword.
func (at *Attempt) Verify(hash string) bool {
	ha1, ok := strings.CutPrefix(hash, hashPrefix)
	if !ok || ha1 == "" {
		return false
	}
	var expected, got string
	switch at.Scheme {
	case "basic":
		expected, got = ha1, md5hex(at.Username+":"+at.realm+":"+at.password)
	case "digest":
		p := at.digest
		ha2 := md5hex(at.method + ":" + p["uri"])
		if p["qop"] == "" {
			expected = md5hex(ha1 + ":" + p["nonce"] + ":" + ha2)
		} else {
			expected = md5hex(strings.Join([]string{ha1, p["nonce"], p["nc"], p["cnonce"], p["qop"], ha2}, ":"))
		}
		got = strings.ToLower(p["response"])
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(got)) == 1
}

// Challenge writes a 401 asking for Digest, and for Basic too when the policy
// allows it on this request.
func (a *Authenticator) Challenge(w http.ResponseWriter, secure, stale bool) {
	digest := fmt.Sprintf(`Digest realm="%s", qop="auth", nonce="%s", algorithm=MD5`, a.realm, a.newNonce())
	if stale {
		digest += ", stale=true"
	}
	w.Header().Add("WWW-Authenticate", digest)
	if a.BasicAllowed(secure) {
		w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Basic realm="%s"`, a.realm))
	}
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusUnauthorized)
}

func (a *Authenticator) newNonce() string {
	ts := make([]byte, 8)
	binary.BigEndian.PutUint64(ts, uint64(clock.Now().Unix()))
	return base64.RawURLEncoding.EncodeToString(append(ts, a.mac(ts)...))
}

func (a *Authenticator) checkNonce(nonce string) error {
	raw, err := base64.RawURLEncoding.DecodeString(nonce)
	if err != nil || len(raw) != 8+16 {
		return errors.New("digest nonce was not issued by the ACS")
	}
	ts, mac := raw[:8], raw[8:]
	if !hmac.Equal(mac, a.mac(ts)) {
		return errors.New("digest nonce was not issued by the ACS")
	}
	issued := time.Unix(int64(binary.BigEndian.Uint64(ts)), 0)
	if clock.Now().Sub(issued) > a.nonceTTL {
		return ErrStaleNonce
	}
	return nil
}

func (a *Authenticator) mac(ts []byte) []byte {
	m := hmac.New(sha256.New, a.secret)
	m.Write(ts)
	return m.Sum(nil)[:16]
}

// parseDigest splits the comma-separated key=value pairs of a Digest header,
// where values may be quoted and quoted values may contain commas.
func parseDigest(s string) map[string]string {
	out := map[string]string{}
	for len(s) > 0 {
		s = strings.TrimLeft(s, " \t,")
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		key := strings.ToLower(strings.TrimSpace(s[:eq]))
		s = strings.TrimLeft(s[eq+1:], " \t")
		var val string
		if strings.HasPrefix(s, `"`) {
			end := 1
			for end < len(s) && s[end] != '"' {
				if s[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(s) {
				val, s = s[1:], ""
			} else {
				val, s = s[1:end], s[end+1:]
			}
			val = strings.ReplaceAll(val, `\"`, `"`)
		} else {
			comma := strings.IndexByte(s, ',')
			if comma < 0 {
				val, s = s, ""
			} else {
				val, s = s[:comma], s[comma:]
			}
			val = strings.TrimSpace(val)
		}
		out[key] = val
	}
	return out
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}
