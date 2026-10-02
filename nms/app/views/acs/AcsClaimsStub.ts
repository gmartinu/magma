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

// Isolated stand-in for the claims API Orc8r does not serve yet. The claim
// UI is wired to it so that enabling claims later means implementing these
// two calls (and flipping CLAIMS_API_AVAILABLE), nothing else. No route is
// guessed here on purpose.
import type {AcsClaim, AcsClaimRequest} from './AcsAPI';

export const CLAIMS_API_AVAILABLE = false;

export class ClaimsUnavailableError extends Error {
  constructor() {
    super('The Orchestrator has no claims API yet');
  }
}

const AcsClaimsAPI = {
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  listClaims(_networkId: string): Promise<Array<AcsClaim>> {
    return Promise.resolve([]);
  },
  /* eslint-disable @typescript-eslint/no-unused-vars */
  createClaim(_networkId: string, _claim: AcsClaimRequest): Promise<AcsClaim> {
    return Promise.reject(new ClaimsUnavailableError());
  },
  /* eslint-enable @typescript-eslint/no-unused-vars */
};

export default AcsClaimsAPI;
