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
import OrganizationEntitlements, {
  toIso,
  toLocalInput,
} from '../OrganizationEntitlements';
import React from 'react';
import axios from 'axios';
import defaultTheme from '../../../theme/default';
import {StyledEngineProvider, ThemeProvider} from '@mui/material/styles';
import {fireEvent, render, waitFor, within} from '@testing-library/react';

const URL = '/host/organization/async/fwa/entitlements';

function renderCard() {
  return render(
    <StyledEngineProvider injectFirst>
      <ThemeProvider theme={defaultTheme}>
        <OrganizationEntitlements name="fwa" />
      </ThemeProvider>
    </StyledEngineProvider>,
  );
}

describe('OrganizationEntitlements', () => {
  const body = {
    features: [{feature: 'acs', title: 'ACS (TR-069 CPE management)'}],
    entitlements: [
      {
        feature: 'acs',
        enabled: true,
        state: 'grace',
        not_after: '2026-09-01T12:00:00Z',
        grace_days: 30,
      },
    ],
  };

  it('shows the state Orc8r computed and saves a change', async () => {
    const get = jest.spyOn(axios, 'get').mockResolvedValue({data: body});
    const put = jest.spyOn(axios, 'put').mockResolvedValue({status: 204});
    const {
      findByTestId,
      getByLabelText,
      getByTestId,
      getByText,
      findByText,
    } = renderCard();
    const row = await findByTestId('entitlement-acs');
    expect(get).toHaveBeenCalledWith(URL);
    expect(within(row).getByText('Grace period')).toBeInTheDocument();
    expect(getByTestId('acs-not-after')).toHaveValue(
      toLocalInput('2026-09-01T12:00:00Z'),
    );

    fireEvent.click(getByLabelText('acs enabled'));
    fireEvent.change(getByTestId('acs-not-after'), {target: {value: ''}});
    fireEvent.click(getByText('Save'));
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith(`${URL}/acs`, {
        enabled: false,
        not_after: null,
        grace_days: 30,
      }),
    );
    expect(await findByText(/acs saved/)).toBeInTheDocument();
    expect(get).toHaveBeenCalledTimes(2);
  });

  it('offers a feature the tenant has no entitlement to', async () => {
    jest
      .spyOn(axios, 'get')
      .mockResolvedValue({data: {...body, entitlements: []}});
    const {findByTestId, getByLabelText} = renderCard();
    const row = await findByTestId('entitlement-acs');
    expect(row).toHaveTextContent('Not entitled');
    expect(getByLabelText('acs enabled')).not.toBeChecked();
  });

  it('reports Orc8r errors', async () => {
    jest
      .spyOn(axios, 'get')
      .mockRejectedValue({response: {status: 503, data: {message: 'down'}}});
    const {findByText} = renderCard();
    expect(
      await findByText(/Couldn't read or save entitlements/),
    ).toBeInTheDocument();
  });

  it('converts between datetime-local and RFC 3339', () => {
    expect(toIso('')).toBeNull();
    expect(toLocalInput(null)).toBe('');
    const local = toLocalInput('2027-01-01T00:00:00Z');
    expect(toIso(local)).toBe('2027-01-01T00:00:00.000Z');
  });
});
