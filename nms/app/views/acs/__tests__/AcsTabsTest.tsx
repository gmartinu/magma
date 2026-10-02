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
import AcsDashboard from '../AcsDashboard';
import AcsGatewayHealth, {fetchGatewayHealth} from '../AcsGatewayHealth';
import AcsParameters, {didYouMean, subtreeCounts} from '../AcsParameters';
import AcsSessionLog from '../AcsSessionLog';
import AcsTasks, {taskResult} from '../AcsTasks';
import MagmaAPI from '../../../api/MagmaAPI';
import React from 'react';
import {FIXTURE_PARAMETERS, FIXTURE_TASKS} from '../AcsFixtures';
import {fireEvent, waitFor, within} from '@testing-library/react';
import {mockAcsAPI, mockPrometheus, renderAt} from '../AcsTestUtils';

jest.mock('react-chartjs-2', () => ({Line: () => <div data-testid="line" />}));

describe('AcsParameters', () => {
  it('filters by case-sensitive prefix, from chips and the tree', async () => {
    mockAcsAPI();
    const {getAllByTestId, getByTestId, getByText, findByText} = renderAt(
      '/x',
      '/x',
      <AcsParameters networkId="net1" cpeKey="IMSI1" />,
    );
    await waitFor(() => expect(getAllByTestId('param-row')).toHaveLength(10));
    fireEvent.click(
      getByText('Device.Cellular.', {selector: '.MuiChip-label'}),
    );
    expect(getAllByTestId('param-row')).toHaveLength(6);
    fireEvent.change(getByTestId('param-prefix'), {
      target: {value: 'Device.Celular.'},
    });
    expect(
      await findByText('No parameter starts with "Device.Celular."'),
    ).toBeInTheDocument();
    fireEvent.click(getByText('Device.Cellular.', {selector: 'button'}));
    expect(getAllByTestId('param-row')).toHaveLength(6);
    fireEvent.click(getAllByTestId('param-tree-node')[1]);
    expect(getAllByTestId('param-row')).toHaveLength(2);
  });

  it('says the gateway is unreachable on 503', async () => {
    jest
      .spyOn(AcsAPI, 'listParameters')
      .mockRejectedValue({response: {status: 503}});
    const {findByText} = renderAt(
      '/x',
      '/x',
      <AcsParameters networkId="net1" cpeKey="IMSI1" />,
    );
    expect(await findByText(/gateway is unreachable/)).toBeInTheDocument();
  });

  it('counts subtrees and suggests the closest one', () => {
    expect(subtreeCounts(FIXTURE_PARAMETERS).slice(0, 2)).toEqual([
      ['Device.Cellular.', 6],
      ['Device.DeviceInfo.', 2],
    ]);
    const subtrees = ['Device.Cellular.', 'Device.WiFi.'];
    expect(didYouMean('device.wifi', subtrees)).toBe('Device.WiFi.');
    expect(didYouMean('Device.Celular.', subtrees)).toBe('Device.Cellular.');
    expect(didYouMean('', subtrees)).toBeUndefined();
  });
});

describe('AcsTasks', () => {
  it('lists the history newest first and expands failures', () => {
    const {getAllByTestId, getByText} = renderAt(
      '/x',
      '/x',
      <AcsTasks tasks={FIXTURE_TASKS} />,
    );
    const rows = getAllByTestId('acs-task-row');
    expect(
      rows.map(
        r => within(r).getAllByRole('cell', {hidden: true})[1].textContent,
      ),
    ).toEqual(['Reboot', 'Refresh', 'Refresh', 'Factory reset']);
    expect(rows[0]).toHaveTextContent('Pending');
    expect(rows[2]).toHaveTextContent('9002 Internal error');
    // Failed tasks open on their fault.
    expect(getByText(/"fault_code": 9002/)).toBeInTheDocument();
  });

  it('describes each outcome', () => {
    expect(taskResult(FIXTURE_TASKS[2])).toBe('1 parameters read');
    expect(taskResult({...FIXTURE_TASKS[1], fault_code: 0})).toBe(
      'Sessions kept dropping; no retries left',
    );
    expect(taskResult(FIXTURE_TASKS[0])).toMatch(/No Inform before the TTL/);
  });

  it('has an empty state', () => {
    const {getByText} = renderAt('/x', '/x', <AcsTasks tasks={[]} />);
    expect(getByText('No tasks yet')).toBeInTheDocument();
  });
});

describe('AcsSessionLog', () => {
  const events = [
    {
      stream_name: 'acsd',
      event_type: 'cpe_session_completed',
      hardware_id: 'hw1',
      tag: 'IMSI001010000000113',
      timestamp: '2026-10-01T14:32:06Z',
      value: {
        session_id: 's-1',
        result: 'completed',
        started: 1790000000,
        ended: 1790000001.5,
        tasks_done: 1,
        tasks_failed: 0,
        faults: 0,
      },
    },
    {
      stream_name: 'acsd',
      event_type: 'cpe_session_completed',
      hardware_id: 'hw1',
      tag: 'CLAIMlab-5400-01',
      timestamp: '2026-10-01T14:20:00Z',
      value: {session_id: 's-2', result: 'timed_out', faults: 2},
    },
  ];

  it('reads cpe_session_completed events of the network', async () => {
    const get = jest
      .spyOn(MagmaAPI.events, 'eventsNetworkIdGet')
      .mockResolvedValue({data: events} as never);
    const {getAllByTestId, getByText} = renderAt(
      '/nms/net1/acs/overview/sessions',
      '/nms/:networkId/acs/overview/sessions',
      <AcsSessionLog networkId="net1" />,
    );
    await waitFor(() =>
      expect(getAllByTestId('acs-session-row')).toHaveLength(2),
    );
    expect(get).toHaveBeenCalledWith(
      expect.objectContaining({
        networkId: 'net1',
        streams: 'acsd',
        events: 'cpe_session_completed',
        tags: undefined,
      }),
    );
    const [ok, timedOut] = getAllByTestId('acs-session-row');
    expect(ok).toHaveTextContent('IMSI001010000000113');
    expect(ok).toHaveTextContent('1 done · 0 failed');
    expect(ok).toHaveTextContent('1.5 s');
    expect(timedOut).toHaveTextContent('Timed out');
    fireEvent.click(within(ok).getByLabelText('expand'));
    expect(getByText(/Session s-1/)).toBeInTheDocument();
  });

  it('filters the device log by its cpe_key tag', async () => {
    const get = jest
      .spyOn(MagmaAPI.events, 'eventsNetworkIdGet')
      .mockResolvedValue({data: []} as never);
    const {findByText} = renderAt(
      '/x',
      '/x',
      <AcsSessionLog networkId="net1" cpeKey="IMSI1" />,
    );
    expect(await findByText('No sessions in this range')).toBeInTheDocument();
    expect(get).toHaveBeenCalledWith(expect.objectContaining({tags: 'IMSI1'}));
  });
});

describe('AcsGatewayHealth', () => {
  function vector(label: string, values: Record<string, number>) {
    return {
      data: {
        data: {
          resultType: 'vector',
          result: Object.entries(values).map(([k, v]) => ({
            metric: {[label]: k},
            value: ['1', String(v)],
          })),
        },
      },
    } as never;
  }

  beforeEach(() => {
    jest
      .spyOn(MagmaAPI.metrics, 'networksNetworkIdPrometheusQueryGet')
      .mockImplementation(({query}) =>
        Promise.resolve(
          query.startsWith('sum by (code)')
            ? vector('code', {'9002': 3, '9005': 0})
            : query.includes('acs_online_cpes')
            ? vector('gatewayID', {agw01: 2, agw02: 1})
            : query.includes('acs_informs_total')
            ? vector('gatewayID', {agw01: 24.4})
            : vector('gatewayID', {}),
        ),
      );
  });

  it('aggregates per gateway and drops zero fault codes', async () => {
    const {gateways, faults} = await fetchGatewayHealth('net1');
    expect(gateways.map(g => [g.gatewayId, g.online, g.informs])).toEqual([
      ['agw01', 2, 24.4],
      ['agw02', 1, 0],
    ]);
    expect(faults).toEqual([['9002', 3]]);
  });

  it('renders one row per gateway', async () => {
    const {getAllByTestId} = renderAt(
      '/nms/net1/acs/overview/gateways',
      '/nms/:networkId/acs/overview/gateways',
      <AcsGatewayHealth />,
    );
    await waitFor(() =>
      expect(getAllByTestId('acs-gateway-row')).toHaveLength(2),
    );
    expect(getAllByTestId('acs-gateway-row')[0]).toHaveTextContent('agw01');
    expect(getAllByTestId('acs-fault-row')).toHaveLength(1);
  });
});

describe('AcsDashboard', () => {
  it('opens on the CPE list and routes to a CPE tab', async () => {
    mockAcsAPI();
    mockPrometheus();
    jest
      .spyOn(MagmaAPI.events, 'eventsNetworkIdGet')
      .mockResolvedValue({data: []} as never);
    const list = renderAt(
      '/nms/net1/acs',
      '/nms/:networkId/acs/*',
      <AcsDashboard />,
    );
    await waitFor(() =>
      expect(list.getAllByTestId('acs-cpe-row')).toHaveLength(4),
    );
    expect(list.getByTestId('Gateways')).toBeInTheDocument();
    list.unmount();

    const detail = renderAt(
      '/nms/net1/acs/cpe/IMSI001010000000113/tasks',
      '/nms/:networkId/acs/*',
      <AcsDashboard />,
    );
    await waitFor(() =>
      expect(detail.getAllByTestId('acs-task-row')).toHaveLength(4),
    );
    expect(detail.getByText('Tasks (1 pending)')).toBeInTheDocument();
  });
});
