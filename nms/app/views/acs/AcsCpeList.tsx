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
import AcsAPI, {AcsCpe, AcsCpeMode} from './AcsAPI';
import Alert from '@mui/material/Alert';
import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import CardTitleRow from '../../components/layout/CardTitleRow';
import InputAdornment from '@mui/material/InputAdornment';
import Link from '@mui/material/Link';
import MenuItem from '@mui/material/MenuItem';
import Paper from '@mui/material/Paper';
import React, {useMemo, useState} from 'react';
import RouterIcon from '@mui/icons-material/Router';
import SearchIcon from '@mui/icons-material/Search';
import Table from '@mui/material/Table';
import TableBody from '@mui/material/TableBody';
import TableCell from '@mui/material/TableCell';
import TableHead from '@mui/material/TableHead';
import TablePagination from '@mui/material/TablePagination';
import TableRow from '@mui/material/TableRow';
import Text from '../../theme/design-system/Text';
import TextField from '@mui/material/TextField';
import nullthrows from '../../../shared/util/nullthrows';
import {
  AcsPaper,
  EmptyBlock,
  ModeChip,
  OnlineStatus,
  PendingChip,
  RetryButton,
  SignalBars,
  SkeletonRows,
  usePoll,
} from './AcsCommon';
import {Link as RouterLink, useNavigate, useParams} from 'react-router-dom';
import {
  WEAK_RSRP_DBM,
  acsPath,
  cpeName,
  cpePath,
  errorMessage,
  formatAgo,
  formatTime,
} from './AcsUtils';
import {colors} from '../../theme/default';
import {latestSignal} from './AcsMetrics';

type Signal = Record<string, {rsrp?: number; sinr?: number}>;

export function useCpes(networkId: string) {
  return usePoll(async () => {
    const cpes = await AcsAPI.listCpes(networkId);
    // A Prometheus outage must not hide the list: signal is best effort.
    const signal: Signal = await latestSignal(networkId).catch(() => ({}));
    return {cpes, signal};
  }, [networkId]);
}

function Kpi({label, value}: {label: string; value: number | string}) {
  return (
    <Paper elevation={0} sx={{p: 2, flex: 1, minWidth: 150}}>
      <Text variant="body3">{label}</Text>
      <Box
        sx={{fontSize: 24, fontWeight: 600, color: colors.primary.brightGray}}>
        {value}
      </Box>
    </Paper>
  );
}

export function KpiTray({cpes, signal}: {cpes: Array<AcsCpe>; signal: Signal}) {
  const online = cpes.filter(c => c.online).length;
  const weak = cpes.filter(c => (signal[c.cpe_key]?.rsrp ?? 0) < WEAK_RSRP_DBM)
    .length;
  const pending = cpes.reduce((n, c) => n + (c.pending_tasks ?? 0), 0);
  return (
    <Box
      sx={{display: 'flex', gap: 2, flexWrap: 'wrap', mb: 3}}
      data-testid="acs-kpis">
      <Kpi label="CPEs" value={cpes.length} />
      <Kpi label="Online" value={online} />
      <Kpi label="Offline" value={cpes.length - online} />
      <Kpi label={`Weak signal (RSRP < ${WEAK_RSRP_DBM} dBm)`} value={weak} />
      <Kpi label="Tasks waiting for Inform" value={pending} />
    </Box>
  );
}

type Filters = {
  search: string;
  model: string;
  status: '' | 'online' | 'offline';
  mode: '' | AcsCpeMode;
};

const NO_FILTERS: Filters = {search: '', model: '', status: '', mode: ''};

export function filterCpes(cpes: Array<AcsCpe>, f: Filters): Array<AcsCpe> {
  const q = f.search.trim().toLowerCase();
  return cpes.filter(c => {
    if (f.model && (c.model_name || c.product_class) !== f.model) return false;
    if (f.status && c.online !== (f.status === 'online')) return false;
    if (f.mode && c.mode !== f.mode) return false;
    if (!q) return true;
    return [c.serial_number, c.cpe_key, c.imsi, c.software_version, c.oui]
      .filter(Boolean)
      .some(v => String(v).toLowerCase().includes(q));
  });
}

function Select({
  label,
  value,
  onChange,
  options,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  options: Array<[string, string]>;
}) {
  return (
    <TextField
      select
      size="small"
      label={label}
      value={value}
      onChange={e => onChange(e.target.value)}
      sx={{minWidth: 140}}
      inputProps={{'data-testid': `filter-${label}`}}>
      {options.map(([v, l]) => (
        <MenuItem key={v} value={v}>
          {l}
        </MenuItem>
      ))}
    </TextField>
  );
}

export default function AcsCpeList() {
  const networkId = nullthrows(useParams().networkId);
  const navigate = useNavigate();
  const {data, error, isLoading, reload} = useCpes(networkId);
  const [filters, setFilters] = useState<Filters>(NO_FILTERS);
  const [page, setPage] = useState(0);
  const [rowsPerPage, setRowsPerPage] = useState(25);

  const cpes = useMemo(() => data?.cpes ?? [], [data]);
  const signal = data?.signal ?? {};
  const models = useMemo(
    () =>
      Array.from(
        new Set(cpes.map(c => c.model_name || c.product_class).filter(Boolean)),
      ) as Array<string>,
    [cpes],
  );
  const rows = filterCpes(cpes, filters);
  const filtered = Object.values(filters).some(Boolean);
  const set = (patch: Partial<Filters>) => {
    setFilters({...filters, ...patch});
    setPage(0);
  };

  const toolbar = () => (
    <Box sx={{display: 'flex', gap: 1, flexWrap: 'wrap'}}>
      <TextField
        size="small"
        placeholder="Search serial, IMSI, claim key, firmware"
        value={filters.search}
        onChange={e => set({search: e.target.value})}
        sx={{width: 300}}
        InputProps={{
          startAdornment: (
            <InputAdornment position="start">
              <SearchIcon fontSize="small" />
            </InputAdornment>
          ),
        }}
        inputProps={{'data-testid': 'acs-search'}}
      />
      <Select
        label="Model"
        value={filters.model}
        onChange={model => set({model})}
        options={[
          ['', 'All models'],
          ...models.map(m => [m, m] as [string, string]),
        ]}
      />
      <Select
        label="Status"
        value={filters.status}
        onChange={status => set({status: status as Filters['status']})}
        options={[
          ['', 'All'],
          ['online', 'Online'],
          ['offline', 'Offline'],
        ]}
      />
      <Select
        label="Mode"
        value={filters.mode}
        onChange={mode => set({mode: mode as Filters['mode']})}
        options={[
          ['', 'All'],
          ['core', 'Core'],
          ['claimed', 'Claimed'],
        ]}
      />
    </Box>
  );

  if (!isLoading && !error && cpes.length === 0) {
    return (
      <AcsPaper>
        <EmptyBlock
          title={`No CPEs in ${networkId} yet`}
          action={
            <Button
              variant="contained"
              onClick={() =>
                navigate(acsPath(networkId, 'overview', 'claims'))
              }>
              Go to Claims
            </Button>
          }>
          A CPE behind one of the network's gateways (core mode) shows up here
          after its first Inform to acsd. A CPE on any other network (claimed
          mode) shows up once you claim it by OUI, product class and serial
          number.
        </EmptyBlock>
      </AcsPaper>
    );
  }

  return (
    <>
      {error ? (
        <Alert
          severity="error"
          sx={{mb: 2}}
          action={<RetryButton onClick={reload} />}>
          <b>Couldn't load CPEs.</b> GET /acs/{networkId}/cpes →{' '}
          {errorMessage(error)}.
          {data ? ' The list below may be out of date.' : ''}
        </Alert>
      ) : null}
      <KpiTray cpes={cpes} signal={signal} />
      <CardTitleRow
        icon={RouterIcon}
        label={`CPEs (${rows.length} of ${cpes.length})`}
        filter={toolbar}
      />
      <AcsPaper>
        <Box sx={{opacity: error && data ? 0.55 : 1}}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Status</TableCell>
                <TableCell>Serial number</TableCell>
                <TableCell>Mode</TableCell>
                <TableCell>Model</TableCell>
                <TableCell>Firmware</TableCell>
                <TableCell>Gateway</TableCell>
                <TableCell>Last Inform</TableCell>
                <TableCell>Signal (RSRP · SINR)</TableCell>
                <TableCell>Tasks</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {isLoading && !data ? (
                <SkeletonRows rows={5} cols={9} />
              ) : (
                rows
                  .slice(page * rowsPerPage, (page + 1) * rowsPerPage)
                  .map(c => (
                    <TableRow key={c.cpe_key} hover data-testid="acs-cpe-row">
                      <TableCell>
                        <OnlineStatus online={c.online} />
                      </TableCell>
                      <TableCell>
                        <Link
                          component={RouterLink}
                          to={cpePath(networkId, c.cpe_key)}>
                          {cpeName(c)}
                        </Link>
                      </TableCell>
                      <TableCell>
                        <ModeChip mode={c.mode} />
                      </TableCell>
                      <TableCell>
                        {c.model_name || c.product_class || '—'}
                      </TableCell>
                      <TableCell sx={{fontFamily: 'monospace'}}>
                        {c.software_version || '—'}
                      </TableCell>
                      <TableCell>{c.gateway_id || '—'}</TableCell>
                      <TableCell title={formatTime(c.last_inform)}>
                        {formatAgo(c.last_inform)}
                      </TableCell>
                      <TableCell>
                        <SignalBars
                          rsrp={signal[c.cpe_key]?.rsrp}
                          sinr={signal[c.cpe_key]?.sinr}
                          stale={!c.online}
                        />
                      </TableCell>
                      <TableCell>
                        <PendingChip count={c.pending_tasks} />
                      </TableCell>
                    </TableRow>
                  ))
              )}
            </TableBody>
          </Table>
          {data && rows.length === 0 && filtered ? (
            <EmptyBlock
              title="No CPEs match these filters"
              action={
                <Button variant="outlined" onClick={() => set(NO_FILTERS)}>
                  Clear filters
                </Button>
              }
            />
          ) : (
            <TablePagination
              component="div"
              count={rows.length}
              page={page}
              rowsPerPage={rowsPerPage}
              rowsPerPageOptions={[25, 50, 100]}
              onPageChange={(_, p) => setPage(p)}
              onRowsPerPageChange={e => {
                setRowsPerPage(parseInt(e.target.value, 10));
                setPage(0);
              }}
            />
          )}
        </Box>
      </AcsPaper>
    </>
  );
}
