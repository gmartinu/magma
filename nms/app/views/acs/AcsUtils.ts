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
import {AcsCpe, AcsCpeModel, AcsTaskStatus, AcsTaskType} from './AcsAPI';
import {colors} from '../../theme/default';
import {format, formatDistanceStrict} from 'date-fns';

// Periodic Inform interval the design assumes when the CPE did not say.
export const DEFAULT_INFORM_INTERVAL_SEC = 300;
// acsd's default task TTL (acsd.yml): an action queued now expires in 7 d.
export const TASK_TTL_SEC = 7 * 24 * 3600;

export const TASK_LABELS: Record<AcsTaskType, string> = {
  reboot: 'Reboot',
  factory_reset: 'Factory reset',
  refresh: 'Refresh',
  get_parameter_values: 'Get parameters',
  set_parameter_values: 'Set parameters',
  get_parameter_names: 'Get parameter names',
};

export const TASK_STATUS: Record<
  AcsTaskStatus,
  {label: string; color: string; fill: string; help: string}
> = {
  pending: {
    label: 'Pending',
    color: colors.state.warningAlt,
    fill: colors.state.warningFill,
    help: 'queued, waits for the next Inform',
  },
  in_progress: {
    label: 'In progress',
    color: colors.secondary.mariner,
    fill: colors.primary.selago,
    help: 'running inside a session',
  },
  done: {
    label: 'Done',
    color: colors.state.positiveAlt,
    fill: '#EFFAF2',
    help: 'CPE confirmed',
  },
  failed: {
    label: 'Failed',
    color: colors.state.errorAlt,
    fill: colors.state.errorFill,
    help: 'fault after the last attempt; code shown',
  },
  expired: {
    label: 'Expired',
    color: colors.primary.comet,
    fill: colors.primary.concrete,
    help: 'no Inform before the 7-day TTL',
  },
};

export function taskLabel(type: string): string {
  return TASK_LABELS[type as AcsTaskType] ?? type;
}

// Timestamps arrive as RFC 3339 from Orc8r; the gRPC layer below uses unix
// seconds, so accept both.
export function toDate(value?: string | number | null): Date | null {
  if (value === undefined || value === null || value === '') {
    return null;
  }
  const date =
    typeof value === 'number' ? new Date(value * 1000) : new Date(value);
  return isNaN(date.getTime()) || date.getTime() <= 0 ? null : date;
}

export function formatTime(value?: string | number | null): string {
  const date = toDate(value);
  return date ? format(date, 'MMM d, HH:mm:ss') : '—';
}

export function formatAgo(
  value?: string | number | null,
  now: Date = new Date(),
): string {
  const date = toDate(value);
  if (!date) {
    return 'never';
  }
  if (now.getTime() - date.getTime() < 60000) {
    return 'just now';
  }
  return `${formatDistanceStrict(date, now)} ago`;
}

export function formatDuration(
  start?: string | number | null,
  end?: string | number | null,
): string {
  const s = toDate(start);
  const e = toDate(end);
  if (!s || !e) {
    return '—';
  }
  const sec = (e.getTime() - s.getTime()) / 1000;
  return sec < 60 ? `${sec.toFixed(1)} s` : `${Math.round(sec / 60)} min`;
}

// TR-069 DeviceId, the string an operator reads off the unit's label.
export function deviceId(cpe: AcsCpe): string {
  const parts = [cpe.oui, cpe.product_class, cpe.serial_number].filter(Boolean);
  return parts.length ? parts.join('-') : '—';
}

export function cpeName(cpe: AcsCpe): string {
  return cpe.serial_number || cpe.cpe_key;
}

export function modeLabel(mode: string): string {
  return mode === 'claimed' ? 'Claimed' : 'Core';
}

// Signal bars from RSRP, thresholds of the approved design (open question:
// carrier-specific).
export function signalBars(rsrp?: number | null): number {
  if (rsrp === undefined || rsrp === null || isNaN(rsrp)) {
    return 0;
  }
  if (rsrp >= -80) return 4;
  if (rsrp >= -90) return 3;
  if (rsrp >= -100) return 2;
  return 1;
}

export const WEAK_RSRP_DBM = -100;

export function modelValue(
  model: AcsCpeModel | undefined,
  section: string,
  ...keys: Array<string>
): string | undefined {
  const values = model?.[section as keyof AcsCpeModel] as
    | Record<string, unknown>
    | undefined;
  if (!values || typeof values !== 'object') {
    return undefined;
  }
  for (const key of keys) {
    const v = values[key];
    if (v !== undefined && v !== null && v !== '') {
      return typeof v === 'object' ? JSON.stringify(v) : String(v);
    }
  }
  return undefined;
}

export function numberOrNull(value?: string): number | null {
  if (value === undefined) {
    return null;
  }
  const n = parseFloat(value);
  return isNaN(n) ? null : n;
}

// When the next periodic Inform is due, and whether it is late.
export function nextInform(
  lastInform: string | undefined,
  intervalSec: number,
  now: Date = new Date(),
): {due: Date | null; overdueSec: number} {
  const last = toDate(lastInform);
  if (!last) {
    return {due: null, overdueSec: 0};
  }
  const due = new Date(last.getTime() + intervalSec * 1000);
  return {
    due,
    overdueSec: Math.max(0, (now.getTime() - due.getTime()) / 1000),
  };
}

export function httpStatus(error: unknown): number | undefined {
  return (error as {response?: {status?: number}})?.response?.status;
}

export function errorMessage(error: unknown): string {
  const e = error as {
    response?: {status?: number; data?: unknown};
    message?: string;
  };
  const data = e?.response?.data;
  const detail =
    typeof data === 'string'
      ? data
      : (data as {message?: string} | undefined)?.message;
  if (e?.response?.status) {
    return `${e.response.status}${detail ? ` ${detail}` : ''}`;
  }
  return e?.message ?? 'unknown error';
}

// Absolute paths inside the ACS tool (the section is mounted at
// /nms/:networkId/acs).
export function acsPath(networkId: string, ...parts: Array<string>): string {
  return ['/nms', networkId, 'acs', ...parts.map(encodeURIComponent)].join('/');
}

export function cpePath(networkId: string, cpeKey: string, tab?: string) {
  return tab
    ? acsPath(networkId, 'cpe', cpeKey, tab)
    : acsPath(networkId, 'cpe', cpeKey);
}

export function formatUptime(sec?: number | null): string {
  if (!sec || sec <= 0) {
    return '—';
  }
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  return d ? `${d} d ${h} h` : h ? `${h} h ${m} min` : `${m} min`;
}
