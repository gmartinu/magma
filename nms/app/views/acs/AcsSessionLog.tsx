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
import IconButton from '@mui/material/IconButton';
import KeyboardArrowDownIcon from '@mui/icons-material/KeyboardArrowDown';
import KeyboardArrowRightIcon from '@mui/icons-material/KeyboardArrowRight';
import Link from '@mui/material/Link';
import ListIcon from '@mui/icons-material/List';
import MagmaAPI from '../../api/MagmaAPI';
import MenuItem from '@mui/material/MenuItem';
import React, {useState} from 'react';
import Table from '@mui/material/Table';
import TableBody from '@mui/material/TableBody';
import TableCell from '@mui/material/TableCell';
import TableHead from '@mui/material/TableHead';
import TableRow from '@mui/material/TableRow';
import TextField from '@mui/material/TextField';
import {
  AcsPaper,
  EmptyBlock,
  RetryButton,
  SessionResultChip,
  SkeletonRows,
  usePoll,
} from './AcsCommon';
import {RANGES, RangeSelect} from './AcsKpiCharts';
import {Link as RouterLink} from 'react-router-dom';
import {colors} from '../../theme/default';
import {cpePath, errorMessage, formatDuration, formatTime} from './AcsUtils';

// acsd logs one cpe_session_completed event per CWMP session to eventd
// (stream acsd, tag = cpe_key); the network log reads them back through the
// Orc8r events API. Fields as in lte/swagger/cpe_acs_events.v1.yml.
export const ACS_EVENT_STREAM = 'acsd';
export const SESSION_EVENT = 'cpe_session_completed';

export type SessionEvent = {
  cpe_key?: string;
  imsi?: string;
  session_id?: string;
  result?: string;
  reason?: string;
  started?: number;
  ended?: number;
  tasks_done?: number;
  tasks_failed?: number;
  faults?: number;
};

export async function fetchSessions(
  networkId: string,
  hours: number,
  cpeKey?: string,
): Promise<Array<SessionEvent>> {
  const end = new Date();
  const start = new Date(end.getTime() - hours * 3600 * 1000);
  const res = await MagmaAPI.events.eventsNetworkIdGet({
    networkId,
    streams: ACS_EVENT_STREAM,
    events: SESSION_EVENT,
    tags: cpeKey,
    size: '500',
    start: start.toISOString(),
    end: end.toISOString(),
  });
  return (res.data ?? []).map(e => ({
    cpe_key: e.tag,
    ...(e.value as SessionEvent),
  }));
}

function SessionRow({
  s,
  networkId,
  showDevice,
}: {
  s: SessionEvent;
  networkId: string;
  showDevice: boolean;
}) {
  const [open, setOpen] = useState(false);
  const faulty = (s.faults ?? 0) > 0 || (s.tasks_failed ?? 0) > 0;
  return (
    <>
      <TableRow hover data-testid="acs-session-row">
        <TableCell sx={{width: 36}}>
          <IconButton
            size="small"
            aria-label="expand"
            onClick={() => setOpen(!open)}>
            {open ? (
              <KeyboardArrowDownIcon fontSize="small" />
            ) : (
              <KeyboardArrowRightIcon fontSize="small" />
            )}
          </IconButton>
        </TableCell>
        <TableCell>{formatTime(s.started)}</TableCell>
        {showDevice && (
          <TableCell>
            {s.cpe_key ? (
              <Link component={RouterLink} to={cpePath(networkId, s.cpe_key)}>
                {s.cpe_key}
              </Link>
            ) : (
              '—'
            )}
          </TableCell>
        )}
        <TableCell>
          <SessionResultChip result={s.result} />
        </TableCell>
        <TableCell>
          {s.tasks_done ?? 0} done · {s.tasks_failed ?? 0} failed
        </TableCell>
        <TableCell sx={{color: faulty ? colors.state.errorAlt : undefined}}>
          {s.faults ?? 0}
        </TableCell>
        <TableCell>{formatDuration(s.started, s.ended)}</TableCell>
      </TableRow>
      {open && (
        <TableRow sx={{backgroundColor: colors.primary.concrete}}>
          <TableCell colSpan={showDevice ? 7 : 6}>
            <Box
              component="pre"
              sx={{m: 0, pl: 5, fontSize: 12, fontFamily: 'monospace'}}>
              {[
                `Session   ${s.session_id ?? '—'}`,
                `Result    ${s.result ?? '—'}${
                  s.reason ? ` (${s.reason})` : ''
                }`,
                `Started   ${formatTime(s.started)}`,
                `Ended     ${formatTime(s.ended)}`,
                `IMSI      ${s.imsi || '—'}`,
              ].join('\n')}
            </Box>
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

export default function AcsSessionLog({
  networkId,
  cpeKey,
}: {
  networkId: string;
  cpeKey?: string;
}) {
  const [hours, setHours] = useState(24);
  const [result, setResult] = useState('');
  const [search, setSearch] = useState('');
  const {data, error, isLoading, reload} = usePoll(
    () => fetchSessions(networkId, hours, cpeKey),
    [networkId, cpeKey, hours],
  );
  const showDevice = !cpeKey;
  const q = search.trim().toLowerCase();
  const rows = (data ?? []).filter(
    s =>
      (!result || s.result === result) &&
      (!q || (s.cpe_key ?? '').toLowerCase().includes(q)),
  );
  const rangeLabel = RANGES.find(([, h]) => h === hours)?.[0] ?? '';

  return (
    <>
      <CardTitleRow
        icon={ListIcon}
        label={
          cpeKey
            ? `Sessions of this CPE (${rows.length})`
            : `Sessions (${rows.length})`
        }
        filter={() => (
          <Box sx={{display: 'flex', gap: 1}}>
            {showDevice && (
              <TextField
                size="small"
                placeholder="CPE key"
                value={search}
                onChange={e => setSearch(e.target.value)}
                inputProps={{'data-testid': 'session-search'}}
              />
            )}
            <TextField
              select
              size="small"
              label="Outcome"
              value={result}
              sx={{minWidth: 140}}
              onChange={e => setResult(e.target.value)}>
              <MenuItem value="">All</MenuItem>
              <MenuItem value="completed">OK</MenuItem>
              <MenuItem value="timed_out">Timed out</MenuItem>
              <MenuItem value="interrupted">Interrupted</MenuItem>
            </TextField>
            <RangeSelect hours={hours} onChange={setHours} />
          </Box>
        )}
      />
      {error ? (
        <Alert
          severity="error"
          sx={{mb: 2}}
          action={<RetryButton onClick={reload} />}>
          <b>Couldn't load the session log.</b> {errorMessage(error)}. Try a
          shorter range.
        </Alert>
      ) : null}
      <AcsPaper>
        {data && rows.length === 0 ? (
          <EmptyBlock title="No sessions in this range">
            No CPE{cpeKey ? '' : ` of ${networkId}`} contacted the ACS in the{' '}
            {rangeLabel.toLowerCase()}. With a 300 s Inform interval, an empty
            range usually means the CPEs are offline or acsd is down.
          </EmptyBlock>
        ) : (
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell />
                <TableCell>Started</TableCell>
                {showDevice && <TableCell>Device</TableCell>}
                <TableCell>Outcome</TableCell>
                <TableCell>Tasks</TableCell>
                <TableCell>Faults</TableCell>
                <TableCell>Duration</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {isLoading && !data ? (
                <SkeletonRows rows={4} cols={showDevice ? 7 : 6} />
              ) : (
                rows.map((s, i) => (
                  <SessionRow
                    key={s.session_id ?? i}
                    s={s}
                    networkId={networkId}
                    showDevice={showDevice}
                  />
                ))
              )}
            </TableBody>
          </Table>
        )}
      </AcsPaper>
    </>
  );
}
