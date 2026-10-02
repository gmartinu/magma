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
import AcsClaims from './AcsClaims';
import AcsCpeDetail from './AcsCpeDetail';
import AcsCpeList from './AcsCpeList';
import AcsGatewayHealth from './AcsGatewayHealth';
import AcsSessionLog from './AcsSessionLog';
import CellWifiIcon from '@mui/icons-material/CellWifi';
import InboxIcon from '@mui/icons-material/Inbox';
import ListIcon from '@mui/icons-material/List';
import React from 'react';
import RouterIcon from '@mui/icons-material/Router';
import TopBar from '../../components/TopBar';
import nullthrows from '../../../shared/util/nullthrows';
import {AcsContent} from './AcsLayout';
import {Navigate, Route, Routes, useParams} from 'react-router-dom';

function NetworkSessionLog() {
  return <AcsSessionLog networkId={nullthrows(useParams().networkId)} />;
}

function AcsOverview() {
  return (
    <>
      <TopBar
        header="ACS"
        tabs={[
          {label: 'CPEs', to: 'cpes', icon: RouterIcon},
          {label: 'Claims', to: 'claims', icon: InboxIcon},
          {label: 'Gateways', to: 'gateways', icon: CellWifiIcon},
          {label: 'Session Log', to: 'sessions', icon: ListIcon},
        ]}
      />
      <AcsContent>
        <Routes>
          <Route path="/cpes" element={<AcsCpeList />} />
          <Route path="/claims" element={<AcsClaims />} />
          <Route path="/gateways" element={<AcsGatewayHealth />} />
          <Route path="/sessions" element={<NetworkSessionLog />} />
          <Route index element={<Navigate to="cpes" replace />} />
        </Routes>
      </AcsContent>
    </>
  );
}

export default function AcsDashboard() {
  return (
    <Routes>
      <Route path="/cpe/:cpeKey/*" element={<AcsCpeDetail />} />
      <Route path="/overview/*" element={<AcsOverview />} />
      <Route index element={<Navigate to="overview" replace />} />
    </Routes>
  );
}
