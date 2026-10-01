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

package auth

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// ClientChallenge is a parsed WWW-Authenticate header, as a CPE sees it.
type ClientChallenge struct {
	Scheme string
	Params map[string]string
}

// ParseChallenges parses the WWW-Authenticate headers of a 401.
func ParseChallenges(headers []string) []ClientChallenge {
	var out []ClientChallenge
	for _, h := range headers {
		scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
		out = append(out, ClientChallenge{Scheme: strings.ToLower(scheme), Params: parseDigest(rest)})
	}
	return out
}

// DigestAuthorization computes the Authorization header a client sends in
// answer to a Digest challenge with qop=auth.
func DigestAuthorization(c ClientChallenge, method, uri, username, password, nc, cnonce string) string {
	realm, nonce := c.Params["realm"], c.Params["nonce"]
	ha1 := md5hex(username + ":" + realm + ":" + password)
	ha2 := md5hex(method + ":" + uri)
	resp := md5hex(strings.Join([]string{ha1, nonce, nc, cnonce, "auth", ha2}, ":"))
	h := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", qop=auth, nc=%s, cnonce="%s", response="%s"`,
		username, realm, nonce, uri, nc, cnonce, resp)
	if opaque, ok := c.Params["opaque"]; ok {
		h += fmt.Sprintf(`, opaque="%s"`, opaque)
	}
	return h
}

// BasicAuthorization returns a Basic Authorization header.
func BasicAuthorization(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}
