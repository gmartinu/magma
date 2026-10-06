#!/bin/bash
# Copyright 2026 The Magma Authors.

# This source code is licensed under the BSD-style license found in the
# LICENSE file in the root directory of this source tree.

# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Regenerate the Python protos from the bind-mounted source tree into /out,
# on top of the image's own /build/gen (which carries the package
# __init__.py files). Needed because the prebuilt image predates the AcsD
# mconfig and any other proto the fork adds.
set -euo pipefail
cp -a /build/gen/. /out/
cd /magma
mapfile -t PROTOS < <(find lte/protos orc8r/protos feg/protos dp/protos \
  -name '*.proto' -not -path '*/prometheus/*')
python3 -m grpc_tools.protoc -I /magma -I /magma/orc8r/protos/prometheus \
  --python_out=/out --grpc_python_out=/out "${PROTOS[@]}"
# acsd's CpeManager API (lte/protos/cpe_acs.proto) is new in the fork; the
# smoke test and acsd itself import these stubs.
test -s /out/lte/protos/cpe_acs_pb2_grpc.py
# eventd loads event schemas from the <module>.swagger.specs packages, which
# the image's /build/gen predates for acsd's events.
for m in lte orc8r; do
  mkdir -p /out/$m/swagger/specs && touch /out/$m/swagger/specs/__init__.py
  cp /magma/$m/swagger/*.yml /out/$m/swagger/specs/
done
echo "generated ${#PROTOS[@]} protos into /out"
