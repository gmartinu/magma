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
import AcsAPI, {AcsCpe, AcsTask, AcsTaskType} from './AcsAPI';
import Alert from '@mui/material/Alert';
import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import Dialog from '@mui/material/Dialog';
import DialogActions from '@mui/material/DialogActions';
import DialogContent from '@mui/material/DialogContent';
import DialogTitle from '@mui/material/DialogTitle';
import ListItemIcon from '@mui/material/ListItemIcon';
import ListItemText from '@mui/material/ListItemText';
import Menu from '@mui/material/Menu';
import MenuItem from '@mui/material/MenuItem';
import MoreVertIcon from '@mui/icons-material/MoreVert';
import PowerSettingsNewIcon from '@mui/icons-material/PowerSettingsNew';
import React, {useState} from 'react';
import RefreshIcon from '@mui/icons-material/Refresh';
import RestoreIcon from '@mui/icons-material/Restore';
import Text from '../../theme/design-system/Text';
import TextField from '@mui/material/TextField';
import {
  TASK_TTL_SEC,
  cpeName,
  errorMessage,
  formatTime,
  taskLabel,
} from './AcsUtils';
import {colors} from '../../theme/default';

type Props = {
  networkId: string;
  cpe: AcsCpe;
  pendingTypes: Set<string>;
  onQueued: (task: AcsTask) => void;
};

function RebootDialog({
  cpe,
  networkId,
  onCancel,
  onConfirm,
  busy,
}: {
  cpe: AcsCpe;
  networkId: string;
  onCancel: () => void;
  onConfirm: () => void;
  busy: boolean;
}) {
  const expires = new Date(Date.now() + TASK_TTL_SEC * 1000);
  return (
    <Dialog open onClose={onCancel} maxWidth="sm" fullWidth>
      <DialogTitle>Reboot {cpeName(cpe)}?</DialogTitle>
      <DialogContent>
        {!cpe.online && (
          <Alert severity="warning" sx={{mt: 2}} data-testid="reboot-offline">
            <b>This CPE is offline</b> (last Inform{' '}
            {formatTime(cpe.last_inform)}
            ). The reboot waits until it contacts the ACS again, and expires on{' '}
            <b>{formatTime(expires.toISOString())}</b> if it doesn't.
          </Alert>
        )}
        <ul>
          {cpe.online ? (
            <li>
              The task is queued now and runs at the CPE's <b>next Inform</b> —
              not immediately.
            </li>
          ) : (
            <li>
              If the CPE is powered off or out of coverage, a reboot from here
              will not help; check the site first.
            </li>
          )}
          <li>
            The CPE restarts and its connection drops for{' '}
            <b>about 2–3 minutes</b>. Every device behind it loses internet
            until it re-attaches.
          </li>
          <li>
            Configuration is kept. The task is marked done when the CPE comes
            back with a 1 BOOT Inform. Network: {networkId}.
          </li>
        </ul>
      </DialogContent>
      <DialogActions>
        <Button onClick={onCancel}>Cancel</Button>
        <Button
          variant="contained"
          startIcon={<PowerSettingsNewIcon />}
          disabled={busy}
          onClick={onConfirm}>
          Queue reboot
        </Button>
      </DialogActions>
    </Dialog>
  );
}

export function FactoryResetDialog({
  cpe,
  onCancel,
  onConfirm,
  busy,
}: {
  cpe: AcsCpe;
  onCancel: () => void;
  onConfirm: () => void;
  busy: boolean;
}) {
  const [typed, setTyped] = useState('');
  const serial = cpeName(cpe);
  const ok = typed === serial;
  return (
    <Dialog open onClose={onCancel} maxWidth="sm" fullWidth>
      <DialogTitle
        sx={{backgroundColor: `${colors.state.errorAlt} !important`}}>
        Factory reset {serial}?
      </DialogTitle>
      <DialogContent>
        <p>
          <b>This erases every setting on the CPE and cannot be undone.</b> When
          the task runs (next Inform):
        </p>
        <ul>
          <li>
            Wi-Fi, LAN, port forwarding and APN settings go back to factory
            values. <b>Customers behind this CPE lose internet</b> until someone
            reconfigures it.
          </li>
          <li>
            The CPE reboots and contacts the ACS again with its bootstrap
            credentials.
          </li>
          <li>
            Nothing reconfigures it automatically: provisioning profiles come
            after the MVP. The last values stay readable in the Parameters tab.
          </li>
        </ul>
        <Text variant="body2">
          Type the serial number <b>{serial}</b> to confirm
        </Text>
        <TextField
          fullWidth
          size="small"
          sx={{mt: 1}}
          placeholder="Serial number"
          value={typed}
          onChange={e => setTyped(e.target.value)}
          error={typed !== '' && !ok}
          helperText={
            typed !== '' && !ok ? "Doesn't match the serial of this CPE." : ' '
          }
          inputProps={{
            'data-testid': 'factory-reset-serial',
            style: {fontFamily: 'monospace'},
          }}
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={onCancel}>Cancel</Button>
        <Button
          variant="contained"
          color="error"
          startIcon={<RestoreIcon />}
          disabled={!ok || busy}
          onClick={onConfirm}>
          Factory reset at next Inform
        </Button>
      </DialogActions>
    </Dialog>
  );
}

// Actions never depend on the CPE being online: they queue for its next
// Inform. A button is disabled only while the same task is already pending.
export default function AcsActions({
  networkId,
  cpe,
  pendingTypes,
  onQueued,
}: Props) {
  const [menu, setMenu] = useState<HTMLElement | null>(null);
  const [dialog, setDialog] = useState<'reboot' | 'factory_reset' | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const queue = async (type: AcsTaskType) => {
    setBusy(true);
    setError(null);
    try {
      const task = await AcsAPI.createTask(networkId, cpe.cpe_key, {type});
      setDialog(null);
      onQueued(task);
    } catch (e) {
      setError(`${taskLabel(type)} was not queued (${errorMessage(e)}).`);
    } finally {
      setBusy(false);
    }
  };

  const pending = (t: AcsTaskType) => pendingTypes.has(t);
  const hint = pending('reboot')
    ? 'Reboot already queued. It runs at the next Inform.'
    : cpe.online
    ? "Actions run at the CPE's next Inform."
    : 'CPE offline: actions are queued and run when it next contacts the ACS (they expire after 7 days).';

  return (
    <Box sx={{textAlign: 'right'}}>
      <Box sx={{display: 'flex', gap: 1, justifyContent: 'flex-end'}}>
        <Button
          variant="outlined"
          startIcon={<RefreshIcon />}
          disabled={busy || pending('refresh')}
          onClick={() => void queue('refresh')}>
          Refresh
        </Button>
        <Button
          variant="outlined"
          startIcon={<PowerSettingsNewIcon />}
          disabled={busy || pending('reboot')}
          onClick={() => setDialog('reboot')}>
          Reboot
        </Button>
        <Button
          variant="outlined"
          aria-label="More actions"
          onClick={e => setMenu(e.currentTarget)}>
          <MoreVertIcon fontSize="small" />
        </Button>
      </Box>
      <Box
        sx={{
          mt: 1,
          color: cpe.online ? colors.primary.comet : colors.state.warningAlt,
        }}>
        <Text variant="body3">{hint}</Text>
      </Box>
      {error && (
        <Alert severity="error" sx={{mt: 1}} onClose={() => setError(null)}>
          {error}
        </Alert>
      )}
      <Menu anchorEl={menu} open={!!menu} onClose={() => setMenu(null)}>
        <MenuItem
          disabled={pending('refresh')}
          onClick={() => {
            setMenu(null);
            void queue('refresh');
          }}>
          <ListItemIcon>
            <RefreshIcon fontSize="small" />
          </ListItemIcon>
          <ListItemText
            primary="Refresh parameters"
            secondary="Re-read the full parameter tree"
          />
        </MenuItem>
        <MenuItem
          disabled={pending('reboot')}
          onClick={() => {
            setMenu(null);
            setDialog('reboot');
          }}>
          <ListItemIcon>
            <PowerSettingsNewIcon fontSize="small" />
          </ListItemIcon>
          <ListItemText
            primary="Reboot"
            secondary="Restart the CPE; ~3 min without service"
          />
        </MenuItem>
        <MenuItem
          disabled={pending('factory_reset')}
          sx={{color: colors.state.errorAlt}}
          onClick={() => {
            setMenu(null);
            setDialog('factory_reset');
          }}>
          <ListItemIcon sx={{color: colors.state.errorAlt}}>
            <RestoreIcon fontSize="small" />
          </ListItemIcon>
          <ListItemText
            primary="Factory reset…"
            secondary="Erase all configuration on the CPE"
          />
        </MenuItem>
      </Menu>
      {dialog === 'reboot' && (
        <RebootDialog
          cpe={cpe}
          networkId={networkId}
          busy={busy}
          onCancel={() => setDialog(null)}
          onConfirm={() => void queue('reboot')}
        />
      )}
      {dialog === 'factory_reset' && (
        <FactoryResetDialog
          cpe={cpe}
          busy={busy}
          onCancel={() => setDialog(null)}
          onConfirm={() => void queue('factory_reset')}
        />
      )}
    </Box>
  );
}
