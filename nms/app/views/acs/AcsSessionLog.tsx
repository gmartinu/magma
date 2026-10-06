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
import AcsAPI, {ACS_LOGS_MAX_SIZE, AcsLog, AcsLogs} from './AcsAPI';
import Alert from '@mui/material/Alert';
import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import CardTitleRow from '../../components/layout/CardTitleRow';
import IconButton from '@mui/material/IconButton';
import KeyboardArrowDownIcon from '@mui/icons-material/KeyboardArrowDown';
import KeyboardArrowRightIcon from '@mui/icons-material/KeyboardArrowRight';
import Link from '@mui/material/Link';
import ListIcon from '@mui/icons-material/List';
import MenuItem from '@mui/material/MenuItem';
import React, {useEffect, useRef, useState} from 'react';
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

// acsd logs one cpe_session_completed event per CWMP session to eventd;
// the ACS session log route reads them back (GET /acs/{network_id}/logs),
// behind the acs entitlement like the rest of the ACS API.
export const SESSION_EVENT = 'cpe_session_completed';

// Sessions per page; "Load more" adds a page, up to the route's maximum.
export const SESSIONS_PAGE = 100;

export async function fetchSessions(
  networkId: string,
  hours: number,
  limit: number,
  cpeKey?: string,
): Promise<AcsLogs> {
  const end = new Date();
  const start = new Date(end.getTime() - hours * 3600 * 1000);
  return AcsAPI.searchLogs(networkId, {
    event: SESSION_EVENT,
    cpe_key: cpeKey,
    start: start.toISOString(),
    end: end.toISOString(),
    size: Math.min(limit, ACS_LOGS_MAX_SIZE),
  });
}

function SessionRow({
  s,
  networkId,
  showDevice,
}: {
  s: AcsLog;
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
  const [limit, setLimit] = useState(SESSIONS_PAGE);
  const {data, error, isLoading, reload} = usePoll(
    () => fetchSessions(networkId, hours, limit, cpeKey),
    [networkId, cpeKey, hours],
  );
  // A bigger page refetches without clearing the rows already shown.
  const firstLimit = useRef(true);
  useEffect(() => {
    if (firstLimit.current) {
      firstLimit.current = false;
      return;
    }
    reload();
  }, [limit, reload]);
  const changeRange = (h: number) => {
    setHours(h);
    setLimit(SESSIONS_PAGE);
  };
  const showDevice = !cpeKey;
  const q = search.trim().toLowerCase();
  const loaded = data?.logs ?? [];
  const total = data?.total_count ?? 0;
  const rows = loaded.filter(
    s =>
      (!result || s.result === result) &&
      (!q || (s.cpe_key ?? '').toLowerCase().includes(q)),
  );
  const filtered = Boolean(result || q);
  const count =
    total > loaded.length
      ? `${filtered ? `${rows.length} of ` : ''}${loaded.length} of ${total}`
      : `${rows.length}`;
  const rangeLabel = RANGES.find(([, h]) => h === hours)?.[0] ?? '';

  return (
    <>
      <CardTitleRow
        icon={ListIcon}
        label={
          cpeKey ? `Sessions of this CPE (${count})` : `Sessions (${count})`
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
            <RangeSelect hours={hours} onChange={changeRange} />
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
        {total > loaded.length && (
          <Box
            data-testid="acs-sessions-more"
            sx={{display: 'flex', alignItems: 'center', gap: 2, p: 2}}>
            {loaded.length < ACS_LOGS_MAX_SIZE ? (
              <Button
                size="small"
                variant="outlined"
                onClick={() =>
                  setLimit(Math.min(limit + SESSIONS_PAGE, ACS_LOGS_MAX_SIZE))
                }>
                Load more
              </Button>
            ) : null}
            <span>
              Showing the newest {loaded.length} of {total} sessions
              {filtered ? '; the filters apply to those' : ''}
              {loaded.length >= ACS_LOGS_MAX_SIZE
                ? '. Pick a shorter range to see older ones.'
                : '.'}
            </span>
          </Box>
        )}
      </AcsPaper>
    </>
  );
}
