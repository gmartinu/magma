#!/usr/bin/env bash
# Copyright 2026 The Magma Authors.
#
# This source code is licensed under the BSD-style license found in the
# LICENSE file in the root directory of this source tree.
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Idempotent setup of the ACS e2e env, run on the host after
# `docker compose up -d` here: writes the dev gateway identity into .agw/
# and registers, through the Orc8r REST API, an LTE network, a tier, that
# gateway, and a tenant entitled to acs.
set -euo pipefail
cd "$(dirname "$0")"

NETWORK=${NETWORK:-acs_e2e}
GATEWAY=${GATEWAY:-agw1}
# Not 1: the NMS syncs its host organization to tenant 1.
TENANT=${TENANT:-100}
API=https://localhost:9443/magma/v1

# The dev gateway identity: a snowflake (hardware ID), a challenge key, the
# Orc8r root CA, and a throwaway cert for control_proxy's local listener.
mkdir -p .agw/certs
[[ -s .agw/snowflake ]] || uuidgen | tr 'A-Z' 'a-z' > .agw/snowflake
until [[ -s .certs/rootCA.pem && -s .certs/admin_operator.pem ]]; do
  echo "waiting for the Orc8r test certs..."; sleep 3
done
cp .certs/rootCA.pem .agw/certs/rootCA.pem
[[ -s .agw/certs/gw_challenge.key ]] ||
  openssl ecparam -name secp384r1 -genkey -noout -out .agw/certs/gw_challenge.key
[[ -s .agw/certs/controller.crt ]] ||
  openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -subj /CN=control_proxy.local \
    -keyout .agw/certs/controller.key -out .agw/certs/controller.crt 2>/dev/null
HWID=$(cat .agw/snowflake)
KEY=$(openssl ec -in .agw/certs/gw_challenge.key -pubout -outform DER 2>/dev/null | base64 | tr -d '\n')

api() {  # api METHOD PATH [JSON]; prints the HTTP status
  curl -sk -o /tmp/acs-e2e-api.out -w '%{http_code}' \
    --cert .certs/admin_operator.pem --key .certs/admin_operator.key.pem \
    -H 'Content-Type: application/json' -X "$1" "$API$2" ${3:+-d "$3"}
}
ensure() {  # ensure WHAT GET_PATH POST_PATH JSON
  if [[ $(api GET "$2") == 200 ]]; then echo "$1: exists"; return; fi
  local code; code=$(api POST "$3" "$4")
  [[ $code == 201 || $code == 200 ]] || { echo "$1: HTTP $code $(cat /tmp/acs-e2e-api.out)"; exit 1; }
  echo "$1: created"
}

until [[ $(api GET /networks) == 200 ]]; do echo "waiting for the Orc8r API..."; sleep 3; done

ensure "network $NETWORK" "/lte/$NETWORK" /lte '{
  "id": "'"$NETWORK"'", "name": "ACS e2e", "description": "ACS e2e dev network",
  "dns": {"enable_caching": false, "local_ttl": 0, "records": []},
  "cellular": {
    "epc": {"lte_auth_amf": "gAA=", "lte_auth_op": "EREREREREREREREREREREQ==", "mcc": "001", "mnc": "01",
            "tac": 1, "hss_relay_enabled": false, "gx_gy_relay_enabled": false},
    "ran": {"bandwidth_mhz": 20, "tdd_config": {"earfcndl": 44590, "special_subframe_pattern": 7, "subframe_assignment": 2}}
  }}'
ensure "tier default" "/networks/$NETWORK/tiers/default" "/networks/$NETWORK/tiers" \
  '{"id": "default", "version": "0.0.0-0", "images": [], "gateways": []}'
ensure "gateway $GATEWAY ($HWID)" "/lte/$NETWORK/gateways/$GATEWAY" "/lte/$NETWORK/gateways" '{
  "id": "'"$GATEWAY"'", "name": "acs-dev AGW", "description": "acs-dev compose", "tier": "default",
  "device": {"hardware_id": "'"$HWID"'", "key": {"key_type": "SOFTWARE_ECDSA_SHA256", "key": "'"$KEY"'"}},
  "magmad": {"autoupgrade_enabled": false, "autoupgrade_poll_interval": 300, "checkin_interval": 15, "checkin_timeout": 10},
  "cellular": {"epc": {"ip_block": "192.168.128.0/24", "nat_enabled": true}, "ran": {"pci": 260, "transmit_enabled": false}},
  "connected_enodeb_serials": []}'
ensure "tenant $TENANT" "/tenants/$TENANT" /tenants \
  '{"id": '"$TENANT"', "name": "acs-e2e", "networks": ["'"$NETWORK"'"]}'
NOT_AFTER=$(python3 -c 'import datetime as d; print((d.datetime.now(d.timezone.utc) + d.timedelta(days=365)).strftime("%Y-%m-%dT%H:%M:%SZ"))')
code=$(api PUT "/tenants/$TENANT/entitlements/acs" '{"feature": "acs", "enabled": true, "not_after": "'"$NOT_AFTER"'"}')
[[ $code == 200 || $code == 201 || $code == 204 ]] || { echo "entitlement acs: HTTP $code $(cat /tmp/acs-e2e-api.out)"; exit 1; }
echo "entitlement acs: enabled until $NOT_AFTER"
echo "network=$NETWORK gateway=$GATEWAY hardware_id=$HWID tenant=$TENANT"
