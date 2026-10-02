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

// requireEntitlement(feature) gates an Orc8r proxy route on the tenant's
// entitlement to a licensed feature, read from Orc8r per network and cached
// for a minute. Orc8r stays the authority (it enforces too); this lets the
// NMS answer a clear 403 instead of proxying calls that will be refused.
import OrchestratorEntitlements, {
  NetworkEntitlements,
} from '../api/OrchestratorEntitlements';
import logging from '../../shared/logging';
import {NextFunction, Request, Response} from 'express';

const logger = logging.getLogger(module);

export const ENTITLEMENT_CACHE_MS = 60 * 1000;
const READ_METHODS = ['GET', 'HEAD', 'OPTIONS'];

type Decision = {allowed: boolean; readOnly: boolean; reason: string};

const cache = new Map<
  string,
  {at: number; value: NetworkEntitlements | null}
>();

export function clearEntitlementCache() {
  cache.clear();
}

// null: this Orc8r has no entitlements service (404), so nothing to enforce.
async function lookup(
  networkId: string,
  now: number,
): Promise<NetworkEntitlements | null> {
  const hit = cache.get(networkId);
  if (hit && now - hit.at < ENTITLEMENT_CACHE_MS) {
    return hit.value;
  }
  let value: NetworkEntitlements | null;
  try {
    value = await OrchestratorEntitlements.network(networkId);
  } catch (e) {
    if ((e as {response?: {status?: number}})?.response?.status !== 404) {
      throw e;
    }
    value = null;
  }
  cache.set(networkId, {at: now, value});
  return value;
}

export function decide(
  feature: string,
  ents: NetworkEntitlements | null,
): Decision {
  if (!ents || !ents.enforce) {
    return {allowed: true, readOnly: false, reason: 'not enforced'};
  }
  const e = ents.entitlements.find(x => x.feature === feature);
  if (!e) {
    return {allowed: false, readOnly: false, reason: 'not entitled'};
  }
  switch (e.state) {
    case 'active':
    case 'grace':
      return {allowed: true, readOnly: false, reason: e.state};
    case 'frozen':
      return {allowed: true, readOnly: true, reason: 'expired (frozen)'};
    case 'disabled':
      return {allowed: false, readOnly: false, reason: 'disabled'};
    default:
      // An Orc8r that does not compute state: trust enabled + not_after.
      if (!e.enabled) {
        return {allowed: false, readOnly: false, reason: 'disabled'};
      }
      if (e.not_after && Date.parse(e.not_after) < Date.now()) {
        return {allowed: true, readOnly: true, reason: 'expired'};
      }
      return {allowed: true, readOnly: false, reason: 'active'};
  }
}

export default function requireEntitlement(feature: string) {
  return (req: Request, res: Response, next: NextFunction) => {
    const networkId = (req.params as Record<string, string | undefined>)
      .networkID;
    if (!networkId || process.env.NMS_ENTITLEMENT_CHECK === 'off') {
      next();
      return;
    }
    lookup(networkId, Date.now())
      .then(ents => {
        const d = decide(feature, ents);
        if (!d.allowed) {
          res.status(403).send({
            message: `${feature} is not enabled for this organization (${d.reason})`,
            feature,
          });
        } else if (d.readOnly && !READ_METHODS.includes(req.method)) {
          res.status(403).send({
            message: `${feature} is read-only: the entitlement ${d.reason}`,
            feature,
          });
        } else {
          next();
        }
      })
      .catch(err => {
        logger.error(
          `entitlement check for ${networkId} failed: ${String(err)}`,
        );
        res.status(503).send({
          message: `Could not verify the ${feature} entitlement`,
          feature,
        });
      });
  };
}
