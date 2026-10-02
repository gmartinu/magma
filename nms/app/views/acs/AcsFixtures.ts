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

// Sample ACS data shaped like the Orc8r ACS API. Used by the jest tests and
// by scripts/mockServer.ts, so the ACS pages can be clicked through without
// an Orchestrator or a CPE.
import type {AcsCpe, AcsCpeDetail, AcsParameter, AcsTask} from './AcsAPI';

const NOW = Date.parse('2026-10-01T14:33:00Z');
const iso = (minutesAgo: number) =>
  new Date(NOW - minutesAgo * 60000).toISOString();

export const FIXTURE_CPES: Array<AcsCpe> = [
  {
    cpe_key: 'IMSI001010000000113',
    mode: 'core',
    imsi: 'IMSI001010000000113',
    serial_number: 'GT4K2409A00113',
    oui: 'D4A5C2',
    product_class: 'Titan4000',
    model_name: 'Titan 4000',
    software_version: 'GT4000_V1.4.18',
    handler: 'titan',
    last_inform: iso(1),
    informs_total: 1812,
    online: true,
    pending_tasks: 1,
    gateway_id: 'agw01',
    reported_at: iso(0),
    last_session: {
      session_id: 's-7f9a',
      result: 'completed',
      started: iso(1),
      ended: iso(1),
      tasks_done: 0,
      tasks_failed: 0,
      faults: 0,
    },
  },
  {
    cpe_key: 'IMSI001010000000187',
    mode: 'core',
    imsi: 'IMSI001010000000187',
    serial_number: 'GT4K2409A00187',
    oui: 'D4A5C2',
    product_class: 'Titan4000',
    model_name: 'Titan 4000',
    software_version: 'GT4000_V1.4.18',
    last_inform: iso(2),
    online: true,
    pending_tasks: 0,
    gateway_id: 'agw01',
    reported_at: iso(0),
  },
  {
    cpe_key: 'CLAIMlab-5400-01',
    mode: 'claimed',
    serial_number: 'GT54K2411B00342',
    oui: 'D4A5C2',
    product_class: 'Titan5400',
    model_name: 'Titan 5400',
    software_version: 'GT5400_V2.1.07',
    last_inform: iso(3),
    online: true,
    pending_tasks: 0,
    gateway_id: 'agw02',
    reported_at: iso(0),
  },
  {
    cpe_key: 'CLAIMlab-5400-02',
    mode: 'claimed',
    serial_number: 'GT54K2411B00377',
    oui: 'D4A5C2',
    product_class: 'Titan5400',
    model_name: 'Titan 5400',
    software_version: 'GT5400_V2.1.07',
    last_inform: iso(180),
    online: false,
    pending_tasks: 0,
    gateway_id: 'agw02',
    reported_at: iso(0),
  },
];

export const FIXTURE_SIGNAL: Record<string, {rsrp: number; sinr: number}> = {
  IMSI001010000000113: {rsrp: -84, sinr: 14.5},
  IMSI001010000000187: {rsrp: -104, sinr: -2.5},
  'CLAIMlab-5400-01': {rsrp: -76, sinr: 22.5},
};

export function fixtureDetail(cpeKey: string): AcsCpeDetail | undefined {
  const cpe = FIXTURE_CPES.find(c => c.cpe_key === cpeKey);
  if (!cpe) {
    return undefined;
  }
  const signal = FIXTURE_SIGNAL[cpeKey];
  return {
    cpe,
    model: {
      root: 'Device.',
      uptime_sec: 6 * 86400 + 4 * 3600,
      identity: {
        manufacturer: 'Global Telecom',
        oui: cpe.oui,
        product_class: cpe.product_class,
        serial_number: cpe.serial_number,
        model_name: cpe.model_name,
        hardware_version: 'HW-B2',
      },
      firmware: {software_version: cpe.software_version},
      cellular: {
        technology: 'NR',
        band: 'n41',
        pci: '312',
        cell_id: '439534593',
        rsrp: signal?.rsrp,
        rsrq: -10,
        sinr: signal?.sinr,
        imei: '358927104488126',
        apn: 'fwa.lab',
        operator: '315010',
      },
      wan: {ipv4_address: '100.72.14.33', ipv6_address: '2600:380:8d1a::2f'},
      management_server: {
        url: 'http://10.0.2.1:7547/',
        username: `${cpe.oui ?? ''}-${cpe.serial_number ?? ''}`,
        periodic_inform_enable: true,
        periodic_inform_interval: 300,
      },
    },
  };
}

export const FIXTURE_PARAMETERS: Array<AcsParameter> = [
  ['Device.Cellular.Interface.1.Status', 'Up'],
  ['Device.Cellular.Interface.1.CurrentAccessTechnology', 'NR'],
  ['Device.Cellular.Interface.1.RSRP', '-84'],
  ['Device.Cellular.Interface.1.RSRQ', '-10'],
  ['Device.Cellular.Interface.1.X_GT_SINR', '14.5'],
  ['Device.Cellular.AccessPoint.1.APN', 'fwa.lab'],
  ['Device.DeviceInfo.SoftwareVersion', 'GT4000_V1.4.18'],
  ['Device.DeviceInfo.UpTime', '533000'],
  ['Device.ManagementServer.PeriodicInformInterval', '300'],
  ['Device.WiFi.SSID.1.SSID', 'fwa-lab-113'],
].map(([name, value]) => ({name, value}));

export const FIXTURE_TASKS: Array<AcsTask> = [
  {
    id: 't-1',
    cpe_key: 'IMSI001010000000113',
    type: 'factory_reset',
    status: 'expired',
    attempts: 0,
    max_attempts: 3,
    created: iso(60 * 24 * 9),
    updated: iso(60 * 24 * 2),
    deadline: iso(60 * 24 * 2),
  },
  {
    id: 't-2',
    cpe_key: 'IMSI001010000000113',
    type: 'refresh',
    status: 'failed',
    attempts: 3,
    max_attempts: 3,
    fault_code: 9002,
    fault_string: 'Internal error',
    created: iso(60 * 22),
    updated: iso(60 * 21),
  },
  {
    id: 't-3',
    cpe_key: 'IMSI001010000000113',
    type: 'refresh',
    status: 'done',
    attempts: 1,
    max_attempts: 3,
    result: {values: {'Device.DeviceInfo.UpTime': '533000'}},
    created: iso(21),
    updated: iso(18),
  },
  {
    id: 't-4',
    cpe_key: 'IMSI001010000000113',
    type: 'reboot',
    status: 'pending',
    attempts: 0,
    max_attempts: 3,
    created: iso(2),
    updated: iso(2),
    deadline: iso(-60 * 24 * 7),
  },
];
