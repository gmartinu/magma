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

package auth_test

import (
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/auth"
	"magma/orc8r/cloud/go/clock"
)

const realm = "magma-acs"

func TestDigestRFC2617Vector(t *testing.T) {
	// RFC 2617 section 3.5.
	c := auth.ClientChallenge{Scheme: "digest", Params: map[string]string{
		"realm": "testrealm@host.com", "nonce": "dcd98b7102dd2f0e8b11d0f600bfb0c093", "opaque": "5ccc069c403ebaf9f0171e9517f40e41",
	}}
	h := auth.DigestAuthorization(c, "GET", "/dir/index.html", "Mufasa", "Circle Of Life", "00000001", "0a4f113b")
	assert.Contains(t, h, `response="6629fae49393a05397450978507c4ef1"`)
	assert.Contains(t, h, `opaque="5ccc069c403ebaf9f0171e9517f40e41"`)
}

func challenge(t *testing.T, a *auth.Authenticator, secure bool) []auth.ClientChallenge {
	rec := httptest.NewRecorder()
	a.Challenge(rec, secure, false)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	return auth.ParseChallenges(rec.Result().Header.Values("WWW-Authenticate"))
}

func post(uri, authz string) *http.Request {
	r := httptest.NewRequest("POST", uri, nil)
	if authz != "" {
		r.Header.Set("Authorization", authz)
	}
	return r
}

func TestDigestRoundTrip(t *testing.T) {
	clock.SetAndFreezeClock(t, time.Unix(1000, 0))
	defer clock.UnfreezeClock(t)
	a := auth.NewAuthenticator(realm, auth.BasicTLSOnly, []byte("secret"), 5*time.Minute)
	hash := auth.HashPassword("cpe1", realm, "pw")
	assert.True(t, strings.HasPrefix(hash, "md5$"))
	assert.NotContains(t, hash, "pw")

	chs := challenge(t, a, false)
	require.Len(t, chs, 1, "no Basic offered over plain HTTP")
	assert.Equal(t, "digest", chs[0].Scheme)
	assert.Equal(t, realm, chs[0].Params["realm"])
	assert.Equal(t, "auth", chs[0].Params["qop"])

	h := auth.DigestAuthorization(chs[0], "POST", "/acs?x=1", "cpe1", "pw", "00000001", "abc")
	at, err := a.Parse(post("/acs?x=1", h), false)
	require.NoError(t, err)
	assert.Equal(t, "cpe1", at.Username)
	assert.Equal(t, "digest", at.Scheme)
	assert.True(t, at.Verify(hash))
	assert.False(t, at.Verify(auth.HashPassword("cpe1", realm, "other")))
	assert.False(t, at.Verify(""))
	assert.False(t, at.Verify("bcrypt$x"))

	// A nonce from another replica with the same secret is accepted.
	other := auth.NewAuthenticator(realm, auth.BasicTLSOnly, []byte("secret"), 5*time.Minute)
	_, err = other.Parse(post("/acs?x=1", h), false)
	assert.NoError(t, err)
	// With another secret it is not.
	_, err = auth.NewAuthenticator(realm, auth.BasicTLSOnly, []byte("x"), 5*time.Minute).Parse(post("/acs?x=1", h), false)
	assert.Error(t, err)

	_, err = a.Parse(post("/other", h), false)
	errContains(t, err, "uri")
	_, err = a.Parse(post("/acs?x=1", strings.Replace(h, realm, "evil", 1)), false)
	errContains(t, err, "realm")
	_, err = a.Parse(post("/acs?x=1", h+`, algorithm=SHA-256`), false)
	errContains(t, err, "algorithm")
	_, err = a.Parse(post("/acs?x=1", `Digest username="u"`), false)
	assert.Error(t, err)
	_, err = a.Parse(post("/acs?x=1", `Digest username="u", realm="magma-acs", nonce="bogus", uri="/acs?x=1", response="x"`), false)
	errContains(t, err, "not issued")
	_, err = a.Parse(post("/", ""), false)
	assert.ErrorIs(t, err, auth.ErrNoCredentials)
	_, err = a.Parse(post("/", "Bearer x"), false)
	assert.Error(t, err)

	clock.SetAndFreezeClock(t, time.Unix(1000+301, 0))
	_, err = a.Parse(post("/acs?x=1", h), false)
	assert.ErrorIs(t, err, auth.ErrStaleNonce)
	rec := httptest.NewRecorder()
	a.Challenge(rec, false, true)
	assert.Contains(t, rec.Header().Get("WWW-Authenticate"), "stale=true")
}

func TestDigestWithoutQop(t *testing.T) {
	// RFC 2069 clients answer without qop: response = MD5(HA1:nonce:HA2).
	a := auth.NewAuthenticator(realm, auth.BasicNever, []byte("s"), time.Minute)
	nonce := challenge(t, a, true)[0].Params["nonce"]
	ha1 := strings.TrimPrefix(auth.HashPassword("u", realm, "p"), "md5$")
	resp := md5hexT(ha1 + ":" + nonce + ":" + md5hexT("POST:/"))
	at, err := a.Parse(post("/", `Digest username="u", realm="magma-acs", nonce="`+nonce+`", uri="/", response="`+resp+`"`), true)
	require.NoError(t, err)
	assert.True(t, at.Verify(auth.HashPassword("u", realm, "p")))
}

func TestBasicPolicy(t *testing.T) {
	hash := auth.HashPassword("cpe1", realm, "p:w")
	basic := auth.BasicAuthorization("cpe1", "p:w")
	for _, tc := range []struct {
		policy          auth.BasicPolicy
		secure, allowed bool
	}{
		{auth.BasicNever, true, false},
		{auth.BasicNever, false, false},
		{auth.BasicTLSOnly, true, true},
		{auth.BasicTLSOnly, false, false},
		{auth.BasicAlways, false, true},
	} {
		a := auth.NewAuthenticator(realm, tc.policy, []byte("s"), time.Minute)
		assert.Equal(t, tc.allowed, a.BasicAllowed(tc.secure))
		chs := challenge(t, a, tc.secure)
		if tc.allowed {
			require.Len(t, chs, 2)
			assert.Equal(t, "basic", chs[1].Scheme)
		} else {
			assert.Len(t, chs, 1)
		}
		at, err := a.Parse(post("/", basic), tc.secure)
		if !tc.allowed {
			assert.ErrorIs(t, err, auth.ErrBasicNotAllowed)
			continue
		}
		require.NoError(t, err)
		assert.Equal(t, "basic", at.Scheme)
		assert.True(t, at.Verify(hash))
		assert.False(t, at.Verify(auth.HashPassword("cpe1", realm, "p")))
	}
	a := auth.NewAuthenticator(realm, auth.BasicAlways, []byte("s"), time.Minute)
	_, err := a.Parse(post("/", "Basic !!!"), false)
	assert.Error(t, err)
	_, err = a.Parse(post("/", "Basic dXNlcg=="), false)
	assert.Error(t, err)

	for in, want := range map[string]auth.BasicPolicy{"": auth.BasicTLSOnly, "NEVER": auth.BasicNever, "always": auth.BasicAlways} {
		p, err := auth.ParseBasicPolicy(in)
		assert.NoError(t, err)
		assert.Equal(t, want, p)
	}
	_, err = auth.ParseBasicPolicy("sometimes")
	assert.Error(t, err)
}

func md5hexT(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func errContains(t *testing.T, err error, s string) {
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), s)
	}
}
