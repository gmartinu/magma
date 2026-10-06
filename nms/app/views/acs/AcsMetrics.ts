/**
 * Copyright 2026 The Magma Authors.
 *
 * This source code is licensed under the BSD-style license found in the
 * LICENSE file in the root directory of this source tree.
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// ACS KPIs read through the Orc8r Prometheus API acsd's metrics land in
// (magmad's metricsd polls acsd; magma adds the gatewayID label).
import MagmaAPI from '../../api/MagmaAPI';
import {PromqlMetricValue} from '../../../generated';

export const CPE_KPIS = [
  {
    metric: 'acs_rsrp_dbm',
    label: 'RSRP',
    unit: 'dBm',
    min: -120,
    max: -60,
    poor: -100,
    color: '#3984FF',
  },
  {
    metric: 'acs_rsrq_db',
    label: 'RSRQ',
    unit: 'dB',
    min: -20,
    max: -4,
    poor: -15,
    color: '#A07EEA',
  },
  {
    metric: 'acs_sinr_db',
    label: 'SINR',
    unit: 'dB',
    min: -5,
    max: 25,
    poor: 0,
    color: '#39B6C8',
  },
] as const;

export type KpiPoint = {x: number; y: number};

// PromQL label values are double-quoted strings; keep a CPE key from
// breaking out of the selector.
export function selectorValue(value: string): string {
  return value.replace(/\\/g, '\\\\').replace(/"/g, '\\"');
}

export function cpeSelector(metric: string, cpeKey: string): string {
  return `${metric}{cpe_key="${selectorValue(cpeKey)}"}`;
}

// One series per CPE: acsd's gauges carry the gateway's labels, so a CPE
// that moved to another AGW, or a claim key on two AGWs, has a series per
// gateway until the old one goes stale; Prometheus would answer them in no
// particular order and the chart would plot whichever came first.
export function cpeKpiQuery(metric: string, cpeKey: string): string {
  return `max by (cpe_key) (${cpeSelector(metric, cpeKey)})`;
}

export function networkKpiQuery(metric: string): string {
  return `max by (cpe_key) (${metric})`;
}

export function stepFor(rangeSec: number): string {
  // ~300 points per chart, never finer than the 60 s scrape.
  return `${Math.max(60, Math.round(rangeSec / 300))}s`;
}

export async function queryRange(
  networkId: string,
  query: string,
  start: Date,
  end: Date,
): Promise<Array<PromqlMetricValue>> {
  const res = await MagmaAPI.metrics.networksNetworkIdPrometheusQueryRangeGet({
    networkId,
    query,
    start: start.toISOString(),
    end: end.toISOString(),
    step: stepFor((end.getTime() - start.getTime()) / 1000),
  });
  return res.data?.data?.result ?? [];
}

export async function queryInstant(
  networkId: string,
  query: string,
): Promise<Array<PromqlMetricValue>> {
  const res = await MagmaAPI.metrics.networksNetworkIdPrometheusQueryGet({
    networkId,
    query,
  });
  return res.data?.data?.result ?? [];
}

export function toPoints(result: Array<PromqlMetricValue>): Array<KpiPoint> {
  const values = result[0]?.values ?? [];
  return values.map(([t, v]) => ({
    x: parseFloat(t) * 1000,
    y: parseFloat(v),
  }));
}

function label(m: PromqlMetricValue, name: string): string | undefined {
  return (m.metric as Record<string, string | undefined>)[name];
}

// Instant vector → value per label (cpe_key, gatewayID, code, ...).
export function byLabel(
  result: Array<PromqlMetricValue>,
  name: string,
): Record<string, number> {
  const out: Record<string, number> = {};
  for (const m of result) {
    const key = label(m, name);
    const v = m.value?.[1];
    if (key !== undefined && v !== undefined) {
      out[key] = parseFloat(v);
    }
  }
  return out;
}

// RSRP and SINR of every CPE of the network, for the list's signal column.
export async function latestSignal(
  networkId: string,
): Promise<Record<string, {rsrp?: number; sinr?: number}>> {
  const [rsrp, sinr] = await Promise.all([
    queryInstant(networkId, networkKpiQuery('acs_rsrp_dbm')),
    queryInstant(networkId, networkKpiQuery('acs_sinr_db')),
  ]);
  const out: Record<string, {rsrp?: number; sinr?: number}> = {};
  for (const [key, v] of Object.entries(byLabel(rsrp, 'cpe_key'))) {
    out[key] = {...out[key], rsrp: v};
  }
  for (const [key, v] of Object.entries(byLabel(sinr, 'cpe_key'))) {
    out[key] = {...out[key], sinr: v};
  }
  return out;
}
