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
import AssignmentTurnedInIcon from '@mui/icons-material/AssignmentTurnedIn';
import Box from '@mui/material/Box';
import CardTitleRow from '../../components/layout/CardTitleRow';
import IconButton from '@mui/material/IconButton';
import KeyboardArrowDownIcon from '@mui/icons-material/KeyboardArrowDown';
import KeyboardArrowRightIcon from '@mui/icons-material/KeyboardArrowRight';
import MenuItem from '@mui/material/MenuItem';
import Paper from '@mui/material/Paper';
import React, {useState} from 'react';
import Table from '@mui/material/Table';
import TableBody from '@mui/material/TableBody';
import TableCell from '@mui/material/TableCell';
import TableHead from '@mui/material/TableHead';
import TableRow from '@mui/material/TableRow';
import Text from '../../theme/design-system/Text';
import TextField from '@mui/material/TextField';
import {AcsPaper, EmptyBlock, TaskStatusChip} from './AcsCommon';
import {AcsTask, AcsTaskStatus} from './AcsAPI';
import {TASK_LABELS, TASK_STATUS, formatTime, taskLabel} from './AcsUtils';
import {colors} from '../../theme/default';

// runsAt says when a pending task runs (taskRunsAt).
export function taskResult(t: AcsTask, runsAt?: string): React.ReactNode {
  switch (t.status) {
    case 'pending':
      return runsAt ?? 'Waiting for next Inform';
    case 'in_progress':
      return 'Running inside a session';
    case 'failed':
      return t.fault_code ? (
        <span>
          <b>{t.fault_code}</b> {t.fault_string}
        </span>
      ) : (
        'Sessions kept dropping; no retries left'
      );
    case 'expired':
      return 'No Inform before the TTL; nothing was run';
    default: {
      const values = (t.result as {values?: Record<string, unknown>})?.values;
      return values
        ? `${Object.keys(values).length} parameters read`
        : 'CPE confirmed';
    }
  }
}

function Json({label, value}: {label: string; value: unknown}) {
  return (
    <Box>
      <Text variant="body3">{label}</Text>
      <Box
        component="pre"
        sx={{
          m: 0,
          mt: 0.5,
          p: 1.5,
          fontSize: 12,
          backgroundColor: colors.primary.white,
          border: `1px solid ${colors.primary.mercury}`,
          overflowX: 'auto',
          maxHeight: 240,
        }}>
        {JSON.stringify(value ?? {}, null, 2)}
      </Box>
    </Box>
  );
}

function TaskRow({task, runsAt}: {task: AcsTask; runsAt?: string}) {
  const [open, setOpen] = useState(task.status === 'failed');
  return (
    <>
      <TableRow hover data-testid="acs-task-row">
        <TableCell sx={{width: 36}}>
          <IconButton
            size="small"
            onClick={() => setOpen(!open)}
            aria-label="expand">
            {open ? (
              <KeyboardArrowDownIcon fontSize="small" />
            ) : (
              <KeyboardArrowRightIcon fontSize="small" />
            )}
          </IconButton>
        </TableCell>
        <TableCell sx={{fontWeight: 500}}>{taskLabel(task.type)}</TableCell>
        <TableCell>
          <TaskStatusChip status={task.status} />
        </TableCell>
        <TableCell>{formatTime(task.created)}</TableCell>
        <TableCell>{formatTime(task.updated)}</TableCell>
        <TableCell>
          {task.attempts} / {task.max_attempts}
        </TableCell>
        <TableCell>{formatTime(task.deadline)}</TableCell>
        <TableCell>{taskResult(task, runsAt)}</TableCell>
      </TableRow>
      {open && (
        <TableRow sx={{backgroundColor: colors.primary.concrete}}>
          <TableCell colSpan={8}>
            <Box
              sx={{
                display: 'grid',
                gridTemplateColumns: '1fr 1fr 1fr',
                gap: 2,
                pl: 5,
              }}>
              <Json
                label="Fault (last attempt)"
                value={{
                  fault_code: task.fault_code ?? 0,
                  fault_string: task.fault_string ?? '',
                }}
              />
              <Json label="Arguments" value={{type: task.type, ...task.args}} />
              <Json label="Result" value={task.result} />
            </Box>
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

// Task history of a CPE, newest first (the API returns run order).
export default function AcsTasks({
  tasks,
  runsAt,
}: {
  tasks: Array<AcsTask>;
  runsAt?: string;
}) {
  const [status, setStatus] = useState('');
  const [type, setType] = useState('');
  const rows = [...tasks]
    .reverse()
    .filter(
      t => (!status || t.status === status) && (!type || t.type === type),
    );

  return (
    <>
      <CardTitleRow
        icon={AssignmentTurnedInIcon}
        label="Task history"
        filter={() => (
          <Box sx={{display: 'flex', gap: 1}}>
            <TextField
              select
              size="small"
              label="Status"
              value={status}
              sx={{minWidth: 140}}
              onChange={e => setStatus(e.target.value)}>
              <MenuItem value="">All</MenuItem>
              {Object.entries(TASK_STATUS).map(([k, v]) => (
                <MenuItem key={k} value={k}>
                  {v.label}
                </MenuItem>
              ))}
            </TextField>
            <TextField
              select
              size="small"
              label="Type"
              value={type}
              sx={{minWidth: 140}}
              onChange={e => setType(e.target.value)}>
              <MenuItem value="">All</MenuItem>
              {Object.entries(TASK_LABELS).map(([k, v]) => (
                <MenuItem key={k} value={k}>
                  {v}
                </MenuItem>
              ))}
            </TextField>
          </Box>
        )}
      />
      <AcsPaper>
        {tasks.length === 0 ? (
          <EmptyBlock title="No tasks yet">
            Reboots, refreshes and factory resets you run on this CPE are listed
            here with their result.
          </EmptyBlock>
        ) : (
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell />
                <TableCell>Task</TableCell>
                <TableCell>Status</TableCell>
                <TableCell>Created</TableCell>
                <TableCell>Updated</TableCell>
                <TableCell>Attempts</TableCell>
                <TableCell>Expires</TableCell>
                <TableCell>Result</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {rows.map(t => (
                <TaskRow key={t.id} task={t} runsAt={runsAt} />
              ))}
            </TableBody>
          </Table>
        )}
      </AcsPaper>
      <Paper elevation={0} sx={{p: 2, mt: 2}}>
        <Text variant="body3">Status legend</Text>
        <Box
          sx={{display: 'flex', gap: 3, flexWrap: 'wrap', mt: 1, fontSize: 13}}>
          {(Object.keys(TASK_STATUS) as Array<AcsTaskStatus>).map(s => (
            <span key={s}>
              <TaskStatusChip status={s} /> {TASK_STATUS[s].help}
            </span>
          ))}
        </Box>
      </Paper>
    </>
  );
}
