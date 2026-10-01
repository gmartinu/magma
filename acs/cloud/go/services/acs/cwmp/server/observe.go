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

package server

import (
	"strconv"
	"time"

	"github.com/golang/glog"

	"magma/acs/cloud/go/services/acs/cwmp"
	"magma/acs/cloud/go/services/acs/datamodel"
	"magma/acs/cloud/go/services/acs/kpi"
	"magma/acs/cloud/go/services/acs/sessionlog"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/orc8r/cloud/go/clock"
)

// Option sets an optional collaborator of the server.
type Option func(*Server)

// WithKPIReporter pushes the KPIs of claimed CPEs on every Inform and
// refresh.
func WithKPIReporter(r *kpi.Reporter) Option {
	return func(s *Server) { s.kpis = r }
}

// WithSessionLog emits a session log record per CWMP exchange.
func WithSessionLog(sink sessionlog.Sink) Option {
	return func(s *Server) { s.logs = sink }
}

// network returns the network of the device of the request, looked up once
// per request. Records of devices that cannot be looked up go out without
// one rather than failing the exchange.
func (s *Server) network(req *request, deviceID string) string {
	if req.networkKnown || deviceID == "" {
		return req.networkID
	}
	req.networkKnown = true
	d, err := s.store.GetDevice(deviceID)
	if err != nil {
		glog.Warningf("cwmp %s: look up network of %s: %s", req.client, deviceID, err)
		return ""
	}
	if d != nil {
		req.networkID = d.NetworkID
	}
	return req.networkID
}

// record returns a log record of the request, for the device it is about.
func (s *Server) record(req *request, event, deviceID string) *sessionlog.Record {
	return &sessionlog.Record{
		TimestampMs: clock.Now().UnixMilli(),
		Event:       event,
		DeviceID:    deviceID,
		NetworkID:   s.network(req, deviceID),
		SourceIP:    req.client,
		DurationMs:  time.Since(req.start).Milliseconds(),
	}
}

func (s *Server) emit(r *sessionlog.Record) {
	if s.logs != nil {
		s.logs.Emit(r)
	}
}

func (s *Server) authFailed(req *request, deviceID, reason string) {
	kpi.AuthFailures.Inc()
	if s.logs == nil {
		return
	}
	r := s.record(req, sessionlog.EventAuthFailure, deviceID)
	r.Reason = reason
	s.emit(r)
}

// cpeFault counts a fault the CPE sent, by its code.
func cpeFault(f *cwmp.Fault) {
	kpi.Faults.WithLabelValues(strconv.FormatUint(uint64(f.Code()), 10)).Inc()
}

// informObserved records an accepted Inform and pushes what it reported.
func (s *Server) informObserved(req *request, sess *storage.Session, inform *cwmp.Inform, reported *datamodel.Model) {
	if s.logs != nil {
		r := s.record(req, sessionlog.EventInform, sess.DeviceID)
		r.SessionID, r.Handler, r.Bootstrap = sess.SessionID, sess.Handler, sess.Bootstrap
		for _, e := range inform.Event {
			r.EventCodes = append(r.EventCodes, e.EventCode)
		}
		s.emit(r)
	}

	// Every accepted Inform is counted, claimed or not, so the total of a
	// device claimed later covers its whole history.
	metrics := kpi.Samples(sess.DeviceID, reported)
	metrics = append(metrics, kpi.Sample(kpi.Online, sess.DeviceID, 1))
	total, err := s.store.IncrementInformTotal(sess.DeviceID)
	if err != nil {
		glog.Warningf("cwmp %s: count inform of %s: %s", req.client, sess.DeviceID, err)
	} else {
		metrics = append(metrics, kpi.Sample(kpi.InformTotal, sess.DeviceID, float64(total)))
	}
	s.kpis.Report(req.networkID, metrics)
}

// rpcObserved records one RPC of a session.
func (s *Server) rpcObserved(req *request, sess *storage.Session, rpc string, fault *cwmp.Fault, taskID, taskType string) {
	if s.logs == nil {
		return
	}
	r := s.record(req, sessionlog.EventRPC, sess.DeviceID)
	r.SessionID, r.RPC, r.TaskID, r.TaskType = sess.SessionID, rpc, taskID, taskType
	if fault != nil {
		r.FaultCode, r.FaultString = int(fault.Code()), fault.String()
	}
	s.emit(r)
}

func (s *Server) sessionEnded(req *request, sess *storage.Session) {
	if s.logs == nil {
		return
	}
	r := s.record(req, sessionlog.EventSessionEnd, sess.DeviceID)
	r.SessionID = sess.SessionID
	r.SessionDurationSec = clock.Now().Unix() - sess.CreatedSec
	r.RPCCount = sess.RequestSeq
	s.emit(r)
}
