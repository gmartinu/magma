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

// Render helpers for the ACS page tests: a router at an ACS URL plus the
// NMS theme. Not imported by app code.
import AcsAPI from './AcsAPI';
import MagmaAPI from '../../api/MagmaAPI';
import React from 'react';
import defaultTheme from '../../theme/default';
import {
  FIXTURE_CPES,
  FIXTURE_PARAMETERS,
  FIXTURE_SIGNAL,
  FIXTURE_TASKS,
  fixtureDetail,
} from './AcsFixtures';
import {MemoryRouter, Route, Routes} from 'react-router-dom';
import {StyledEngineProvider, ThemeProvider} from '@mui/material/styles';
import {networkKpiQuery} from './AcsMetrics';
import {render} from '@testing-library/react';

export function renderAt(
  url: string,
  routePath: string,
  element: React.ReactElement,
) {
  return render(
    <MemoryRouter initialEntries={[url]} initialIndex={0}>
      <StyledEngineProvider injectFirst>
        <ThemeProvider theme={defaultTheme}>
          <Routes>
            <Route path={routePath} element={element} />
          </Routes>
        </ThemeProvider>
      </StyledEngineProvider>
    </MemoryRouter>,
  );
}

function vector(label: string, values: Record<string, number>) {
  return {
    data: {
      status: 'success',
      data: {
        resultType: 'vector',
        result: Object.entries(values).map(([k, v]) => ({
          metric: {[label]: k},
          value: ['1790000000', String(v)],
        })),
      },
    },
  } as never;
}

// Prometheus answering acs_rsrp_dbm / acs_sinr_db from the fixtures and
// everything else empty.
export function mockPrometheus() {
  const rsrp: Record<string, number> = {};
  const sinr: Record<string, number> = {};
  Object.entries(FIXTURE_SIGNAL).forEach(([k, s]) => {
    rsrp[k] = s.rsrp;
    sinr[k] = s.sinr;
  });
  const instant = jest
    .spyOn(MagmaAPI.metrics, 'networksNetworkIdPrometheusQueryGet')
    .mockImplementation(({query}) =>
      Promise.resolve(
        query === networkKpiQuery('acs_rsrp_dbm')
          ? vector('cpe_key', rsrp)
          : query === networkKpiQuery('acs_sinr_db')
          ? vector('cpe_key', sinr)
          : vector('x', {}),
      ),
    );
  const range = jest
    .spyOn(MagmaAPI.metrics, 'networksNetworkIdPrometheusQueryRangeGet')
    .mockResolvedValue({
      data: {
        status: 'success',
        data: {
          resultType: 'matrix',
          result: [
            {
              metric: {},
              values: [
                ['1790000000', '-84'],
                ['1790000060', '-86'],
              ],
            },
          ],
        },
      },
    } as never);
  return {instant, range};
}

// The ACS API answering from the fixtures.
export function mockAcsAPI() {
  return {
    listCpes: jest
      .spyOn(AcsAPI, 'listCpes')
      .mockImplementation((_n, f) =>
        Promise.resolve(
          FIXTURE_CPES.filter(c => !f?.mode || c.mode === f.mode),
        ),
      ),
    getCpe: jest.spyOn(AcsAPI, 'getCpe').mockImplementation((_n, key) => {
      const d = fixtureDetail(key);
      return d ? Promise.resolve(d) : Promise.reject({response: {status: 404}});
    }),
    listTasks: jest.spyOn(AcsAPI, 'listTasks').mockResolvedValue(FIXTURE_TASKS),
    listParameters: jest
      .spyOn(AcsAPI, 'listParameters')
      .mockResolvedValue(FIXTURE_PARAMETERS),
    createTask: jest
      .spyOn(AcsAPI, 'createTask')
      .mockImplementation((_n, key, t) =>
        Promise.resolve({
          id: 't-new',
          cpe_key: key,
          type: t.type,
          status: 'pending',
          attempts: 0,
          max_attempts: 3,
          created: new Date().toISOString(),
          updated: new Date().toISOString(),
        }),
      ),
  };
}
