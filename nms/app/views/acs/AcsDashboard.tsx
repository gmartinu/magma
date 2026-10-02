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
import InboxIcon from '@mui/icons-material/Inbox';
import React from 'react';
import RouterIcon from '@mui/icons-material/Router';
import TopBar from '../../components/TopBar';
import {AcsContent} from './AcsLayout';
import {Navigate, Route, Routes} from 'react-router-dom';

function AcsOverview() {
  return (
    <>
      <TopBar
        header="ACS"
        tabs={[
          {label: 'CPEs', to: 'cpes', icon: RouterIcon},
          {label: 'Claims', to: 'claims', icon: InboxIcon},
        ]}
      />
      <AcsContent>
        <Routes>
          <Route path="/cpes" element={<AcsCpeList />} />
          <Route path="/claims" element={<AcsClaims />} />
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
