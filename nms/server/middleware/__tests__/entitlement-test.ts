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
import OrchestratorEntitlements, {
  NetworkEntitlements,
} from '../../api/OrchestratorEntitlements';
import express from 'express';
import request from 'supertest';
import requireEntitlement, {
  ENTITLEMENT_CACHE_MS,
  clearEntitlementCache,
  decide,
} from '../entitlementMiddleware';

jest.mock('../../api/OrchestratorEntitlements', () => ({
  __esModule: true,
  default: {network: jest.fn()},
}));

const network = OrchestratorEntitlements.network as jest.Mock;

function app() {
  return express().use(
    '/acs/:networkID',
    requireEntitlement('acs'),
    (_req, res) => res.status(200).send('proxied'),
  );
}

function ents(state?: string, extra: object = {}): NetworkEntitlements {
  return {
    network_id: 'net1',
    tenant_id: 1,
    enforce: true,
    entitlements: state
      ? [
          {
            feature: 'acs',
            enabled: state !== 'disabled',
            state: state as 'active',
            ...extra,
          },
        ]
      : [],
  };
}

describe('requireEntitlement', () => {
  beforeEach(() => {
    clearEntitlementCache();
    delete process.env.NMS_ENTITLEMENT_CHECK;
  });

  it('proxies an active or grace entitlement, any method', async () => {
    network.mockResolvedValue(ents('active'));
    await request(app()).get('/acs/net1/cpes').expect(200, 'proxied');
    await request(app()).post('/acs/net1/cpes/IMSI1/tasks').expect(200);
    clearEntitlementCache();
    network.mockResolvedValue(ents('grace'));
    await request(app()).post('/acs/net1/cpes/IMSI1/tasks').expect(200);
  });

  it('allows only reads past the grace period (frozen)', async () => {
    network.mockResolvedValue(ents('frozen'));
    await request(app()).get('/acs/net1/cpes').expect(200);
    const res = await request(app())
      .post('/acs/net1/cpes/IMSI1/tasks')
      .expect(403);
    expect((res.body as {message: string}).message).toMatch(/read-only/);
  });

  it('refuses a disabled or missing entitlement with 403', async () => {
    network.mockResolvedValue(ents('disabled'));
    await request(app()).get('/acs/net1/cpes').expect(403);
    clearEntitlementCache();
    network.mockResolvedValue(ents());
    const res = await request(app()).get('/acs/net1/cpes').expect(403);
    expect(res.body).toEqual({
      message: 'acs is not enabled for this organization (not entitled)',
      feature: 'acs',
    });
  });

  it('lets everything through when Orc8r does not enforce', async () => {
    network.mockResolvedValue({...ents(), enforce: false});
    await request(app()).post('/acs/net1/cpes/IMSI1/tasks').expect(200);
  });

  it('treats an Orc8r without the entitlements service as not enforcing', async () => {
    network.mockRejectedValue({response: {status: 404}});
    await request(app()).get('/acs/net1/cpes').expect(200);
  });

  it('answers 503 when the entitlement cannot be read', async () => {
    network.mockRejectedValue({response: {status: 500}});
    await request(app()).get('/acs/net1/cpes').expect(503);
  });

  it('caches per network for a minute', async () => {
    const now = jest.spyOn(Date, 'now').mockReturnValue(1000);
    network.mockResolvedValue(ents('active'));
    await request(app()).get('/acs/net1/cpes').expect(200);
    await request(app()).get('/acs/net1/cpes/IMSI1').expect(200);
    await request(app()).get('/acs/net2/cpes').expect(200);
    expect(network).toHaveBeenCalledTimes(2);
    now.mockReturnValue(1000 + ENTITLEMENT_CACHE_MS);
    await request(app()).get('/acs/net1/cpes').expect(200);
    expect(network).toHaveBeenCalledTimes(3);
  });

  it('can be switched off for development', async () => {
    process.env.NMS_ENTITLEMENT_CHECK = 'off';
    await request(app()).get('/acs/net1/cpes').expect(200);
    expect(network).not.toHaveBeenCalled();
  });

  it('falls back to enabled and not_after without a server state', () => {
    const past = '2020-01-01T00:00:00Z';
    expect(decide('acs', ents(undefined)).allowed).toBe(false);
    const noState = (enabled: boolean, not_after?: string) => ({
      ...ents(),
      entitlements: [{feature: 'acs', enabled, not_after}],
    });
    expect(decide('acs', noState(true))).toMatchObject({
      allowed: true,
      readOnly: false,
    });
    expect(decide('acs', noState(true, past))).toMatchObject({readOnly: true});
    expect(decide('acs', noState(false)).allowed).toBe(false);
  });
});
