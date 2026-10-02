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
import OrchestratorEntitlements from '../../api/OrchestratorEntitlements';
import appMiddleware from '../../middleware/appMiddleware';
import express from 'express';
import request from 'supertest';
import router from '../entitlementRoutes';
import {Organization} from '../../../shared/sequelize_models';

jest.mock('../../api/OrchestratorEntitlements', () => ({
  __esModule: true,
  default: {tenant: jest.fn(), putTenantFeature: jest.fn()},
}));

const tenant = OrchestratorEntitlements.tenant as jest.Mock;
const put = OrchestratorEntitlements.putTenantFeature as jest.Mock;

describe('Host entitlement routes', () => {
  const app = express().use(appMiddleware()).use('', router);
  let orgId = 0;

  beforeEach(async () => {
    await Organization.sync();
    const org = await Organization.create({
      name: 'fwa',
      networkIDs: ['net1'],
      customDomains: [],
      csvCharset: '',
      ssoCert: '',
      ssoEntrypoint: '',
      ssoIssuer: '',
    });
    orgId = org.id;
  });

  afterEach(async () => {
    await Organization.drop();
  });

  it('lists the tenant entitlements with the features offered', async () => {
    tenant.mockResolvedValue([
      {
        feature: 'acs',
        enabled: true,
        state: 'grace',
        not_after: '2026-09-01T00:00:00Z',
      },
    ]);
    const res = await request(app)
      .get('/organization/async/FWA/entitlements')
      .expect(200);
    expect(tenant).toHaveBeenCalledWith(orgId);
    expect(res.body).toEqual({
      features: [{feature: 'acs', title: 'ACS (TR-069 CPE management)'}],
      entitlements: [
        {
          feature: 'acs',
          enabled: true,
          state: 'grace',
          not_after: '2026-09-01T00:00:00Z',
        },
      ],
    });
  });

  it('sets one feature on the tenant', async () => {
    put.mockResolvedValue(undefined);
    await request(app)
      .put('/organization/async/fwa/entitlements/acs')
      .send({enabled: true, not_after: '2027-01-01T00:00:00Z', grace_days: 15})
      .expect(204);
    expect(put).toHaveBeenCalledWith(orgId, {
      feature: 'acs',
      enabled: true,
      not_after: '2027-01-01T00:00:00Z',
      grace_days: 15,
      source: 'manual',
    });
    await request(app)
      .put('/organization/async/fwa/entitlements/acs')
      .send({enabled: false, not_after: ''})
      .expect(204);
    expect(put).toHaveBeenLastCalledWith(orgId, {
      feature: 'acs',
      enabled: false,
      not_after: null,
      source: 'manual',
    });
  });

  it('rejects unknown features, bad bodies and unknown organizations', async () => {
    await request(app)
      .put('/organization/async/fwa/entitlements/nope')
      .send({enabled: true})
      .expect(400);
    await request(app)
      .put('/organization/async/fwa/entitlements/acs')
      .send({enabled: 'yes'})
      .expect(400);
    await request(app)
      .get('/organization/async/ghost/entitlements')
      .expect(404);
    expect(put).not.toHaveBeenCalled();
  });

  it('passes Orc8r errors through', async () => {
    tenant.mockRejectedValue({response: {status: 503, data: 'down'}});
    await request(app).get('/organization/async/fwa/entitlements').expect(503);
  });
});
