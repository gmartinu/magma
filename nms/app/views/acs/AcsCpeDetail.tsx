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
import AcsAPI, {AcsCpe, AcsCpeModel, AcsTask} from './AcsAPI';
import AcsActions from './AcsActions';
import AcsKpiCharts from './AcsKpiCharts';
import Alert from '@mui/material/Alert';
import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import Grid from '@mui/material/Grid';
import Paper from '@mui/material/Paper';
import React, {useState} from 'react';
import Skeleton from '@mui/material/Skeleton';
import Text from '../../theme/design-system/Text';
import TopBar from '../../components/TopBar';
import nullthrows from '../../../shared/util/nullthrows';
import {AcsContent} from './AcsLayout';
import {
  AcsPaper,
  EmptyBlock,
  ModeChip,
  RetryButton,
  SessionResultChip,
  StatusDot,
  usePoll,
} from './AcsCommon';
import {
  DEFAULT_INFORM_INTERVAL_SEC,
  acsPath,
  cpeName,
  deviceId,
  errorMessage,
  formatAgo,
  formatTime,
  formatUptime,
  httpStatus,
  modelValue,
  nextInform,
  taskLabel,
} from './AcsUtils';
import {
  Navigate,
  Route,
  Routes,
  useNavigate,
  useParams,
} from 'react-router-dom';
import {colors} from '../../theme/default';

export type CpeDetailData = {
  cpe: AcsCpe;
  model?: AcsCpeModel;
  tasks: Array<AcsTask>;
};

export function useCpeDetail(networkId: string, cpeKey: string) {
  return usePoll<CpeDetailData>(async () => {
    const detail = await AcsAPI.getCpe(networkId, cpeKey);
    // Tasks are read live from acsd; an unreachable AGW must not hide the
    // reported state.
    const tasks = await AcsAPI.listTasks(networkId, cpeKey).catch(
      () => [] as Array<AcsTask>,
    );
    return {cpe: detail.cpe, model: detail.model, tasks};
  }, [networkId, cpeKey]);
}

function KV({k, v, mono}: {k: string; v?: React.ReactNode; mono?: boolean}) {
  return (
    <Box sx={{minWidth: 150}}>
      <Text variant="body3">{k}</Text>
      <Box sx={{fontFamily: mono ? 'monospace' : undefined, fontSize: 14}}>
        {v ?? '—'}
      </Box>
    </Box>
  );
}

function intervalOf(model?: AcsCpeModel): number {
  const v = Number(
    modelValue(model, 'management_server', 'periodic_inform_interval'),
  );
  return v > 0 ? v : DEFAULT_INFORM_INTERVAL_SEC;
}

function DetailHeader({
  networkId,
  data,
  onQueued,
}: {
  networkId: string;
  data: CpeDetailData;
  onQueued: (t: AcsTask) => void;
}) {
  const {cpe, model, tasks} = data;
  const interval = intervalOf(model);
  const next = nextInform(cpe.last_inform, interval);
  const pendingTypes = new Set(
    tasks.filter(t => t.status === 'pending').map(t => t.type as string),
  );
  return (
    <Paper elevation={0} sx={{p: 2, mb: 3}} data-testid="acs-detail-header">
      <Grid container spacing={2} justifyContent="space-between">
        <Grid item xs={12} md={8}>
          <Box sx={{display: 'flex', alignItems: 'center', gap: 1, mb: 2}}>
            <StatusDot online={cpe.online} size={12} />
            <Text variant="h5">{cpeName(cpe)}</Text>
            <Text variant="body2">
              {cpe.online
                ? 'Online'
                : `Offline since ${formatTime(cpe.last_inform)}`}
            </Text>
            <ModeChip mode={cpe.mode} />
          </Box>
          <Box sx={{display: 'flex', flexWrap: 'wrap', gap: 3}}>
            <KV k="Model" v={cpe.model_name || cpe.product_class} />
            <KV k="Firmware" v={cpe.software_version} mono />
            <KV k="Device ID" v={deviceId(cpe)} mono />
            <KV
              k={cpe.mode === 'claimed' ? 'Claim key' : 'IMSI'}
              v={cpe.mode === 'claimed' ? cpe.cpe_key : cpe.imsi || cpe.cpe_key}
              mono
            />
            <KV k="Gateway" v={cpe.gateway_id || '—'} mono />
            <KV
              k="Last Inform"
              v={`${formatTime(cpe.last_inform)} (${formatAgo(
                cpe.last_inform,
              )})`}
            />
            <KV
              k="Next Inform expected"
              v={
                !next.due ? (
                  '—'
                ) : next.overdueSec > 60 ? (
                  <span style={{color: colors.state.errorAlt}}>
                    overdue by {formatUptime(next.overdueSec)}
                  </span>
                ) : (
                  `by ${formatTime(
                    next.due.toISOString(),
                  )} (every ${interval} s)`
                )
              }
            />
            <KV k="Uptime" v={formatUptime(model?.uptime_sec)} />
            <KV
              k="Last session"
              v={<SessionResultChip result={cpe.last_session?.result} />}
            />
          </Box>
        </Grid>
        <Grid item xs={12} md={4}>
          <AcsActions
            networkId={networkId}
            cpe={cpe}
            pendingTypes={pendingTypes}
            onQueued={onQueued}
          />
        </Grid>
      </Grid>
    </Paper>
  );
}

export function PendingBanner({tasks}: {tasks: Array<AcsTask>}) {
  const pending = tasks.filter(t => t.status === 'pending');
  if (pending.length === 0) {
    return null;
  }
  const what = pending
    .map(t => `${taskLabel(t.type)}, queued ${formatTime(t.created)}`)
    .join('; ');
  return (
    <Alert severity="warning" sx={{mb: 2}} data-testid="acs-pending-banner">
      <b>
        {pending.length} task{pending.length > 1 ? 's' : ''} waiting for the
        next Inform:
      </b>{' '}
      {what}. The page updates when it runs.
    </Alert>
  );
}

const CARDS: Array<[string, string, Array<[string, string]>]> = [
  [
    'Cellular',
    'cellular',
    [
      ['Access technology', 'technology'],
      ['RSRP (dBm)', 'rsrp'],
      ['RSRQ (dB)', 'rsrq'],
      ['SINR (dB)', 'sinr'],
      ['Band', 'band'],
      ['PCI', 'pci'],
      ['Cell ID', 'cell_id'],
      ['Operator', 'operator'],
      ['APN', 'apn'],
      ['IMEI', 'imei'],
      ['ICCID', 'iccid'],
    ],
  ],
  [
    'WAN',
    'wan',
    [
      ['IPv4', 'ipv4_address'],
      ['IPv6', 'ipv6_address'],
    ],
  ],
  [
    'Management server',
    'management_server',
    [
      ['ACS URL', 'url'],
      ['ACS username', 'username'],
      ['Periodic Inform', 'periodic_inform_enable'],
      ['Inform interval (s)', 'periodic_inform_interval'],
      ['Connection request URL', 'connection_request_url'],
    ],
  ],
  [
    'Identity',
    'identity',
    [
      ['Manufacturer', 'manufacturer'],
      ['OUI', 'oui'],
      ['Product class', 'product_class'],
      ['Model', 'model_name'],
      ['Hardware version', 'hardware_version'],
    ],
  ],
];

export function StateCards({model}: {model?: AcsCpeModel}) {
  return (
    <Grid container spacing={3}>
      {CARDS.map(([title, section, fields]) => {
        const hasAny = fields.some(([, key]) =>
          modelValue(model, section, key),
        );
        return (
          <Grid item xs={12} md={6} lg={3} key={section}>
            <Paper
              elevation={0}
              sx={{p: 2, height: '100%'}}
              data-testid={`card-${section}`}>
              <Text variant="body1" weight="medium">
                {title}
              </Text>
              {fields.map(([label, key]) => (
                <Box
                  key={key}
                  sx={{
                    display: 'flex',
                    justifyContent: 'space-between',
                    gap: 2,
                    py: 0.75,
                    borderBottom: `1px solid ${colors.primary.mercury}`,
                    fontSize: 13,
                  }}>
                  <span style={{color: colors.primary.comet}}>{label}</span>
                  {hasAny ? (
                    <span style={{fontFamily: 'monospace', textAlign: 'right'}}>
                      {modelValue(model, section, key) ?? '—'}
                    </span>
                  ) : (
                    <Skeleton variant="text" width={70} />
                  )}
                </Box>
              ))}
              {!hasAny && (
                <Box sx={{mt: 1}}>
                  <Text variant="body3">
                    Waiting for the first parameter read.
                  </Text>
                </Box>
              )}
            </Paper>
          </Grid>
        );
      })}
    </Grid>
  );
}

function Overview({networkId, data}: {networkId: string; data: CpeDetailData}) {
  return (
    <>
      <AcsKpiCharts networkId={networkId} cpeKey={data.cpe.cpe_key} />
      <StateCards model={data.model} />
    </>
  );
}

export default function AcsCpeDetail() {
  const params = useParams();
  const networkId = nullthrows(params.networkId);
  const cpeKey = nullthrows(params.cpeKey);
  const navigate = useNavigate();
  const {data, error, isLoading, reload} = useCpeDetail(networkId, cpeKey);
  const [queued, setQueued] = useState<AcsTask | null>(null);

  const header = `ACS / ${data ? cpeName(data.cpe) : cpeKey}`;

  const tabs = [{label: 'Overview', to: 'overview'}];

  let body: React.ReactNode;
  if (!data && isLoading) {
    body = <Skeleton variant="rectangular" height={160} />;
  } else if (!data && httpStatus(error) === 404) {
    body = (
      <AcsPaper>
        <EmptyBlock
          title={`CPE not found in ${networkId}`}
          action={
            <Button
              variant="outlined"
              onClick={() => navigate(acsPath(networkId, 'overview', 'cpes'))}>
              Back to CPEs
            </Button>
          }>
          No gateway of this network reports a CPE with key {cpeKey}. It may
          belong to another network, or a claim may not have matched a CPE yet.
        </EmptyBlock>
      </AcsPaper>
    );
  } else if (!data) {
    body = (
      <Alert severity="error" action={<RetryButton onClick={reload} />}>
        <b>Couldn't load the CPE.</b> {errorMessage(error)}
      </Alert>
    );
  } else {
    body = (
      <>
        {queued && (
          <Alert severity="info" sx={{mb: 2}} onClose={() => setQueued(null)}>
            <b>{taskLabel(queued.type)} queued.</b> It runs at the next Inform.
            You can leave this page.
          </Alert>
        )}
        <PendingBanner tasks={data.tasks} />
        <DetailHeader
          networkId={networkId}
          data={data}
          onQueued={t => {
            setQueued(t);
            reload();
          }}
        />
        <Routes>
          <Route
            path="/overview"
            element={<Overview networkId={networkId} data={data} />}
          />
          <Route index element={<Navigate to="overview" replace />} />
        </Routes>
      </>
    );
  }

  return (
    <>
      <TopBar header={header} tabs={tabs} />
      <AcsContent>{body}</AcsContent>
    </>
  );
}
