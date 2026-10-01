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

// Package kpi exports the ACS metrics: per-CPE KPIs pushed to metricsd with
// the network of the CPE, and process counters scraped through Service303.
package kpi

import (
	"context"
	"time"

	"github.com/golang/glog"

	"magma/acs/cloud/go/services/acs/datamodel"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/orc8r/cloud/go/clock"
	"magma/orc8r/cloud/go/services/metricsd"
	"magma/orc8r/lib/go/protos"
)

// Per-CPE metrics. metricsd stores every pushed metric as a gauge, so
// acs_inform_total is a gauge carrying a count the ACS keeps per device in
// the database; it only grows, so rate() and increase() work on it.
const (
	RSRP        = "acs_rsrp_dbm"
	RSRQ        = "acs_rsrq_db"
	SINR        = "acs_sinr_db"
	Online      = "acs_online"
	Uptime      = "acs_uptime_s"
	InformTotal = "acs_inform_total"

	// CPELabel identifies the CPE (its TR-069 DeviceId); metricsd adds
	// networkID.
	CPELabel = "cpeID"
)

// Pusher sends the metrics of a network to metricsd.
type Pusher interface {
	Push(ctx context.Context, networkID string, metrics []*protos.PushedMetric) error
}

// MetricsdPusher pushes through the metricsd service.
type MetricsdPusher struct{}

func (MetricsdPusher) Push(ctx context.Context, networkID string, metrics []*protos.PushedMetric) error {
	return metricsd.PushMetrics(ctx, &protos.PushedMetricsContainer{NetworkId: networkID, Metrics: metrics})
}

// Samples returns the KPIs a CPE reported. A KPI it did not report has no
// sample: an absent series is "unknown", a 0 would be a reading.
func Samples(deviceID string, m *datamodel.Model) []*protos.PushedMetric {
	if m == nil {
		return nil
	}
	var out []*protos.PushedMetric
	add := func(name string, v *float64) {
		if v != nil {
			out = append(out, Sample(name, deviceID, *v))
		}
	}
	add(RSRP, m.Cellular.RSRP)
	add(RSRQ, m.Cellular.RSRQ)
	add(SINR, m.Cellular.SINR)
	if m.UptimeSec != nil {
		v := float64(*m.UptimeSec)
		add(Uptime, &v)
	}
	return out
}

// Sample is one metric of a CPE at the current time.
func Sample(name, deviceID string, value float64) *protos.PushedMetric {
	return &protos.PushedMetric{
		MetricName:  name,
		Value:       value,
		TimestampMS: clock.Now().UnixMilli(),
		Labels:      []*protos.LabelPair{{Name: CPELabel, Value: deviceID}},
	}
}

// Reporter pushes metrics in the background, so a slow or absent metricsd
// never holds up a CWMP session. A nil Reporter drops everything.
type Reporter struct {
	pusher  Pusher
	timeout time.Duration
	slots   chan struct{}
}

// NewReporter returns a reporter running at most maxInFlight pushes at once;
// pushes beyond that are dropped and counted.
func NewReporter(p Pusher, timeout time.Duration, maxInFlight int) *Reporter {
	if maxInFlight <= 0 {
		maxInFlight = 1
	}
	return &Reporter{pusher: p, timeout: timeout, slots: make(chan struct{}, maxInFlight)}
}

// Report pushes the metrics of a CPE claimed by networkID. Unclaimed CPEs
// have no network to report under and are skipped.
func (r *Reporter) Report(networkID string, metrics []*protos.PushedMetric) {
	if r == nil || networkID == "" || len(metrics) == 0 {
		return
	}
	select {
	case r.slots <- struct{}{}:
	default:
		PushesDropped.Inc()
		return
	}
	go func() {
		defer func() { <-r.slots }()
		ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
		defer cancel()
		if err := r.pusher.Push(ctx, networkID, metrics); err != nil {
			PushFailures.Inc()
			glog.Warningf("push %d acs metrics of network %s: %s", len(metrics), networkID, err)
		}
	}()
}

// ReportOnline pushes acs_online for every claimed device. A device only
// tells the ACS it is up, by informing; this sweep is what turns acs_online
// to 0 once it stops.
func (r *Reporter) ReportOnline(store storage.ACSStorage, policy storage.OnlinePolicy) error {
	if r == nil {
		return nil
	}
	now := clock.Now().Unix()
	devices, err := store.FindDevices(storage.DeviceFilter{Claimed: true})
	if err != nil {
		return err
	}
	byNetwork := map[string][]*protos.PushedMetric{}
	for _, d := range devices {
		v := 0.0
		if policy.Online(d, now) {
			v = 1
		}
		byNetwork[d.NetworkID] = append(byNetwork[d.NetworkID], Sample(Online, d.DeviceID, v))
	}
	for networkID, metrics := range byNetwork {
		r.Report(networkID, metrics)
	}
	return nil
}

// RunOnlineSweep calls ReportOnline every interval until stop is closed.
func (r *Reporter) RunOnlineSweep(stop <-chan struct{}, store storage.ACSStorage, policy storage.OnlinePolicy, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := r.ReportOnline(store, policy); err != nil {
				glog.Errorf("report acs_online: %s", err)
			}
		}
	}
}
