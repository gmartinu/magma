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
import AcsAPI from '../AcsAPI';
import AcsClaims, {ClaimDialog, claimErrors} from '../AcsClaims';
import AcsCpeList, {filterCpes} from '../AcsCpeList';
import React from 'react';
import {FIXTURE_CPES} from '../AcsFixtures';
import {fireEvent, render, waitFor, within} from '@testing-library/react';
import {mockAcsAPI, mockPrometheus, renderAt} from '../AcsTestUtils';

const LIST_URL = '/nms/net1/acs/overview/cpes';
const LIST_ROUTE = '/nms/:networkId/acs/overview/cpes';

describe('AcsCpeList', () => {
  it('lists CPEs with mode, signal and pending tasks', async () => {
    mockAcsAPI();
    mockPrometheus();
    const {getAllByTestId, getByTestId, getByText} = renderAt(
      LIST_URL,
      LIST_ROUTE,
      <AcsCpeList />,
    );
    await waitFor(() => expect(getAllByTestId('acs-cpe-row')).toHaveLength(4));

    const first = getAllByTestId('acs-cpe-row')[0];
    expect(within(first).getByText('GT4K2409A00113')).toHaveAttribute(
      'href',
      '/nms/net1/acs/cpe/IMSI001010000000113',
    );
    expect(within(first).getByText('Core')).toBeInTheDocument();
    expect(within(first).getByText('-84 dBm · 14.5 dB')).toBeInTheDocument();
    expect(within(first).getByText('1 pending')).toBeInTheDocument();
    expect(getAllByTestId('acs-cpe-row')[2]).toHaveTextContent('Claimed');
    expect(getAllByTestId('acs-cpe-row')[3]).toHaveTextContent('no KPI yet');

    const kpis = getByTestId('acs-kpis');
    expect(kpis).toHaveTextContent('CPEs4');
    expect(kpis).toHaveTextContent('Online3');
    expect(kpis).toHaveTextContent('Offline1');
    expect(kpis).toHaveTextContent('Weak signal (RSRP < -100 dBm)1');
    expect(kpis).toHaveTextContent('Tasks waiting for Inform1');
    expect(getByText('CPEs (4 of 4)')).toBeInTheDocument();
  });

  it('filters by search and clears filters', async () => {
    mockAcsAPI();
    mockPrometheus();
    const {getAllByTestId, getByTestId, getByText, queryAllByTestId} = renderAt(
      LIST_URL,
      LIST_ROUTE,
      <AcsCpeList />,
    );
    await waitFor(() => expect(getAllByTestId('acs-cpe-row')).toHaveLength(4));
    fireEvent.change(getByTestId('acs-search'), {target: {value: 'GT54K'}});
    expect(getAllByTestId('acs-cpe-row')).toHaveLength(2);
    fireEvent.change(getByTestId('acs-search'), {target: {value: 'nothing'}});
    expect(queryAllByTestId('acs-cpe-row')).toHaveLength(0);
    fireEvent.click(getByText('Clear filters'));
    expect(getAllByTestId('acs-cpe-row')).toHaveLength(4);
  });

  it('filters by model, status and mode', () => {
    const none = {search: '', model: '', status: '', mode: ''} as const;
    expect(filterCpes(FIXTURE_CPES, {...none, mode: 'claimed'})).toHaveLength(
      2,
    );
    expect(filterCpes(FIXTURE_CPES, {...none, status: 'offline'})).toHaveLength(
      1,
    );
    expect(
      filterCpes(FIXTURE_CPES, {...none, model: 'Titan 4000'}),
    ).toHaveLength(2);
    expect(filterCpes(FIXTURE_CPES, {...none, search: '000187'})).toHaveLength(
      1,
    );
  });

  it('keeps the list when Prometheus is down', async () => {
    mockAcsAPI();
    const {instant} = mockPrometheus();
    instant.mockRejectedValue(new Error('prometheus down'));
    const {getAllByTestId} = renderAt(LIST_URL, LIST_ROUTE, <AcsCpeList />);
    await waitFor(() => expect(getAllByTestId('acs-cpe-row')).toHaveLength(4));
    expect(getAllByTestId('acs-cpe-row')[0]).toHaveTextContent('no KPI yet');
  });

  it('explains how CPEs arrive when there are none', async () => {
    jest.spyOn(AcsAPI, 'listCpes').mockResolvedValue([]);
    mockPrometheus();
    const {findByText} = renderAt(LIST_URL, LIST_ROUTE, <AcsCpeList />);
    expect(await findByText('No CPEs in net1 yet')).toBeInTheDocument();
    expect(await findByText('Go to Claims')).toBeInTheDocument();
  });

  it('shows the API error with a retry', async () => {
    const list = jest
      .spyOn(AcsAPI, 'listCpes')
      .mockRejectedValue({response: {status: 503, data: 'unreachable'}});
    mockPrometheus();
    const {findByText, getByText} = renderAt(
      LIST_URL,
      LIST_ROUTE,
      <AcsCpeList />,
    );
    expect(await findByText("Couldn't load CPEs.")).toBeInTheDocument();
    expect(getByText(/503 unreachable/)).toBeInTheDocument();
    fireEvent.click(getByText('Retry'));
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
  });
});

describe('AcsClaims', () => {
  it('lists the claimed CPEs and keeps claiming behind "coming soon"', async () => {
    mockAcsAPI();
    const {getAllByTestId, getByTestId, getByText} = renderAt(
      '/nms/net1/acs/overview/claims',
      '/nms/:networkId/acs/overview/claims',
      <AcsClaims />,
    );
    await waitFor(() =>
      expect(getAllByTestId('acs-claim-row')).toHaveLength(2),
    );
    expect(getByTestId('claims-coming-soon')).toBeInTheDocument();
    expect(getByText('Claimed CPEs (2)')).toBeInTheDocument();
    expect(getAllByTestId('acs-claim-row')[0]).toHaveTextContent(
      'CLAIMlab-5400-01',
    );
    expect(getByText('New claim').closest('button')).toBeDisabled();
    expect(AcsAPI.listCpes).toHaveBeenCalledWith('net1', {mode: 'claimed'});
  });

  it('validates a claim by OUI, product class and serial', () => {
    expect(
      claimErrors({oui: 'D4A5C2', product_class: 'T', serial_number: 'S'}),
    ).toEqual({});
    expect(
      Object.keys(
        claimErrors({oui: 'XYZ', product_class: '', serial_number: ''}),
      ),
    ).toEqual(['oui', 'product_class', 'serial_number']);
  });

  it('reports why a claim was not saved', async () => {
    const onClaimed = jest.fn();
    const {getByTestId, getByText, findByTestId} = render(
      <ClaimDialog
        networkId="net1"
        open
        onClose={jest.fn()}
        onClaimed={onClaimed}
      />,
    );
    fireEvent.change(getByTestId('claim-oui'), {target: {value: 'd4a5c2'}});
    fireEvent.change(getByTestId('claim-product_class'), {
      target: {value: 'Titan5400'},
    });
    fireEvent.change(getByTestId('claim-serial_number'), {
      target: {value: 'GT54K'},
    });
    fireEvent.click(getByText('Claim into net1'));
    expect(await findByTestId('claim-error')).toHaveTextContent(
      'The Orchestrator has no claims API yet',
    );
    expect(onClaimed).not.toHaveBeenCalled();
  });
});
