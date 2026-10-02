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
import AcsActions, {FactoryResetDialog} from '../AcsActions';
import AcsCpeDetail from '../AcsCpeDetail';
import React from 'react';
import {FIXTURE_CPES} from '../AcsFixtures';
import {fireEvent, render, waitFor} from '@testing-library/react';
import {mockAcsAPI, mockPrometheus, renderAt} from '../AcsTestUtils';

jest.mock('react-chartjs-2', () => ({
  Line: (props: {data: {datasets: Array<{data: Array<unknown>}>}}) => (
    <div data-testid="line">{props.data.datasets[0].data.length} points</div>
  ),
}));

const ROUTE = '/nms/:networkId/acs/cpe/:cpeKey/*';
const url = (key: string) => `/nms/net1/acs/cpe/${key}/overview`;

describe('AcsCpeDetail', () => {
  it('shows identity, state cards, KPI charts and the pending banner', async () => {
    mockAcsAPI();
    const {range} = mockPrometheus();
    const {findByTestId, getByTestId, getAllByTestId, getByText} = renderAt(
      url('IMSI001010000000113'),
      ROUTE,
      <AcsCpeDetail />,
    );
    const header = await findByTestId('acs-detail-header');
    expect(header).toHaveTextContent('GT4K2409A00113');
    expect(header).toHaveTextContent('D4A5C2-Titan4000-GT4K2409A00113');
    expect(header).toHaveTextContent('IMSI001010000000113');
    expect(header).toHaveTextContent('agw01');
    expect(header).toHaveTextContent('6 d 4 h');
    expect(getByTestId('acs-pending-banner')).toHaveTextContent(
      '1 task waiting for the next Inform: Reboot',
    );
    // A reboot is already pending: the button says so instead of queueing
    // a second one.
    expect(getByText('Reboot').closest('button')).toBeDisabled();
    expect(
      getByText('Reboot already queued. It runs at the next Inform.'),
    ).toBeInTheDocument();
    expect(getByTestId('card-cellular')).toHaveTextContent('n41');
    expect(getByTestId('card-management_server')).toHaveTextContent('300');

    await waitFor(() => expect(getAllByTestId('line')).toHaveLength(3));
    expect(range).toHaveBeenCalledWith(
      expect.objectContaining({
        networkId: 'net1',
        query: 'acs_rsrp_dbm{cpe_key="IMSI001010000000113"}',
      }),
    );
  });

  it('shows the claim key for a claimed CPE and waits for its first read', async () => {
    mockAcsAPI();
    mockPrometheus();
    jest.spyOn(AcsAPI, 'getCpe').mockResolvedValue({cpe: FIXTURE_CPES[2]});
    jest.spyOn(AcsAPI, 'listTasks').mockResolvedValue([]);
    const {findByTestId, getAllByText} = renderAt(
      url('CLAIMlab-5400-01'),
      ROUTE,
      <AcsCpeDetail />,
    );
    const header = await findByTestId('acs-detail-header');
    expect(header).toHaveTextContent('Claim key');
    expect(header).toHaveTextContent('CLAIMlab-5400-01');
    expect(header).toHaveTextContent('Claimed');
    expect(getAllByText('Waiting for the first parameter read.')).toHaveLength(
      4,
    );
  });

  it('says when the CPE is not in the network', async () => {
    mockAcsAPI();
    mockPrometheus();
    const {findByText} = renderAt(url('IMSI999'), ROUTE, <AcsCpeDetail />);
    expect(await findByText('CPE not found in net1')).toBeInTheDocument();
  });

  it('keeps the state when the AGW cannot list tasks (503)', async () => {
    mockAcsAPI();
    mockPrometheus();
    jest
      .spyOn(AcsAPI, 'listTasks')
      .mockRejectedValue({response: {status: 503}});
    const {findByTestId, queryByTestId} = renderAt(
      url('IMSI001010000000113'),
      ROUTE,
      <AcsCpeDetail />,
    );
    expect(await findByTestId('acs-detail-header')).toBeInTheDocument();
    expect(queryByTestId('acs-pending-banner')).toBeNull();
  });

  it('shows the Prometheus error without hiding the page', async () => {
    mockAcsAPI();
    const {range} = mockPrometheus();
    range.mockRejectedValue(new Error('down'));
    const {findByText} = renderAt(
      url('IMSI001010000000113'),
      ROUTE,
      <AcsCpeDetail />,
    );
    expect(await findByText('KPI history is unavailable.')).toBeInTheDocument();
  });
});

describe('AcsActions', () => {
  const cpe = FIXTURE_CPES[1];

  it('queues a reboot after the confirmation', async () => {
    const {createTask} = mockAcsAPI();
    const onQueued = jest.fn();
    const {getByText} = render(
      <AcsActions
        networkId="net1"
        cpe={cpe}
        pendingTypes={new Set()}
        onQueued={onQueued}
      />,
    );
    fireEvent.click(getByText('Reboot'));
    expect(getByText(/about 2–3 minutes/)).toBeInTheDocument();
    fireEvent.click(getByText('Queue reboot'));
    await waitFor(() => expect(onQueued).toHaveBeenCalled());
    expect(createTask).toHaveBeenCalledWith('net1', cpe.cpe_key, {
      type: 'reboot',
    });
  });

  it('warns that an offline CPE gets the reboot when it returns', () => {
    const {getByText, getByTestId} = render(
      <AcsActions
        networkId="net1"
        cpe={FIXTURE_CPES[3]}
        pendingTypes={new Set()}
        onQueued={jest.fn()}
      />,
    );
    expect(getByText(/CPE offline: actions are queued/)).toBeInTheDocument();
    fireEvent.click(getByText('Reboot'));
    expect(getByTestId('reboot-offline')).toHaveTextContent(
      'This CPE is offline',
    );
  });

  it('queues a refresh straight away and reports API errors', async () => {
    jest
      .spyOn(AcsAPI, 'createTask')
      .mockRejectedValue({response: {status: 503, data: 'agw unreachable'}});
    const {getAllByText, findByText} = render(
      <AcsActions
        networkId="net1"
        cpe={cpe}
        pendingTypes={new Set()}
        onQueued={jest.fn()}
      />,
    );
    fireEvent.click(getAllByText('Refresh')[0]);
    expect(
      await findByText('Refresh was not queued (503 agw unreachable).'),
    ).toBeInTheDocument();
  });

  it('unlocks factory reset only with the exact serial', () => {
    const onConfirm = jest.fn();
    const {getByTestId, getByText} = render(
      <FactoryResetDialog
        cpe={cpe}
        busy={false}
        onCancel={jest.fn()}
        onConfirm={onConfirm}
      />,
    );
    const confirm = getByText('Factory reset at next Inform').closest('button');
    expect(confirm).toBeDisabled();
    fireEvent.change(getByTestId('factory-reset-serial'), {
      target: {value: 'GT4K2409A0018'},
    });
    expect(
      getByText("Doesn't match the serial of this CPE."),
    ).toBeInTheDocument();
    expect(confirm).toBeDisabled();
    fireEvent.change(getByTestId('factory-reset-serial'), {
      target: {value: 'GT4K2409A00187'},
    });
    expect(confirm).toBeEnabled();
    fireEvent.click(confirm!);
    expect(onConfirm).toHaveBeenCalled();
  });
});
