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

// Thin typed client for the Orc8r ACS REST API (acs/cloud/go/services/acs,
// swagger.v1.yml). Kept by hand instead of regenerating nms/generated: the
// ACS swagger is not part of the combined spec the generator reads yet.
import axios from 'axios';

export const ACS_BASE_PATH = '/nms/apicontroller/magma/v1/acs';

export type AcsCpeMode = 'core' | 'claimed';

export type AcsTaskType =
  | 'reboot'
  | 'factory_reset'
  | 'refresh'
  | 'get_parameter_values'
  | 'set_parameter_values'
  | 'get_parameter_names';

export type AcsTaskStatus =
  | 'pending'
  | 'in_progress'
  | 'done'
  | 'failed'
  | 'expired';

export type AcsCpeSession = {
  session_id?: string;
  result?: string;
  reason?: string;
  started?: string;
  ended?: string;
  tasks_done?: number;
  tasks_failed?: number;
  faults?: number;
};

export type AcsCpe = {
  cpe_key: string;
  mode: AcsCpeMode;
  online: boolean;
  gateway_id: string;
  reported_at: string;
  imsi?: string;
  serial_number?: string;
  oui?: string;
  product_class?: string;
  model_name?: string;
  software_version?: string;
  handler?: string;
  last_inform?: string;
  informs_total?: number;
  pending_tasks?: number;
  last_session?: AcsCpeSession;
  hardware_id?: string;
  // How queued tasks reach the CPE, as acsd reports it; empty from an acsd
  // that does not say.
  reach?: AcsReach | '';
  reach_reason?: string;
  next_inform?: string;
  // Other gateways reporting this cpe_key online too.
  conflicting_gateway_ids?: Array<string>;
};

export type AcsReach = 'connection_request' | 'next_inform';

export type AcsConnectionRequest = {
  sent: boolean;
  reach?: AcsReach | '';
  reason?: string;
  next_inform?: string;
  detail?: string;
};

export type AcsLogEvent = 'cpe_session_completed' | 'cpe_task_failed';

// One record of the session log (GET /acs/{network_id}/logs).
export type AcsLog = {
  time: string;
  event: AcsLogEvent;
  cpe_key: string;
  imsi?: string;
  gateway_id?: string;
  hardware_id?: string;
  session_id?: string;
  result?: string;
  reason?: string;
  started?: string;
  ended?: string;
  tasks_done?: number;
  tasks_failed?: number;
  faults?: number;
  task_id?: string;
  task_type?: string;
  attempts?: number;
  max_attempts?: number;
  fault_code?: number;
  fault_string?: string;
};

export type AcsLogs = {total_count: number; logs: Array<AcsLog>};

export type AcsLogQuery = {
  cpe_key?: string;
  gateway_id?: string;
  event?: AcsLogEvent;
  start?: string;
  end?: string;
  size?: number;
  from?: number;
};

// The logs route refuses a bigger page.
export const ACS_LOGS_MAX_SIZE = 1000;

// Vendor-independent model acsd normalizes the parameter tree into
// (lte/gateway/python/magma/acsd/datamodel/model.py). Every section is
// optional: a CPE that was just claimed has none yet.
export type AcsCpeModel = {
  root?: string;
  uptime_sec?: number;
  identity?: Record<string, unknown>;
  firmware?: Record<string, unknown>;
  cellular?: Record<string, unknown>;
  wan?: Record<string, unknown>;
  lan?: Record<string, unknown>;
  wifi?: Record<string, unknown>;
  management_server?: Record<string, unknown>;
};

export type AcsCpeDetail = {
  cpe: AcsCpe;
  model?: AcsCpeModel;
};

export type AcsParameter = {name: string; value: string};

export type AcsParameterValue = {name: string; value: string; type?: string};

export type AcsTaskRequest = {
  type: AcsTaskType;
  parameter_names?: Array<string>;
  parameter_values?: Array<AcsParameterValue>;
  parameter_path?: string;
  next_level?: boolean;
  max_attempts?: number;
  ttl_sec?: number;
};

export type AcsTask = {
  id: string;
  cpe_key: string;
  type: AcsTaskType;
  status: AcsTaskStatus;
  attempts: number;
  max_attempts: number;
  created: string;
  updated: string;
  args?: Record<string, unknown>;
  result?: Record<string, unknown>;
  fault_code?: number;
  fault_string?: string;
  deadline?: string;
  imsi?: string;
};

// Claimed mode: the operator names a CPE ahead of time by its TR-069
// DeviceId; acsd keys it CLAIM<claim_id>. Orc8r serves no claims API yet
// (claims are added on the AGW with acsd_cli.py), so AcsClaimsStub.ts holds
// the client the claim UI will use; these types are what it will speak.
export type AcsClaim = {
  claim_id: string;
  oui: string;
  product_class: string;
  serial_number: string;
  label?: string;
  created?: string;
  cpe_key?: string;
};

export type AcsClaimRequest = {
  oui: string;
  product_class: string;
  serial_number: string;
  label?: string;
};

export type AcsCpeFilters = {
  model?: string;
  online?: boolean;
  gateway_id?: string;
  mode?: AcsCpeMode;
};

const enc = encodeURIComponent;

function cpeUrl(networkId: string, cpeKey: string) {
  return `${ACS_BASE_PATH}/${enc(networkId)}/cpes/${enc(cpeKey)}`;
}

const AcsAPI = {
  async listCpes(
    networkId: string,
    filters: AcsCpeFilters = {},
  ): Promise<Array<AcsCpe>> {
    const res = await axios.get<Array<AcsCpe>>(
      `${ACS_BASE_PATH}/${enc(networkId)}/cpes`,
      {params: filters},
    );
    return res.data ?? [];
  },

  async getCpe(networkId: string, cpeKey: string): Promise<AcsCpeDetail> {
    const res = await axios.get<AcsCpeDetail>(cpeUrl(networkId, cpeKey));
    return res.data;
  },

  async listParameters(
    networkId: string,
    cpeKey: string,
    prefix?: string,
  ): Promise<Array<AcsParameter>> {
    const res = await axios.get<Array<AcsParameter>>(
      `${cpeUrl(networkId, cpeKey)}/parameters`,
      {params: prefix ? {prefix} : {}},
    );
    return res.data ?? [];
  },

  async listTasks(networkId: string, cpeKey: string): Promise<Array<AcsTask>> {
    const res = await axios.get<Array<AcsTask>>(
      `${cpeUrl(networkId, cpeKey)}/tasks`,
    );
    return res.data ?? [];
  },

  async getTask(
    networkId: string,
    cpeKey: string,
    taskId: string,
  ): Promise<AcsTask> {
    const res = await axios.get<AcsTask>(
      `${cpeUrl(networkId, cpeKey)}/tasks/${enc(taskId)}`,
    );
    return res.data;
  },

  async searchLogs(networkId: string, query: AcsLogQuery): Promise<AcsLogs> {
    const res = await axios.get<AcsLogs>(
      `${ACS_BASE_PATH}/${enc(networkId)}/logs`,
      {params: query},
    );
    return res.data ?? {total_count: 0, logs: []};
  },

  async connectionRequest(
    networkId: string,
    cpeKey: string,
  ): Promise<AcsConnectionRequest> {
    const res = await axios.post<AcsConnectionRequest>(
      `${cpeUrl(networkId, cpeKey)}/connection_request`,
    );
    return res.data;
  },

  async createTask(
    networkId: string,
    cpeKey: string,
    task: AcsTaskRequest,
  ): Promise<AcsTask> {
    const res = await axios.post<AcsTask>(
      `${cpeUrl(networkId, cpeKey)}/tasks`,
      task,
    );
    return res.data;
  },
};

export default AcsAPI;
