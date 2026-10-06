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
import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import Chip from '@mui/material/Chip';
import Paper from '@mui/material/Paper';
import React, {useCallback, useEffect, useRef, useState} from 'react';
import Skeleton from '@mui/material/Skeleton';
import TableCell from '@mui/material/TableCell';
import TableRow from '@mui/material/TableRow';
import Text from '../../theme/design-system/Text';
import {AcsTaskStatus} from './AcsAPI';
import {TASK_STATUS, modeLabel, signalBars} from './AcsUtils';
import {colors} from '../../theme/default';

export function StatusDot({
  online,
  size = 8,
}: {
  online: boolean;
  size?: number;
}) {
  return (
    <Box
      component="span"
      sx={{
        display: 'inline-block',
        width: size,
        height: size,
        borderRadius: '50%',
        mr: 1,
        backgroundColor: online ? colors.state.positive : colors.state.error,
      }}
    />
  );
}

export function OnlineStatus({online}: {online: boolean}) {
  return (
    <Box component="span" sx={{display: 'inline-flex', alignItems: 'center'}}>
      <StatusDot online={online} />
      {online ? 'Online' : 'Offline'}
    </Box>
  );
}

function SoftChip({
  label,
  color,
  fill,
  testId,
}: {
  label: React.ReactNode;
  color: string;
  fill: string;
  testId?: string;
}) {
  return (
    <Chip
      size="small"
      label={label}
      data-testid={testId}
      sx={{color, backgroundColor: fill, fontWeight: 500, borderRadius: '4px'}}
    />
  );
}

export function ModeChip({mode}: {mode: string}) {
  const claimed = mode === 'claimed';
  return (
    <SoftChip
      label={modeLabel(mode)}
      color={claimed ? colors.data.studio : colors.secondary.mariner}
      fill={claimed ? '#F3EFFC' : colors.primary.selago}
    />
  );
}

export function TaskStatusChip({status}: {status: string}) {
  const s = TASK_STATUS[status as AcsTaskStatus] ?? TASK_STATUS.expired;
  return <SoftChip label={s.label} color={s.color} fill={s.fill} />;
}

export function PendingChip({count}: {count?: number}) {
  if (!count) {
    return null;
  }
  return (
    <SoftChip
      label={`${count} pending`}
      color={TASK_STATUS.pending.color}
      fill={TASK_STATUS.pending.fill}
    />
  );
}

export function SessionResultChip({result}: {result?: string}) {
  if (!result) {
    return <span>—</span>;
  }
  const map: Record<string, [string, string, string]> = {
    completed: ['OK', colors.state.positiveAlt, '#EFFAF2'],
    timed_out: ['Timed out', colors.state.warningAlt, colors.state.warningFill],
    interrupted: ['Interrupted', colors.state.errorAlt, colors.state.errorFill],
  };
  const [label, color, fill] = map[result] ?? [
    result,
    colors.primary.comet,
    colors.primary.concrete,
  ];
  return <SoftChip label={label} color={color} fill={fill} />;
}

export function SignalBars({
  rsrp,
  sinr,
  stale,
}: {
  rsrp?: number | null;
  sinr?: number | null;
  stale?: boolean;
}) {
  if (rsrp === undefined || rsrp === null) {
    return <Text variant="body3">no KPI yet</Text>;
  }
  const q = signalBars(rsrp);
  const color =
    q >= 3
      ? colors.state.positive
      : q === 2
      ? colors.state.warningAlt
      : colors.state.error;
  return (
    <Box
      component="span"
      sx={{display: 'inline-flex', alignItems: 'flex-end', gap: '6px'}}
      data-testid="signal"
      title={`${q} of 4 bars`}>
      <Box component="span" sx={{display: 'inline-flex', gap: '2px'}}>
        {[1, 2, 3, 4].map(i => (
          <Box
            key={i}
            component="span"
            sx={{
              width: 4,
              height: 4 + i * 3,
              borderRadius: '1px',
              alignSelf: 'flex-end',
              backgroundColor: i <= q ? color : colors.primary.mercury,
            }}
          />
        ))}
      </Box>
      <span>
        {rsrp} dBm
        {sinr !== undefined && sinr !== null ? ` · ${sinr.toFixed(1)} dB` : ''}
        {stale ? ' (last known)' : ''}
      </span>
    </Box>
  );
}

export function EmptyBlock({
  title,
  children,
  action,
}: {
  title: string;
  children?: React.ReactNode;
  action?: React.ReactNode;
}) {
  return (
    <Box sx={{textAlign: 'center', py: 6, px: 3}} data-testid="acs-empty">
      <Text variant="subtitle1">{title}</Text>
      {children && (
        <Box
          sx={{maxWidth: 560, mx: 'auto', mt: 1, color: colors.primary.comet}}>
          <Text variant="body2">{children}</Text>
        </Box>
      )}
      {action && <Box sx={{mt: 2}}>{action}</Box>}
    </Box>
  );
}

export function SkeletonRows({rows, cols}: {rows: number; cols: number}) {
  return (
    <>
      {Array.from({length: rows}).map((_, r) => (
        <TableRow key={r} data-testid="acs-skeleton">
          {Array.from({length: cols}).map((__, c) => (
            <TableCell key={c}>
              <Skeleton variant="text" width={60 + ((r + c) % 3) * 30} />
            </TableCell>
          ))}
        </TableRow>
      ))}
    </>
  );
}

export function AcsPaper({children}: {children: React.ReactNode}) {
  return (
    <Paper elevation={0} sx={{overflowX: 'auto'}}>
      {children}
    </Paper>
  );
}

export function RetryButton({onClick}: {onClick: () => void}) {
  return (
    <Button size="small" variant="outlined" onClick={onClick}>
      Retry
    </Button>
  );
}

// Fetches on mount, on deps change and every refreshMs; keeps the last good
// data on error so the page can dim it instead of wiping the table. Only
// the newest request lands: a slow answer for the previous deps (another
// CPE, another range) must not overwrite the current one, and a deps
// change clears the data so the old CPE's rows are not shown meanwhile.
export function usePoll<T>(
  fetcher: () => Promise<T>,
  deps: Array<unknown>,
  refreshMs = 30000,
): {
  data: T | null;
  error: unknown;
  isLoading: boolean;
  reload: () => void;
} {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [isLoading, setIsLoading] = useState(true);
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;
  const latest = useRef(0);

  const reload = useCallback(() => {
    const id = ++latest.current;
    fetcherRef
      .current()
      .then(result => {
        if (id === latest.current) {
          setData(result);
          setError(null);
        }
      })
      .catch(err => {
        if (id === latest.current) {
          setError(err);
        }
      })
      .finally(() => {
        if (id === latest.current) {
          setIsLoading(false);
        }
      });
  }, []);

  useEffect(() => {
    // The counter, not a DOM node: bumping it in the cleanup is the point.
    const requests = latest;
    setData(null);
    setError(null);
    setIsLoading(true);
    reload();
    const timer = refreshMs > 0 ? setInterval(reload, refreshMs) : undefined;
    return () => {
      if (timer) {
        clearInterval(timer);
      }
      // Drop whatever is still in flight for these deps.
      requests.current++;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reload, refreshMs, ...deps]);

  return {data, error, isLoading, reload};
}
