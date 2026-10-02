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

// Host portal: an organization's entitlements, kept in Orc8r on the tenant
// with the organization's id (see util/tenantsSync).
import OrchestratorEntitlements, {
  Entitlement,
} from '../api/OrchestratorEntitlements';
import Sequelize from 'sequelize';
import asyncHandler from '../util/asyncHandler';
import {Organization} from '../../shared/sequelize_models';
import {Request, Response, Router} from 'express';

// Licensed features the host portal offers to toggle.
export const ENTITLED_FEATURES = [
  {feature: 'acs', title: 'ACS (TR-069 CPE management)'},
];

const router = Router();

function findOrganization(name: string) {
  return Organization.findOne({
    where: Sequelize.where(
      Sequelize.fn('lower', Sequelize.col('name')),
      Sequelize.fn('lower', name),
    ),
  });
}

function sendOrc8rError(res: Response, e: unknown) {
  const err = e as {response?: {status?: number; data?: unknown}};
  const status = err.response?.status ?? 503;
  res.status(status >= 400 ? status : 503).send({
    message: 'Orchestrator entitlements request failed',
    detail: err.response?.data ?? String(e),
  });
}

router.get(
  '/organization/async/:name/entitlements',
  asyncHandler(async (req: Request<{name: string}>, res) => {
    const organization = await findOrganization(req.params.name);
    if (!organization) {
      return res.status(404).send({message: 'Organization does not exist'});
    }
    try {
      const entitlements = await OrchestratorEntitlements.tenant(
        organization.id,
      );
      res.status(200).send({features: ENTITLED_FEATURES, entitlements});
    } catch (e) {
      sendOrc8rError(res, e);
    }
  }),
);

router.put(
  '/organization/async/:name/entitlements/:feature',
  asyncHandler(
    async (
      req: Request<{name: string; feature: string}, any, Partial<Entitlement>>,
      res,
    ) => {
      const {feature} = req.params;
      if (!ENTITLED_FEATURES.some(f => f.feature === feature)) {
        return res.status(400).send({message: `Unknown feature ${feature}`});
      }
      if (typeof req.body.enabled !== 'boolean') {
        return res.status(400).send({message: 'enabled must be a boolean'});
      }
      const organization = await findOrganization(req.params.name);
      if (!organization) {
        return res.status(404).send({message: 'Organization does not exist'});
      }
      const entitlement: Entitlement = {
        feature,
        enabled: req.body.enabled,
        not_after: req.body.not_after || null,
        source: 'manual',
      };
      if (typeof req.body.grace_days === 'number') {
        entitlement.grace_days = req.body.grace_days;
      }
      try {
        await OrchestratorEntitlements.putTenantFeature(
          organization.id,
          entitlement,
        );
        res.status(204).send();
      } catch (e) {
        sendOrc8rError(res, e);
      }
    },
  ),
);

export default router;
