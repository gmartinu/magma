#!/usr/bin/env bash
# Copyright 2026 The Magma Authors.

# This source code is licensed under the BSD-style license found in the
# LICENSE file in the root directory of this source tree.

# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Lists the CPEs of a network through obsidian, as an operator would.
# Defaults target the local docker-compose Orc8r (nginx on 9443) with the
# admin operator test cert generated under .cache/test_certs.
#
# Usage: curl_list_devices.sh [network_id]

set -euo pipefail

MAGMA_ROOT="${MAGMA_ROOT:-$(cd "$(dirname "$0")/../../.." && pwd)}"
CERTS_DIR="${CERTS_DIR:-${MAGMA_ROOT}/.cache/test_certs}"
API_URL="${API_URL:-https://localhost:9443}"
NETWORK_ID="${1:-test}"

curl --silent --show-error --fail-with-body --insecure \
  --cert "${CERTS_DIR}/admin_operator.pem" \
  --key "${CERTS_DIR}/admin_operator.key.pem" \
  "${API_URL}/magma/v1/acs/${NETWORK_ID}/devices"
echo
