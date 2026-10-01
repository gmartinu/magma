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
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang/glog"

	"magma/acs/cloud/go/services/acs/auth"
	"magma/acs/cloud/go/services/acs/cwmp"
	"magma/acs/cloud/go/services/acs/datamodel"
	"magma/acs/cloud/go/services/acs/kpi"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/acs/cloud/go/services/acs/tasks"
	"magma/orc8r/cloud/go/clock"
)

// acsMethods are the RPCs the ACS accepts from a CPE (TR-069 A.4.2).
var acsMethods = cwmp.StringList{"Inform", "GetRPCMethods", "TransferComplete"}

// continueSession handles every message of a session after its Inform.
//
// The session state machine: after the InformResponse the session is in
// SessionCPERequests and the CPE may send its own requests. Its empty POST
// moves the session to SessionACSRequests, where the ACS sends one request
// per HTTP response (credential rotation first, then the queued tasks) and
// ends the session with a 204 once it has nothing left.
func (s *Server) continueSession(req *request, sess *storage.Session, env *cwmp.Envelope) {
	sess.ExpiresSec = clock.Now().Unix() + int64(s.cfg.SessionTimeout/time.Second)

	if env != nil {
		switch m := env.Body.(type) {
		case *cwmp.GetRPCMethods:
			s.rpcObserved(req, sess, m.Method(), nil, "", "")
			s.answerCPE(req, sess, env.ID, &cwmp.GetRPCMethodsResponse{MethodList: acsMethods})
			return
		case *cwmp.TransferComplete:
			glog.Infof("%s: transfer %q complete, fault %d", sess.DeviceID, m.CommandKey, m.FaultStruct.FaultCode)
			var fault *cwmp.Fault
			if m.FaultStruct.FaultCode != 0 {
				fault = cwmp.NewFault(m.FaultStruct.FaultCode, m.FaultStruct.FaultString)
				cpeFault(fault)
			}
			s.rpcObserved(req, sess, m.Method(), fault, "", "")
			s.answerCPE(req, sess, env.ID, &cwmp.TransferCompleteResponse{})
			return
		case *cwmp.Unknown:
			if !strings.HasSuffix(m.Name, "Response") {
				fault := cwmp.NewFault(cwmp.FaultMethodNotSupported, "Method not supported")
				s.rpcObserved(req, sess, m.Name, fault, "", "")
				s.answerCPE(req, sess, env.ID, fault)
				return
			}
		}
	}

	if sess.PendingID != "" {
		if err := s.handleAnswer(req, sess, env); err != nil {
			s.internalError(req, err)
			return
		}
	} else if env != nil {
		glog.Warningf("%s: unsolicited %s", sess.DeviceID, env.Body.Method())
	}
	sess.Step = storage.SessionACSRequests
	s.next(req, sess)
}

func (s *Server) answerCPE(req *request, sess *storage.Session, id string, msg cwmp.Message) {
	if err := s.store.PutSession(sess); err != nil {
		s.internalError(req, err)
		return
	}
	s.write(req, sess, id, msg)
}

// handleAnswer processes the CPE's answer to the pending ACS request. A nil
// env is an empty POST where an answer was due.
func (s *Server) handleAnswer(req *request, sess *storage.Session, env *cwmp.Envelope) error {
	pendingMethod := sess.PendingMethod
	sess.PendingID, sess.PendingMethod = "", ""

	var fault *cwmp.Fault
	var answer cwmp.Message
	if env == nil {
		fault = cwmp.NewFault(cwmp.FaultInternalError, fmt.Sprintf("CPE sent an empty POST instead of answering %s", pendingMethod))
	} else if f, ok := env.Body.(*cwmp.Fault); ok {
		fault = f
		cpeFault(f)
	} else if env.Body.Method() != pendingMethod+"Response" {
		fault = cwmp.NewFault(cwmp.FaultInternalError, fmt.Sprintf("CPE answered %s with %s", pendingMethod, env.Body.Method()))
	} else {
		answer = env.Body
	}
	// The ACS-side faults above are protocol slips worth another try; a
	// fault the CPE sent is judged by its code.
	retryable := env == nil || answer == nil && env.Body.Method() != "Fault"

	if sess.PendingTaskID == "" {
		s.rpcObserved(req, sess, pendingMethod, fault, "", "")
		return s.rotationAnswered(sess, fault)
	}
	taskID := sess.PendingTaskID
	taskType, err := s.taskAnswered(sess, answer, fault, retryable)
	s.rpcObserved(req, sess, pendingMethod, fault, taskID, taskType)
	return err
}

// next sends the next ACS request of the session, or ends it.
func (s *Server) next(req *request, sess *storage.Session) {
	msg, err := s.nextRequest(sess)
	if err != nil {
		s.internalError(req, err)
		return
	}
	if msg == nil {
		if err := s.store.EndSession(sess.SessionID, "session ended"); err != nil {
			s.internalError(req, err)
			return
		}
		glog.V(1).Infof("%s: session %s done", sess.DeviceID, sess.SessionID)
		s.sessionEnded(req, sess)
		http.SetCookie(req.w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1})
		req.w.WriteHeader(http.StatusNoContent)
		return
	}
	sess.RequestSeq++
	sess.PendingID = fmt.Sprintf("%s.%d", sess.SessionID[:8], sess.RequestSeq)
	sess.PendingMethod = msg.Method()
	// The session is stored before the request goes out, so the answer can be
	// handled by any replica.
	if err := s.store.PutSession(sess); err != nil {
		s.internalError(req, err)
		return
	}
	s.write(req, sess, sess.PendingID, msg)
}

func (s *Server) nextRequest(sess *storage.Session) (cwmp.Message, error) {
	h := s.handlers.Get(sess.Handler)
	if sess.Bootstrap && s.cfg.RotateCredentials && !sess.RotationSent && !h.Quirks().NoCredentialRotation {
		sess.RotationSent = true
		return s.rotationRequest(sess, h)
	}
	if sess.PendingTaskID != "" {
		task, err := s.store.GetTask(sess.PendingTaskID)
		if err != nil {
			return nil, err
		}
		if task != nil && task.Status == storage.TaskInProgress && task.SessionID == sess.SessionID {
			plan, err := s.plan(task, sess)
			if err == nil && sess.TaskStep < len(plan) {
				return plan[sess.TaskStep], nil
			}
		}
		// The task was requeued or canceled meanwhile; move on.
		sess.PendingTaskID, sess.TaskStep = "", 0
	}
	for {
		task, err := s.store.ClaimNextTask(sess.DeviceID, sess.SessionID)
		if err != nil || task == nil {
			return nil, err
		}
		plan, err := s.plan(task, sess)
		if err != nil {
			if err := s.store.FailTask(task.TaskID, cwmp.FaultInvalidArguments, err.Error(), false); err != nil {
				return nil, err
			}
			continue
		}
		if len(plan) == 0 {
			if err := s.finishTask(sess, task, tasks.Result{}); err != nil {
				return nil, err
			}
			continue
		}
		sess.PendingTaskID, sess.TaskStep = task.TaskID, 0
		return plan[0], nil
	}
}

func (s *Server) plan(task *storage.Task, sess *storage.Session) ([]cwmp.Message, error) {
	args, err := tasks.ParseArgs(task.Args)
	if err != nil {
		return nil, fmt.Errorf("bad task arguments: %w", err)
	}
	return tasks.Plan(task.Type, args, s.handlers.Get(sess.Handler), sess.Root, task.TaskID)
}

// taskAnswered applies the answer to the pending task and returns the task
// type, "" when the task is gone.
func (s *Server) taskAnswered(sess *storage.Session, answer cwmp.Message, fault *cwmp.Fault, retryable bool) (string, error) {
	task, err := s.store.GetTask(sess.PendingTaskID)
	if err != nil {
		return "", err
	}
	if task == nil || task.Status != storage.TaskInProgress || task.SessionID != sess.SessionID {
		sess.PendingTaskID, sess.TaskStep = "", 0
		return "", nil
	}
	return task.Type, s.applyAnswer(sess, task, answer, fault, retryable)
}

func (s *Server) applyAnswer(sess *storage.Session, task *storage.Task, answer cwmp.Message, fault *cwmp.Fault, retryable bool) error {
	result, err := tasks.ParseResult(task.Result)
	if err != nil {
		result = tasks.Result{}
	}
	plan, err := s.plan(task, sess)
	if err != nil {
		return err
	}

	switch {
	case fault != nil && tasks.Tolerated(task.Type, fault):
		req := ""
		if sess.TaskStep < len(plan) {
			req = describe(plan[sess.TaskStep])
		}
		result.Faults = append(result.Faults, tasks.StepFault{Request: req, FaultCode: int(fault.Code()), FaultString: fault.String()})
	case fault != nil:
		glog.Infof("%s: task %s (%s) fault %d: %s", sess.DeviceID, task.TaskID, task.Type, fault.Code(), fault.String())
		sess.PendingTaskID, sess.TaskStep = "", 0
		return s.store.FailTask(task.TaskID, int(fault.Code()), fault.String(), retryable || tasks.Retryable(fault))
	default:
		if err := tasks.Apply(&result, answer); err != nil {
			sess.PendingTaskID, sess.TaskStep = "", 0
			return s.store.FailTask(task.TaskID, cwmp.FaultInternalError, err.Error(), true)
		}
	}

	sess.TaskStep++
	if sess.TaskStep < len(plan) {
		b, err := marshalJSON(result)
		if err != nil {
			return err
		}
		return s.store.SaveTaskResult(task.TaskID, b)
	}
	sess.PendingTaskID, sess.TaskStep = "", 0
	return s.finishTask(sess, task, result)
}

func (s *Server) finishTask(sess *storage.Session, task *storage.Task, result tasks.Result) error {
	now := clock.Now().Unix()
	switch task.Type {
	case tasks.TypeRefresh:
		h := s.handlers.Get(sess.Handler)
		model := h.Normalize(sess.Root, result.Values)
		if err := s.mergeState(sess.DeviceID, h.Name(), model, now); err != nil {
			return err
		}
		if err := s.store.MergeParameters(sess.DeviceID, result.Values, now); err != nil {
			return err
		}
		if err := s.refreshDevice(sess.DeviceID, model); err != nil {
			return err
		}
	case tasks.TypeGetParameterValues:
		if err := s.store.MergeParameters(sess.DeviceID, result.Values, now); err != nil {
			return err
		}
	case tasks.TypeFactoryReset:
		// A factory reset CPE comes back with the bootstrap credentials.
		if err := s.store.DeleteCredentials(sess.DeviceID); err != nil {
			return err
		}
	}
	b, err := marshalJSON(result)
	if err != nil {
		return err
	}
	glog.Infof("%s: task %s (%s) done", sess.DeviceID, task.TaskID, task.Type)
	return s.store.CompleteTask(task.TaskID, b)
}

// rotationRequest replaces the bootstrap credentials of the CPE with its own.
// The new password is kept as pending until the CPE confirms it, either by
// answering the SetParameterValues or by authenticating with it later.
func (s *Server) rotationRequest(sess *storage.Session, h datamodel.Handler) (cwmp.Message, error) {
	n := h.Quirks().PasswordLength
	if n <= 0 {
		n = datamodel.DefaultPasswordLength
	}
	password, err := randomPassword(n)
	if err != nil {
		return nil, err
	}
	crPassword, err := randomPassword(n)
	if err != nil {
		return nil, err
	}
	creds, err := s.store.GetCredentials(sess.DeviceID)
	if err != nil {
		return nil, err
	}
	if creds == nil {
		creds = &storage.Credentials{DeviceID: sess.DeviceID}
	}
	username := sess.DeviceID
	creds.ACSUsername = username
	creds.PendingPasswordHash = auth.HashPassword(username, s.cfg.Realm, password)
	creds.ConnReqUsername, creds.ConnReqPassword = username, crPassword
	creds.UpdatedSec = clock.Now().Unix()
	if err := s.store.PutCredentials(creds); err != nil {
		return nil, err
	}
	p := func(name string) string { return datamodel.ManagementServerPath(sess.Root, name) }
	return &cwmp.SetParameterValues{ParameterList: cwmp.ParameterValueList{
		{Name: p("Username"), Value: username, Type: "xsd:string"},
		{Name: p("Password"), Value: password, Type: "xsd:string"},
		{Name: p("ConnectionRequestUsername"), Value: username, Type: "xsd:string"},
		{Name: p("ConnectionRequestPassword"), Value: crPassword, Type: "xsd:string"},
	}}, nil
}

func (s *Server) rotationAnswered(sess *storage.Session, fault *cwmp.Fault) error {
	creds, err := s.store.GetCredentials(sess.DeviceID)
	if err != nil || creds == nil {
		return err
	}
	if fault != nil {
		glog.Warningf("%s: credential rotation refused, fault %d: %s", sess.DeviceID, fault.Code(), fault.String())
		creds.PendingPasswordHash = ""
		creds.UpdatedSec = clock.Now().Unix()
		return s.store.PutCredentials(creds)
	}
	glog.Infof("%s: credentials rotated", sess.DeviceID)
	sess.Bootstrap = false
	return s.promoteCredentials(creds)
}

func (s *Server) promoteCredentials(creds *storage.Credentials) error {
	creds.ACSPasswordHash, creds.PendingPasswordHash = creds.PendingPasswordHash, ""
	creds.UpdatedSec = clock.Now().Unix()
	return s.store.PutCredentials(creds)
}

func describe(m cwmp.Message) string {
	if gpv, ok := m.(*cwmp.GetParameterValues); ok {
		return m.Method() + " " + strings.Join(gpv.ParameterNames, ",")
	}
	return m.Method()
}

func marshalJSON(v interface{}) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func unmarshalJSON(s string, v interface{}) error {
	return json.Unmarshal([]byte(s), v)
}

// refreshDevice copies what a refresh read onto the device record: the model
// name and firmware its list filters on, which the Inform may not carry, and
// the periodic inform interval its online state is derived from.
func (s *Server) refreshDevice(deviceID string, m *datamodel.Model) error {
	d, err := s.store.GetDevice(deviceID)
	if err != nil || d == nil {
		return err
	}
	s.kpis.Report(d.NetworkID, kpi.Samples(deviceID, m))
	updated := *d
	if v := m.Identity.ModelName; v != "" {
		updated.Model = v
	}
	if v := m.Firmware.SoftwareVersion; v != "" {
		updated.Firmware = v
	}
	if v := m.ManagementServer.PeriodicInformInterval; v != nil {
		updated.InformIntervalSec = *v
	}
	if updated.Model == d.Model && updated.Firmware == d.Firmware && updated.InformIntervalSec == d.InformIntervalSec {
		return nil
	}
	return s.store.UpsertDevice(&updated)
}
