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
import Box from '@mui/material/Box';
import React from 'react';

// Page body under the TopBar, with the NMS dashboards' 24 px margin.
export function AcsContent({children}: {children: React.ReactNode}) {
  return <Box sx={{m: 3}}>{children}</Box>;
}
