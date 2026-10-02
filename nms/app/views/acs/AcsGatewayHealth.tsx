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
import Alert from '@mui/material/Alert';
import Box from '@mui/material/Box';
import CardTitleRow from '../../components/layout/CardTitleRow';
import CellWifiIcon from '@mui/icons-material/CellWifi';
import ErrorOutlineIcon from '@mui/icons-material/ErrorOutline';
import Grid from '@mui/material/Grid';
import React from 'react';
import Table from '@mui/material/Table';
import TableBody from '@mui/material/TableBody';
import TableCell from '@mui/material/TableCell';
import TableHead from '@mui/material/TableHead';
import TableRow from '@mui/material/TableRow';
import nullthrows from '../../../shared/util/nullthrows';
import {
  AcsPaper,
  EmptyBlock,
  RetryButton,
  SkeletonRows,
  usePoll,
} from './AcsCommon';
import {byLabel, queryInstant} from './AcsMetrics';
import {errorMessage} from './AcsUtils';
import {useParams} from 'react-router-dom';

// One instant query per column, grouped by the gatewayID label magma adds
// to every metric an AGW pushes.
export const GATEWAY_QUERIES = {
  cpes: 'sum by (gatewayID) (acs_cpes)',
  online: 'sum by (gatewayID) (acs_online_cpes)',
  informs: 'sum by (gatewayID) (increase(acs_informs_total[1h]))',
  sessions: 'sum by (gatewayID) (increase(acs_sessions_total[1h]))',
  faults: 'sum by (gatewayID) (increase(acs_faults_total[1h]))',
  pending: 'sum by (gatewayID) (acs_tasks{status="pending"})',
  failed: 'sum by (gatewayID) (acs_tasks{status="failed"})',
} as const;

export const FAULTS_BY_CODE = 'sum by (code) (increase(acs_faults_total[24h]))';

type Column = keyof typeof GATEWAY_QUERIES;

export type GatewayHealthRow = {gatewayId: string} & Record<Column, number>;

export async function fetchGatewayHealth(
  networkId: string,
): Promise<{
  gateways: Array<GatewayHealthRow>;
  faults: Array<[string, number]>;
}> {
  const columns = Object.keys(GATEWAY_QUERIES) as Array<Column>;
  const results = await Promise.all(
    columns.map(c =>
      queryInstant(networkId, GATEWAY_QUERIES[c]).then(r =>
        byLabel(r, 'gatewayID'),
      ),
    ),
  );
  const ids = new Set<string>();
  results.forEach(r => Object.keys(r).forEach(id => ids.add(id)));
  const gateways = Array.from(ids)
    .sort()
    .map(gatewayId => {
      const row = {gatewayId} as GatewayHealthRow;
      columns.forEach((c, i) => (row[c] = results[i][gatewayId] ?? 0));
      return row;
    });
  const faults = Object.entries(
    byLabel(await queryInstant(networkId, FAULTS_BY_CODE), 'code'),
  )
    .filter(([, n]) => n > 0)
    .sort(([, a], [, b]) => b - a);
  return {gateways, faults};
}

const round = (n: number) => (Number.isInteger(n) ? n : Math.round(n));

export default function AcsGatewayHealth() {
  const networkId = nullthrows(useParams().networkId);
  const {data, error, isLoading, reload} = usePoll(
    () => fetchGatewayHealth(networkId),
    [networkId],
  );

  return (
    <>
      {error ? (
        <Alert
          severity="error"
          sx={{mb: 2}}
          action={<RetryButton onClick={reload} />}>
          <b>Couldn't read ACS metrics from Prometheus.</b>{' '}
          {errorMessage(error)}
        </Alert>
      ) : null}
      <Grid container spacing={3}>
        <Grid item xs={12} lg={8}>
          <CardTitleRow
            icon={CellWifiIcon}
            label="ACS per gateway (last hour)"
          />
          <AcsPaper>
            {data && data.gateways.length === 0 ? (
              <EmptyBlock title="No gateway reports ACS metrics">
                acsd exports acs_* metrics on every AGW that runs it; magmad's
                metricsd forwards them to Orc8r. None arrived for {networkId}.
              </EmptyBlock>
            ) : (
              <Table size="small">
                <TableHead>
                  <TableRow>
                    <TableCell>Gateway</TableCell>
                    <TableCell align="right">CPEs</TableCell>
                    <TableCell align="right">Online</TableCell>
                    <TableCell align="right">Informs / h</TableCell>
                    <TableCell align="right">Sessions / h</TableCell>
                    <TableCell align="right">Faults / h</TableCell>
                    <TableCell align="right">Pending tasks</TableCell>
                    <TableCell align="right">Failed tasks</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {isLoading && !data ? (
                    <SkeletonRows rows={2} cols={8} />
                  ) : (
                    data?.gateways.map(g => (
                      <TableRow key={g.gatewayId} data-testid="acs-gateway-row">
                        <TableCell sx={{fontFamily: 'monospace'}}>
                          {g.gatewayId}
                        </TableCell>
                        <TableCell align="right">{round(g.cpes)}</TableCell>
                        <TableCell align="right">{round(g.online)}</TableCell>
                        <TableCell align="right">{round(g.informs)}</TableCell>
                        <TableCell align="right">{round(g.sessions)}</TableCell>
                        <TableCell align="right">{round(g.faults)}</TableCell>
                        <TableCell align="right">{round(g.pending)}</TableCell>
                        <TableCell align="right">{round(g.failed)}</TableCell>
                      </TableRow>
                    ))
                  )}
                </TableBody>
              </Table>
            )}
          </AcsPaper>
        </Grid>
        <Grid item xs={12} lg={4}>
          <CardTitleRow
            icon={ErrorOutlineIcon}
            label="CWMP faults by code (24 h)"
          />
          <AcsPaper>
            {data && data.faults.length === 0 ? (
              <Box sx={{p: 2}}>No faults in the last 24 hours.</Box>
            ) : (
              <Table size="small">
                <TableHead>
                  <TableRow>
                    <TableCell>Fault code</TableCell>
                    <TableCell align="right">Count</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {data?.faults.map(([code, n]) => (
                    <TableRow key={code} data-testid="acs-fault-row">
                      <TableCell sx={{fontFamily: 'monospace'}}>
                        {code}
                      </TableCell>
                      <TableCell align="right">{round(n)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </AcsPaper>
        </Grid>
      </Grid>
    </>
  );
}
