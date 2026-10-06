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
import AcsClaimsAPI, {
  CLAIMS_API_AVAILABLE,
  ClaimsUnavailableError,
} from '../AcsClaimsStub';
import MagmaAPI from '../../../api/MagmaAPI';
import axios from 'axios';
import {FIXTURE_CPES} from '../AcsFixtures';
import {
  acsPath,
  cpePath,
  deviceId,
  errorMessage,
  formatDuration,
  formatUptime,
  httpStatus,
  modelValue,
  nextInform,
  signalBars,
  toDate,
} from '../AcsUtils';
import {
  byLabel,
  cpeKpiQuery,
  cpeSelector,
  latestSignal,
  stepFor,
  toPoints,
} from '../AcsMetrics';

describe('AcsUtils', () => {
  it('parses RFC 3339 and unix seconds, rejects empty and zero', () => {
    expect(toDate('2026-10-01T14:33:00Z')?.toISOString()).toBe(
      '2026-10-01T14:33:00.000Z',
    );
    expect(toDate(1790000000)?.getTime()).toBe(1790000000000);
    expect(toDate('')).toBeNull();
    expect(toDate(0)).toBeNull();
    expect(toDate('not a date')).toBeNull();
  });

  it('maps RSRP to the design thresholds', () => {
    expect([-70, -80, -85, -95, -101, null].map(signalBars)).toEqual([
      4,
      4,
      3,
      2,
      1,
      0,
    ]);
  });

  it('builds the TR-069 DeviceId and paths', () => {
    expect(deviceId(FIXTURE_CPES[0])).toBe('D4A5C2-Titan4000-GT4K2409A00113');
    expect(
      deviceId({
        ...FIXTURE_CPES[0],
        oui: '',
        product_class: '',
        serial_number: '',
      }),
    ).toBe('—');
    expect(acsPath('net1', 'overview', 'cpes')).toBe(
      '/nms/net1/acs/overview/cpes',
    );
    expect(cpePath('net1', 'CLAIM a/b', 'tasks')).toBe(
      '/nms/net1/acs/cpe/CLAIM%20a%2Fb/tasks',
    );
  });

  it('reads model values, skipping empty ones', () => {
    const model = {cellular: {rsrp: -84, band: '', apn: 'fwa'}};
    expect(modelValue(model, 'cellular', 'rsrp')).toBe('-84');
    expect(modelValue(model, 'cellular', 'band', 'apn')).toBe('fwa');
    expect(modelValue(model, 'wan', 'ipv4_address')).toBeUndefined();
    expect(modelValue(undefined, 'cellular', 'rsrp')).toBeUndefined();
  });

  it('computes the next Inform and how late it is', () => {
    const now = new Date('2026-10-01T14:40:00Z');
    const onTime = nextInform('2026-10-01T14:38:00Z', 300, now);
    expect(onTime.due?.toISOString()).toBe('2026-10-01T14:43:00.000Z');
    expect(onTime.overdueSec).toBe(0);
    expect(nextInform('2026-10-01T14:00:00Z', 300, now).overdueSec).toBe(2100);
    expect(nextInform(undefined, 300, now).due).toBeNull();
  });

  it('formats durations and uptimes', () => {
    expect(
      formatDuration('2026-10-01T14:00:00Z', '2026-10-01T14:00:02.5Z'),
    ).toBe('2.5 s');
    expect(formatDuration(undefined, 'x')).toBe('—');
    expect(formatUptime(6 * 86400 + 4 * 3600)).toBe('6 d 4 h');
    expect(formatUptime(3700)).toBe('1 h 1 min');
    expect(formatUptime(0)).toBe('—');
  });

  it('extracts HTTP status and message from axios errors', () => {
    const err = {response: {status: 503, data: {message: 'agw unreachable'}}};
    expect(httpStatus(err)).toBe(503);
    expect(errorMessage(err)).toBe('503 agw unreachable');
    expect(errorMessage(new Error('boom'))).toBe('boom');
  });
});

describe('AcsMetrics', () => {
  it('escapes the cpe_key in PromQL selectors', () => {
    expect(cpeSelector('acs_rsrp_dbm', 'IMSI1')).toBe(
      'acs_rsrp_dbm{cpe_key="IMSI1"}',
    );
    expect(cpeKpiQuery('acs_sinr_db', 'IMSI1')).toBe(
      'max by (cpe_key) (acs_sinr_db{cpe_key="IMSI1"})',
    );
    expect(cpeSelector('acs_rsrp_dbm', 'a"b\\c')).toBe(
      'acs_rsrp_dbm{cpe_key="a\\"b\\\\c"}',
    );
  });

  it('keeps ~300 points per range, never under 60 s', () => {
    expect(stepFor(3 * 3600)).toBe('60s');
    expect(stepFor(7 * 24 * 3600)).toBe('2016s');
  });

  it('converts matrix and vector results', () => {
    expect(
      toPoints([
        {
          metric: {},
          values: [
            ['100', '-84'],
            ['160', '-85.5'],
          ],
        },
      ]),
    ).toEqual([
      {x: 100000, y: -84},
      {x: 160000, y: -85.5},
    ]);
    expect(toPoints([])).toEqual([]);
    expect(
      byLabel(
        [
          {metric: {gatewayID: 'agw01'} as never, value: ['1', '3']},
          {metric: {} as never, value: ['1', '9']},
        ],
        'gatewayID',
      ),
    ).toEqual({agw01: 3});
  });

  it('merges RSRP and SINR per cpe_key', async () => {
    const spy = jest
      .spyOn(MagmaAPI.metrics, 'networksNetworkIdPrometheusQueryGet')
      .mockImplementation(({query}) =>
        Promise.resolve({
          data: {
            status: 'success',
            data: {
              resultType: 'vector',
              result: [
                {
                  metric: {cpe_key: 'IMSI1'} as never,
                  value: [
                    '1',
                    query === 'max by (cpe_key) (acs_rsrp_dbm)'
                      ? '-84'
                      : '14.5',
                  ],
                },
              ],
            },
          },
        } as never),
      );
    expect(await latestSignal('net1')).toEqual({
      IMSI1: {rsrp: -84, sinr: 14.5},
    });
    expect(spy).toHaveBeenCalledTimes(2);
  });
});

describe('AcsClaimsStub', () => {
  it('lists nothing and refuses to create until Orc8r serves claims', async () => {
    expect(CLAIMS_API_AVAILABLE).toBe(false);
    expect(await AcsClaimsAPI.listClaims('net1')).toEqual([]);
    await expect(
      AcsClaimsAPI.createClaim('net1', {
        oui: 'D4A5C2',
        product_class: 'X',
        serial_number: 'S',
      }),
    ).rejects.toBeInstanceOf(ClaimsUnavailableError);
  });
});

describe('AcsAPI', () => {
  it('calls the Orc8r ACS routes through the NMS proxy', async () => {
    const get = jest.spyOn(axios, 'get').mockResolvedValue({data: []});
    const post = jest.spyOn(axios, 'post').mockResolvedValue({data: {id: 't'}});
    await AcsAPI.listCpes('net1', {mode: 'claimed'});
    await AcsAPI.listParameters('net1', 'IMSI1', 'Device.Cellular.');
    await AcsAPI.createTask('net1', 'IMSI1', {type: 'reboot'});
    expect(get).toHaveBeenNthCalledWith(
      1,
      '/nms/apicontroller/magma/v1/acs/net1/cpes',
      {params: {mode: 'claimed'}},
    );
    expect(get).toHaveBeenNthCalledWith(
      2,
      '/nms/apicontroller/magma/v1/acs/net1/cpes/IMSI1/parameters',
      {params: {prefix: 'Device.Cellular.'}},
    );
    expect(
      post,
    ).toHaveBeenCalledWith(
      '/nms/apicontroller/magma/v1/acs/net1/cpes/IMSI1/tasks',
      {type: 'reboot'},
    );
  });

  it('searches the session log and sends Connection Requests', async () => {
    const page = {total_count: 3, logs: []};
    const get = jest.spyOn(axios, 'get').mockResolvedValue({data: page});
    const post = jest
      .spyOn(axios, 'post')
      .mockResolvedValue({data: {sent: true}});
    expect(
      await AcsAPI.searchLogs('net1', {
        event: 'cpe_session_completed',
        cpe_key: 'IMSI1',
        size: 100,
      }),
    ).toEqual(page);
    expect(
      get,
    ).toHaveBeenCalledWith('/nms/apicontroller/magma/v1/acs/net1/logs', {
      params: {event: 'cpe_session_completed', cpe_key: 'IMSI1', size: 100},
    });
    expect(await AcsAPI.connectionRequest('net1', 'CLAIM 1')).toEqual({
      sent: true,
    });
    expect(post).toHaveBeenCalledWith(
      '/nms/apicontroller/magma/v1/acs/net1/cpes/CLAIM%201/connection_request',
    );
  });
});
