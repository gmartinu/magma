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
import AccountTreeIcon from '@mui/icons-material/AccountTree';
import AcsAPI, {AcsParameter} from './AcsAPI';
import Alert from '@mui/material/Alert';
import Box from '@mui/material/Box';
import CardTitleRow from '../../components/layout/CardTitleRow';
import Chip from '@mui/material/Chip';
import InputAdornment from '@mui/material/InputAdornment';
import Link from '@mui/material/Link';
import Paper from '@mui/material/Paper';
import React, {useMemo, useState} from 'react';
import SearchIcon from '@mui/icons-material/Search';
import Table from '@mui/material/Table';
import TableBody from '@mui/material/TableBody';
import TableCell from '@mui/material/TableCell';
import TableHead from '@mui/material/TableHead';
import TablePagination from '@mui/material/TablePagination';
import TableRow from '@mui/material/TableRow';
import Text from '../../theme/design-system/Text';
import TextField from '@mui/material/TextField';
import {
  AcsPaper,
  EmptyBlock,
  RetryButton,
  SkeletonRows,
  usePoll,
} from './AcsCommon';
import {colors} from '../../theme/default';
import {errorMessage, httpStatus} from './AcsUtils';

const JUMPS = [
  'Device.Cellular.',
  'Device.DeviceInfo.',
  'Device.ManagementServer.',
  'Device.IP.',
  'Device.WiFi.',
];

// Counts per subtree, two levels below the root (Device.Cellular., ...).
export function subtreeCounts(
  params: Array<AcsParameter>,
): Array<[string, number]> {
  const counts = new Map<string, number>();
  for (const p of params) {
    const parts = p.name.split('.');
    const key = parts.length > 2 ? `${parts[0]}.${parts[1]}.` : `${parts[0]}.`;
    counts.set(key, (counts.get(key) ?? 0) + 1);
  }
  return Array.from(counts.entries()).sort(([a], [b]) => a.localeCompare(b));
}

// Suggest the subtree the operator probably meant: a case-insensitive
// match, else the first subtree sharing the first 8 characters.
export function didYouMean(
  prefix: string,
  subtrees: Array<string>,
): string | undefined {
  const p = prefix.toLowerCase();
  if (!p) {
    return undefined;
  }
  return (
    subtrees.find(s => s.toLowerCase().startsWith(p)) ??
    subtrees.find(
      s => p.length >= 8 && s.toLowerCase().startsWith(p.slice(0, 8)),
    )
  );
}

export default function AcsParameters({
  networkId,
  cpeKey,
}: {
  networkId: string;
  cpeKey: string;
}) {
  const [prefix, setPrefix] = useState('');
  const [page, setPage] = useState(0);
  // The whole tree is fetched once and filtered here: prefix search must
  // react per keystroke and the tree needs the global counts.
  const {data, error, isLoading, reload} = usePoll(
    () => AcsAPI.listParameters(networkId, cpeKey),
    [networkId, cpeKey],
    0,
  );
  const all = useMemo(() => data ?? [], [data]);
  const subtrees = useMemo(() => subtreeCounts(all), [all]);
  const rows = prefix ? all.filter(p => p.name.startsWith(prefix)) : all;
  const pageSize = 50;
  const suggestion = didYouMean(
    prefix,
    subtrees.map(([name]) => name),
  );
  const choose = (p: string) => {
    setPrefix(p);
    setPage(0);
  };

  if (error && !data) {
    return (
      <Alert severity="error" action={<RetryButton onClick={reload} />}>
        <b>Couldn't load parameters.</b>{' '}
        {httpStatus(error) === 503
          ? "The CPE's gateway is unreachable; parameters are read live from its acsd."
          : errorMessage(error)}
      </Alert>
    );
  }

  return (
    <>
      <CardTitleRow
        icon={AccountTreeIcon}
        label={`Parameters (${all.length})`}
        filter={() => (
          <TextField
            size="small"
            autoFocus
            sx={{width: 420}}
            placeholder="Device.Cellular."
            value={prefix}
            onChange={e => choose(e.target.value)}
            InputProps={{
              startAdornment: (
                <InputAdornment position="start">
                  <SearchIcon fontSize="small" />
                </InputAdornment>
              ),
              endAdornment: (
                <InputAdornment position="end">prefix</InputAdornment>
              ),
              style: {fontFamily: 'monospace'},
            }}
            inputProps={{'data-testid': 'param-prefix'}}
          />
        )}
      />
      <Box
        sx={{
          display: 'flex',
          gap: 1,
          flexWrap: 'wrap',
          mb: 2,
          alignItems: 'center',
        }}>
        <Text variant="body3">Jump to</Text>
        {JUMPS.map(j => (
          <Chip
            key={j}
            label={j}
            size="small"
            variant={prefix === j ? 'filled' : 'outlined'}
            onClick={() => choose(j)}
            sx={{fontFamily: 'monospace'}}
          />
        ))}
      </Box>
      <Box sx={{display: 'flex', gap: 3, alignItems: 'flex-start'}}>
        <Paper elevation={0} sx={{width: 290, flex: 'none', py: 1}}>
          <Box
            sx={{
              px: 2,
              pb: 1,
              borderBottom: `1px solid ${colors.primary.mercury}`,
            }}>
            <Text variant="body2" weight="medium">
              Tree
            </Text>
          </Box>
          {subtrees.map(([name, count]) => (
            <Box
              key={name}
              onClick={() => choose(name)}
              data-testid="param-tree-node"
              sx={{
                display: 'flex',
                justifyContent: 'space-between',
                px: 2,
                py: 0.5,
                cursor: 'pointer',
                fontFamily: 'monospace',
                fontSize: 13,
                backgroundColor:
                  prefix === name ? colors.primary.selago : undefined,
              }}>
              <span>{name}</span>
              <span style={{color: colors.primary.gullGray}}>{count}</span>
            </Box>
          ))}
        </Paper>
        <Box sx={{flex: 1, minWidth: 0}}>
          <AcsPaper>
            {data && rows.length === 0 ? (
              <EmptyBlock title={`No parameter starts with "${prefix}"`}>
                The prefix is case-sensitive and matches from the start of the
                name.
                {suggestion && (
                  <>
                    {' '}
                    Did you mean{' '}
                    <Link component="button" onClick={() => choose(suggestion)}>
                      {suggestion}
                    </Link>
                    ?
                  </>
                )}
              </EmptyBlock>
            ) : (
              <>
                <Table size="small">
                  <TableHead>
                    <TableRow>
                      <TableCell>Name</TableCell>
                      <TableCell>Value</TableCell>
                    </TableRow>
                  </TableHead>
                  <TableBody>
                    {isLoading && !data ? (
                      <SkeletonRows rows={6} cols={2} />
                    ) : (
                      rows
                        .slice(page * pageSize, (page + 1) * pageSize)
                        .map(p => (
                          <TableRow key={p.name} hover data-testid="param-row">
                            <TableCell
                              sx={{fontFamily: 'monospace', fontSize: 12.5}}>
                              {p.name}
                            </TableCell>
                            <TableCell
                              sx={{
                                fontFamily: 'monospace',
                                fontSize: 12.5,
                                fontWeight: 500,
                              }}>
                              {p.value}
                            </TableCell>
                          </TableRow>
                        ))
                    )}
                  </TableBody>
                </Table>
                <TablePagination
                  component="div"
                  count={rows.length}
                  page={page}
                  rowsPerPage={pageSize}
                  rowsPerPageOptions={[pageSize]}
                  onPageChange={(_, p) => setPage(p)}
                />
              </>
            )}
          </AcsPaper>
        </Box>
      </Box>
    </>
  );
}
