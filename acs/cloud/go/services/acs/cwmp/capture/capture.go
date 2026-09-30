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

// Package capture loads CWMP sessions recorded by the ACS lab logging proxy,
// laid out as <root>/<device>/<session>/NNN-{request,response}.xml with
// optional NNN-{request,response}.headers.json next to them.
package capture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

// Exchange is one HTTP request of a CWMP session and the response to it.
type Exchange struct {
	Seq      int
	Method   string
	URL      string
	Request  []byte
	Response []byte
	// Status is the HTTP status of the response, 0 when no headers were
	// captured.
	Status int
	// RequestHeaders are the captured request headers, with the lower-cased
	// names the proxy writes.
	RequestHeaders map[string]string
}

// Session is one captured CWMP session of a device.
type Session struct {
	Device    string
	ID        string
	Exchanges []Exchange
}

var fileRe = regexp.MustCompile(`^(\d+)-(request|response)(\.headers\.json|\.xml)$`)

// LoadDir loads every session under root. A missing root yields no sessions.
func LoadDir(root string) ([]Session, error) {
	devices, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sessions []Session
	for _, dev := range devices {
		if !dev.IsDir() {
			continue
		}
		sessDirs, err := os.ReadDir(filepath.Join(root, dev.Name()))
		if err != nil {
			return nil, err
		}
		for _, sd := range sessDirs {
			if !sd.IsDir() {
				continue
			}
			s, err := loadSession(filepath.Join(root, dev.Name(), sd.Name()))
			if err != nil {
				return nil, err
			}
			s.Device, s.ID = dev.Name(), sd.Name()
			sessions = append(sessions, s)
		}
	}
	return sessions, nil
}

func loadSession(dir string) (Session, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return Session{}, err
	}
	bySeq := map[int]*Exchange{}
	for _, f := range files {
		m := fileRe.FindStringSubmatch(f.Name())
		if m == nil {
			continue
		}
		seq, _ := strconv.Atoi(m[1])
		ex := bySeq[seq]
		if ex == nil {
			ex = &Exchange{Seq: seq, Method: "POST"}
			bySeq[seq] = ex
		}
		data, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			return Session{}, err
		}
		switch m[2] + m[3] {
		case "request.xml":
			ex.Request = data
		case "response.xml":
			ex.Response = data
		case "request.headers.json":
			var h struct {
				Method  string            `json:"method"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
			}
			if err := json.Unmarshal(data, &h); err != nil {
				return Session{}, fmt.Errorf("%s: %w", f.Name(), err)
			}
			if h.Method != "" {
				ex.Method = h.Method
			}
			ex.URL, ex.RequestHeaders = h.URL, h.Headers
		case "response.headers.json":
			var h struct {
				Status int `json:"status"`
			}
			if err := json.Unmarshal(data, &h); err != nil {
				return Session{}, fmt.Errorf("%s: %w", f.Name(), err)
			}
			ex.Status = h.Status
		}
	}
	s := Session{}
	for _, ex := range bySeq {
		s.Exchanges = append(s.Exchanges, *ex)
	}
	sort.Slice(s.Exchanges, func(i, j int) bool { return s.Exchanges[i].Seq < s.Exchanges[j].Seq })
	return s, nil
}

// IsCWMP reports whether the exchange is a CWMP POST that the upstream ACS
// processed, as opposed to a probe (GET, health check) or an auth challenge
// whose request is resent right after.
func (e Exchange) IsCWMP() bool {
	return e.Method == "POST" && e.Status != 401 && e.Status != 405
}
