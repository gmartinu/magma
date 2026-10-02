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

// Orc8r entitlements API (orc8r/cloud/go/services/entitlements, swagger at
// feature/acs-entitlements 440f6041cf). Not in nms/generated yet, so called directly
// with the NMS's Orc8r client certificate.
import axios from 'axios';
import https from 'https';
import nullthrows from '../../shared/util/nullthrows';
import {API_HOST, apiCredentials} from '../../config/config';

export type EntitlementState = 'active' | 'grace' | 'frozen' | 'disabled';

export type Entitlement = {
  tenant_id?: number;
  feature: string;
  enabled: boolean;
  not_after?: string | null;
  grace_days?: number;
  source?: 'manual' | 'license';
  license_id?: string;
  updated_at?: string;
  state?: EntitlementState;
};

export type NetworkEntitlements = {
  network_id: string;
  tenant_id?: number | null;
  enforce: boolean;
  entitlements: Array<Entitlement>;
};

let client: ReturnType<typeof axios.create> | null = null;

function orc8r() {
  if (!client) {
    const host = nullthrows(API_HOST);
    client = axios.create({
      baseURL: /^https?:\/\//.test(host)
        ? `${host}/magma/v1`
        : `https://${host}/magma/v1`,
      httpsAgent: new https.Agent({
        cert: apiCredentials().cert,
        key: apiCredentials().key,
        rejectUnauthorized: false,
      }),
      timeout: 10000,
    });
  }
  return client;
}

const enc = encodeURIComponent;

const OrchestratorEntitlements = {
  async network(networkId: string): Promise<NetworkEntitlements> {
    const res = await orc8r().get<NetworkEntitlements>(
      `/networks/${enc(networkId)}/entitlements`,
    );
    return res.data;
  },

  async tenant(tenantId: number): Promise<Array<Entitlement>> {
    const res = await orc8r().get<Array<Entitlement>>(
      `/tenants/${tenantId}/entitlements`,
    );
    return res.data ?? [];
  },

  async putTenantFeature(
    tenantId: number,
    entitlement: Entitlement,
  ): Promise<void> {
    await orc8r().put(
      `/tenants/${tenantId}/entitlements/${enc(entitlement.feature)}`,
      entitlement,
    );
  },
};

export default OrchestratorEntitlements;
