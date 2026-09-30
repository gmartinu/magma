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

package server_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/cwmp"
	"magma/acs/cloud/go/services/acs/cwmp/server"
	"magma/acs/cloud/go/services/acs/cwmp/simulator"
	"magma/acs/cloud/go/services/acs/datamodel"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/acs/cloud/go/services/acs/tasks"
	"magma/orc8r/cloud/go/sqorc"
)

const (
	bootstrapUser = "bootstrap"
	bootstrapPass = "bootstrap-secret"
)

var simID = cwmp.DeviceIDStruct{Manufacturer: "Global Telecom (sim)", OUI: "00A1B2", ProductClass: "SIM4000", SerialNumber: "SIM0001"}

// harness is an ACS of one or more replicas sharing one database, each an
// independent Server, behind a round-robin balancer.
type harness struct {
	t        *testing.T
	store    storage.ACSStorage
	replicas []*server.Server
	ts       *httptest.Server
	served   []int64
}

func defaultConfig() server.Config {
	return server.Config{
		BootstrapUsername: bootstrapUser,
		BootstrapPassword: bootstrapPass,
		RotateCredentials: true,
		SessionTimeout:    time.Minute,
	}
}

func openSQLite(t *testing.T) *sql.DB {
	db, err := sqorc.Open("sqlite3", ":memory:?_foreign_keys=1")
	require.NoError(t, err)
	// Every connection to :memory: is its own database.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func newHarness(t *testing.T, cfg server.Config, replicas int) *harness {
	return newHarnessOn(t, openSQLite(t), cfg, replicas)
}

func newHarnessOn(t *testing.T, db *sql.DB, cfg server.Config, replicas int) *harness {
	store := storage.NewSQLACSStorage(db, sqorc.GetSqlBuilder())
	require.NoError(t, store.Init())
	h := &harness{t: t, store: store, served: make([]int64, replicas)}
	for i := 0; i < replicas; i++ {
		srv, err := server.New(cfg, store, datamodel.NewRegistry())
		require.NoError(t, err)
		h.replicas = append(h.replicas, srv)
	}
	var next int64
	h.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt64(&next, 1)-1) % replicas
		atomic.AddInt64(&h.served[i], 1)
		h.replicas[i].ServeHTTP(w, r)
	}))
	t.Cleanup(h.ts.Close)
	return h
}

func (h *harness) cpe(root string) *simulator.CPE {
	return simulator.New(h.ts.URL+"/acs", root, simID, bootstrapUser, bootstrapPass)
}

func (h *harness) deviceID() string {
	return simID.DeviceID()
}

func (h *harness) queue(typ string, args tasks.Args, maxAttempts int) string {
	return h.queueFor(h.deviceID(), typ, args, maxAttempts)
}

var taskSeq int64

func (h *harness) queueFor(deviceID, typ string, args tasks.Args, maxAttempts int) string {
	b, err := json.Marshal(args)
	require.NoError(h.t, err)
	id := fmt.Sprintf("task-%d", atomic.AddInt64(&taskSeq, 1))
	require.NoError(h.t, h.store.CreateTask(&storage.Task{
		TaskID: id, DeviceID: deviceID, Type: typ, Args: string(b), Status: storage.TaskPending, MaxAttempts: maxAttempts,
	}))
	return id
}

func (h *harness) task(id string) *storage.Task {
	task, err := h.store.GetTask(id)
	require.NoError(h.t, err)
	require.NotNil(h.t, task)
	return task
}

func (h *harness) model() *datamodel.Model {
	st, err := h.store.GetDeviceState(h.deviceID())
	require.NoError(h.t, err)
	require.NotNil(h.t, st)
	m := &datamodel.Model{}
	require.NoError(h.t, json.Unmarshal([]byte(st.Model), m))
	return m
}

// runSession runs a session that must end normally.
func runSession(t *testing.T, c *simulator.CPE, events ...string) *simulator.Session {
	s, err := c.RunSession(events...)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, s.Status)
	return s
}

// methods lists the method names of the ACS requests of a session.
func methods(s *simulator.Session) []string {
	out := []string{}
	for _, m := range s.Requests {
		out = append(out, m.Method())
	}
	return out
}

func informBody(t *testing.T) []byte {
	body, err := cwmp.Encode(&cwmp.Envelope{ID: "1", Body: &cwmp.Inform{
		DeviceId: simID,
		Event:    cwmp.EventList{{EventCode: cwmp.EventBootstrap}},
	}})
	require.NoError(t, err)
	return body
}

func rawPost(t *testing.T, h *harness, body []byte, headers map[string]string) *http.Response {
	req, err := http.NewRequest(http.MethodPost, h.ts.URL+"/acs", bytes.NewReader(body))
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// decodeResp decodes a CWMP response, or returns nil for a 204.
func decodeResp(t *testing.T, resp *http.Response) *cwmp.Envelope {
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	env, err := cwmp.Decode(body)
	require.NoError(t, err)
	return env
}

func rawCWMP(t *testing.T, h *harness, msg cwmp.Message, headers map[string]string) *cwmp.Envelope {
	body, err := cwmp.Encode(&cwmp.Envelope{ID: "cpe-1", Body: msg})
	require.NoError(t, err)
	return decodeResp(t, rawPost(t, h, body, headers))
}
