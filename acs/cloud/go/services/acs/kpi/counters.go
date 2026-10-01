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

package kpi

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Process counters of one replica. They are in the default registry, which
// Service303 GetMetrics serves and metricsd scrapes; they carry the service
// and host labels, not a network.
var (
	Sessions = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "acs_sessions_total",
		Help: "CWMP sessions started by an accepted Inform.",
	})
	Faults = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "acs_faults_total",
		Help: "CWMP faults CPEs answered ACS requests with, by fault code.",
	}, []string{"code"})
	AuthFailures = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "acs_auth_failures_total",
		Help: "CWMP requests refused for bad or misused credentials.",
	})
	RateLimited = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "acs_rate_limited_total",
		Help: "Informs refused by the per-device session rate limit.",
	})
	PushFailures = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "acs_kpi_push_failures_total",
		Help: "Pushes of CPE KPIs to metricsd that failed.",
	})
	PushesDropped = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "acs_kpi_pushes_dropped_total",
		Help: "Pushes of CPE KPIs dropped because too many were in flight.",
	})
	SessionLogsDropped = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "acs_session_logs_dropped_total",
		Help: "Session log documents dropped because the log sink was full or failing.",
	})
)

func init() {
	prometheus.MustRegister(Sessions, Faults, AuthFailures, RateLimited, PushFailures, PushesDropped, SessionLogsDropped)
}
