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

// Package server is the CWMP endpoint CPEs connect to. It keeps no session
// state in memory: every request loads and stores its session in the
// database, so consecutive requests of a session may land on any replica.
package server

import (
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang/glog"

	"magma/acs/cloud/go/services/acs/auth"
	"magma/acs/cloud/go/services/acs/cwmp"
	"magma/acs/cloud/go/services/acs/datamodel"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/orc8r/cloud/go/clock"
)

// CookieName is the session cookie set on the InformResponse.
const CookieName = "acs_session"

// nonceSecretName is the acs_secrets row shared by the replicas to MAC
// Digest nonces.
const nonceSecretName = "digest_nonce"

// Config is the runtime configuration of the CWMP endpoint.
type Config struct {
	// Realm is the Digest realm. Stored password hashes are bound to it.
	Realm       string
	BasicPolicy auth.BasicPolicy
	// TrustProxyHeaders makes the server take the client address from
	// X-Real-IP/X-Forwarded-For and TLS from X-Forwarded-Proto. Only set it
	// when the listener is reachable solely through a proxy that overwrites
	// those headers; otherwise a client could claim TLS and get Basic.
	TrustProxyHeaders bool
	// BootstrapUsername and BootstrapPassword are the shared credentials
	// accepted from devices the ACS has no credentials for. Empty disables
	// bootstrap.
	BootstrapUsername string
	BootstrapPassword string
	// RotateCredentials sends per-device credentials to a CPE that
	// authenticated with the bootstrap credentials.
	RotateCredentials bool
	SessionTimeout    time.Duration
	NonceTTL          time.Duration
	// InformRateLimit caps the sessions a device may start per
	// InformRateWindow; 0 disables the limit.
	InformRateLimit  int
	InformRateWindow time.Duration
	MaxBodyBytes     int64
}

func (c Config) withDefaults() Config {
	if c.Realm == "" {
		c.Realm = "magma-acs"
	}
	if c.BasicPolicy == "" {
		c.BasicPolicy = auth.BasicTLSOnly
	}
	if c.SessionTimeout <= 0 {
		c.SessionTimeout = 2 * time.Minute
	}
	if c.NonceTTL <= 0 {
		c.NonceTTL = 5 * time.Minute
	}
	if c.InformRateWindow <= 0 {
		c.InformRateWindow = 5 * time.Minute
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = 4 << 20
	}
	return c
}

// Server serves the CWMP endpoint.
type Server struct {
	cfg           Config
	store         storage.ACSStorage
	handlers      *datamodel.Registry
	auth          *auth.Authenticator
	bootstrapHash string
}

// New returns a server. The Digest nonce secret is read from, or created in,
// the database so that every replica shares it.
func New(cfg Config, store storage.ACSStorage, handlers *datamodel.Registry) (*Server, error) {
	cfg = cfg.withDefaults()
	secret, err := store.GetOrCreateSecret(nonceSecretName, func() (string, error) { return randomHex(32) })
	if err != nil {
		return nil, fmt.Errorf("load digest nonce secret: %w", err)
	}
	s := &Server{
		cfg:      cfg,
		store:    store,
		handlers: handlers,
		auth:     auth.NewAuthenticator(cfg.Realm, cfg.BasicPolicy, []byte(secret), cfg.NonceTTL),
	}
	if cfg.BootstrapUsername != "" && cfg.BootstrapPassword != "" {
		s.bootstrapHash = auth.HashPassword(cfg.BootstrapUsername, cfg.Realm, cfg.BootstrapPassword)
	}
	return s, nil
}

// request is the per-request context of the handler.
type request struct {
	w      http.ResponseWriter
	r      *http.Request
	secure bool
	client string
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req := &request{w: w, r: r, secure: s.isSecure(r), client: s.clientAddr(r)}
	body, err := s.readBody(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var env *cwmp.Envelope
	if !cwmp.IsEmpty(body) {
		env, err = cwmp.Decode(body)
		if err != nil {
			glog.Warningf("cwmp %s: %s", req.client, err)
			http.Error(w, "malformed CWMP message", http.StatusBadRequest)
			return
		}
	}

	if env != nil {
		if inform, ok := env.Body.(*cwmp.Inform); ok {
			s.handleInform(req, env, inform)
			return
		}
	}

	sess, err := s.sessionFromCookie(r)
	if err != nil {
		s.internalError(req, err)
		return
	}
	if sess == nil {
		var ok bool
		if sess, ok = s.sessionFromAuth(req); !ok {
			return
		}
	}
	if sess == nil {
		// Nothing to continue: the session ended or never started with an
		// Inform. An empty POST just ends the exchange.
		if env == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "a CWMP session starts with an Inform", http.StatusBadRequest)
		return
	}
	s.continueSession(req, sess, env)
}

func (s *Server) isSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.cfg.TrustProxyHeaders && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func (s *Server) clientAddr(r *http.Request) string {
	if s.cfg.TrustProxyHeaders {
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	var rd io.Reader = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(rd)
		if err != nil {
			return nil, fmt.Errorf("bad gzip body: %w", err)
		}
		defer gz.Close()
		rd = io.LimitReader(gz, s.cfg.MaxBodyBytes)
	}
	return io.ReadAll(rd)
}

func (s *Server) sessionFromCookie(r *http.Request) (*storage.Session, error) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	return s.store.GetSession(c.Value)
}

// sessionFromAuth finds the session of a CPE that did not send the session
// cookie back, by the device its credentials belong to. It returns false
// after writing a challenge.
func (s *Server) sessionFromAuth(req *request) (*storage.Session, bool) {
	at, err := s.auth.Parse(req.r, req.secure)
	if err != nil {
		s.challenge(req, err)
		return nil, false
	}
	if s.bootstrapHash != "" && at.Username == s.cfg.BootstrapUsername && at.Verify(s.bootstrapHash) {
		// Bootstrap credentials do not name a device, so a cookie-less
		// session on them cannot be continued.
		return nil, true
	}
	creds, err := s.store.GetCredentialsByUsername(at.Username)
	if err != nil {
		s.internalError(req, err)
		return nil, false
	}
	if creds == nil || (!at.Verify(creds.ACSPasswordHash) && !at.Verify(creds.PendingPasswordHash)) {
		s.challenge(req, errors.New("bad credentials without a session cookie"))
		return nil, false
	}
	sess, err := s.store.GetDeviceSession(creds.DeviceID)
	if err != nil {
		s.internalError(req, err)
		return nil, false
	}
	return sess, true
}

func (s *Server) challenge(req *request, err error) {
	if err != nil && !errors.Is(err, auth.ErrNoCredentials) {
		glog.Warningf("cwmp %s: auth failed: %s", req.client, err)
	}
	s.auth.Challenge(req.w, req.secure, errors.Is(err, auth.ErrStaleNonce))
}

func (s *Server) internalError(req *request, err error) {
	glog.Errorf("cwmp %s: %s", req.client, err)
	http.Error(req.w, "internal error", http.StatusInternalServerError)
}

// authenticateInform checks the credentials of an Inform from deviceID and
// reports whether they are the bootstrap ones. It returns false after
// writing the response.
func (s *Server) authenticateInform(req *request, deviceID string) (bootstrap bool, ok bool) {
	at, err := s.auth.Parse(req.r, req.secure)
	if err != nil {
		s.challenge(req, err)
		return false, false
	}
	if s.bootstrapHash != "" && at.Username == s.cfg.BootstrapUsername {
		if !at.Verify(s.bootstrapHash) {
			s.challenge(req, fmt.Errorf("bad bootstrap password from %s", deviceID))
			return false, false
		}
		creds, err := s.store.GetCredentials(deviceID)
		if err != nil {
			s.internalError(req, err)
			return false, false
		}
		if creds != nil && creds.ACSPasswordHash != "" {
			s.challenge(req, fmt.Errorf("bootstrap credentials refused for provisioned device %s", deviceID))
			return false, false
		}
		return true, true
	}

	creds, err := s.store.GetCredentialsByUsername(at.Username)
	if err != nil {
		s.internalError(req, err)
		return false, false
	}
	if creds == nil {
		s.challenge(req, fmt.Errorf("unknown username %q", at.Username))
		return false, false
	}
	if creds.DeviceID != deviceID {
		glog.Warningf("cwmp %s: credentials of %s used by %s", req.client, creds.DeviceID, deviceID)
		http.Error(req.w, "credentials belong to another device", http.StatusForbidden)
		return false, false
	}
	switch {
	case at.Verify(creds.ACSPasswordHash):
	case at.Verify(creds.PendingPasswordHash):
		// The CPE applied a rotation whose response the ACS never got.
		if err := s.promoteCredentials(creds); err != nil {
			s.internalError(req, err)
			return false, false
		}
	default:
		s.challenge(req, fmt.Errorf("bad password for %s", deviceID))
		return false, false
	}
	return false, true
}

func (s *Server) handleInform(req *request, env *cwmp.Envelope, inform *cwmp.Inform) {
	id := inform.DeviceId
	if id.OUI == "" || id.SerialNumber == "" {
		http.Error(req.w, "Inform without OUI or SerialNumber", http.StatusBadRequest)
		return
	}
	deviceID := id.DeviceID()
	bootstrap, ok := s.authenticateInform(req, deviceID)
	if !ok {
		return
	}

	now := clock.Now().Unix()
	params := inform.ParameterList.Map()
	names := make([]string, 0, len(params))
	for n := range params {
		names = append(names, n)
	}
	root := datamodel.DetectRoot(names)
	if root == "" {
		root = datamodel.RootTR181
	}
	info := datamodel.InfoFromParams(datamodel.DeviceInfo{
		Manufacturer: id.Manufacturer, OUI: id.OUI, ProductClass: id.ProductClass,
	}, params)
	h := s.handlers.Select(info)
	reported := h.Normalize(root, params)

	if err := s.saveDevice(deviceID, id, h, reported, now); err != nil {
		s.internalError(req, err)
		return
	}
	if s.cfg.InformRateLimit > 0 {
		n, windowEnd, err := s.store.CountInform(deviceID, int64(s.cfg.InformRateWindow/time.Second))
		if err != nil {
			s.internalError(req, err)
			return
		}
		if n > s.cfg.InformRateLimit {
			glog.Warningf("cwmp %s: %s over %d sessions per %s", req.client, deviceID, s.cfg.InformRateLimit, s.cfg.InformRateWindow)
			req.w.Header().Set("Retry-After", strconv.FormatInt(windowEnd-now, 10))
			http.Error(req.w, "too many sessions", http.StatusServiceUnavailable)
			return
		}
	}

	// A new Inform means the CPE gave up on any session it had open.
	if err := s.store.EndDeviceSessions(deviceID, "CPE started a new session"); err != nil {
		s.internalError(req, err)
		return
	}
	sessionID, err := randomHex(16)
	if err != nil {
		s.internalError(req, err)
		return
	}
	sess := &storage.Session{
		SessionID:  sessionID,
		DeviceID:   deviceID,
		Step:       storage.SessionCPERequests,
		Namespace:  env.Namespace,
		Root:       root,
		Handler:    h.Name(),
		Bootstrap:  bootstrap,
		CreatedSec: now,
		ExpiresSec: now + int64(s.cfg.SessionTimeout/time.Second),
	}
	if err := s.store.PutSession(sess); err != nil {
		s.internalError(req, err)
		return
	}
	glog.V(1).Infof("cwmp %s: session %s for %s, events %v", req.client, sessionID, deviceID, inform.Event)

	http.SetCookie(req.w, &http.Cookie{Name: CookieName, Value: sessionID, Path: "/", HttpOnly: true, Secure: req.secure})
	s.write(req, sess, env.ID, &cwmp.InformResponse{MaxEnvelopes: 1})
}

// saveDevice records the device and merges what its Inform reports onto its
// normalized model.
func (s *Server) saveDevice(deviceID string, id cwmp.DeviceIDStruct, h datamodel.Handler, reported *datamodel.Model, now int64) error {
	prev, err := s.store.GetDevice(deviceID)
	if err != nil {
		return err
	}
	d := &storage.Device{
		DeviceID:     deviceID,
		OUI:          id.OUI,
		ProductClass: id.ProductClass,
		SerialNumber: id.SerialNumber,
		Handler:      h.Name(),
		FirstSeenSec: now,
		LastSeenSec:  now,
	}
	if prev != nil {
		d.Model, d.Firmware, d.ConnectionRequestURL = prev.Model, prev.Firmware, prev.ConnectionRequestURL
	}
	if v := reported.Identity.ModelName; v != "" {
		d.Model = v
	}
	if v := reported.Firmware.SoftwareVersion; v != "" {
		d.Firmware = v
	}
	if v := reported.ManagementServer.ConnectionRequestURL; v != "" {
		d.ConnectionRequestURL = v
	}
	if err := s.store.UpsertDevice(d); err != nil {
		return err
	}
	return s.mergeState(deviceID, h.Name(), reported, now)
}

func (s *Server) mergeState(deviceID, handler string, m *datamodel.Model, now int64) error {
	model := &datamodel.Model{}
	st, err := s.store.GetDeviceState(deviceID)
	if err != nil {
		return err
	}
	if st != nil {
		if err := unmarshalJSON(st.Model, model); err != nil {
			glog.Warningf("discarding unreadable state of %s: %s", deviceID, err)
			model = &datamodel.Model{}
		}
	}
	model.Merge(m)
	b, err := marshalJSON(model)
	if err != nil {
		return err
	}
	return s.store.PutDeviceState(&storage.DeviceState{DeviceID: deviceID, Handler: handler, Model: b, UpdatedSec: now})
}

// write sends a CWMP message in the namespace of the session.
func (s *Server) write(req *request, sess *storage.Session, id string, msg cwmp.Message) {
	out, err := cwmp.Encode(&cwmp.Envelope{ID: id, Namespace: sess.Namespace, Body: msg})
	if err != nil {
		s.internalError(req, err)
		return
	}
	req.w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	req.w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	req.w.WriteHeader(http.StatusOK)
	_, _ = req.w.Write(out)
}

// HTTPServer returns the CWMP listener on port, separate from the echo
// server that serves the REST API.
func (s *Server) HTTPServer(port int) *http.Server {
	return &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		// CPEs keep the connection open across the requests of a session.
		IdleTimeout: 90 * time.Second,
	}
}

// RunMaintenance ends expired sessions and expires overdue tasks every
// interval until stop is closed. Every replica may run it.
func (s *Server) RunMaintenance(stop <-chan struct{}, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			res, err := s.store.ReapExpired()
			if err != nil {
				glog.Errorf("reap expired sessions: %s", err)
			} else if res != (storage.ReapResult{}) {
				glog.Infof("reaped %d sessions, requeued %d tasks, expired %d tasks", res.Sessions, res.RequeuedTasks, res.ExpiredTasks)
			}
		}
	}
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// randomPassword returns n URL-safe characters, which every CPE stack accepts
// in ManagementServer.Password.
func randomPassword(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b)[:n], nil
}
