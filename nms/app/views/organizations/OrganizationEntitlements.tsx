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
import Button from '@mui/material/Button';
import Chip from '@mui/material/Chip';
import Grid from '@mui/material/Grid';
import Paper from '@mui/material/Paper';
import React, {useCallback, useEffect, useState} from 'react';
import Switch from '@mui/material/Switch';
import Table from '@mui/material/Table';
import TableBody from '@mui/material/TableBody';
import TableCell from '@mui/material/TableCell';
import TableHead from '@mui/material/TableHead';
import TableRow from '@mui/material/TableRow';
import Text from '../../theme/design-system/Text';
import TextField from '@mui/material/TextField';
import axios from 'axios';
import {colors} from '../../theme/default';
import {format} from 'date-fns';
import {getErrorMessage} from '../../util/ErrorUtils';

// Mirrors server/api/OrchestratorEntitlements.ts (Orc8r entitlement).
export type OrgEntitlement = {
  feature: string;
  enabled: boolean;
  not_after?: string | null;
  grace_days?: number;
  source?: string;
  state?: 'active' | 'grace' | 'frozen' | 'disabled';
};

type Feature = {feature: string; title: string};

type Draft = {enabled: boolean; notAfter: string; graceDays: string};

const STATE_STYLE: Record<string, [string, string, string]> = {
  active: ['Active', colors.state.positiveAlt, '#EFFAF2'],
  grace: ['Grace period', colors.state.warningAlt, colors.state.warningFill],
  frozen: ['Frozen (read-only)', colors.state.errorAlt, colors.state.errorFill],
  disabled: ['Disabled', colors.primary.comet, colors.primary.concrete],
};

export function StateChip({state}: {state?: string}) {
  if (!state) {
    return <span>Not entitled</span>;
  }
  const [label, color, fill] = STATE_STYLE[state] ?? [
    state,
    colors.primary.comet,
    colors.primary.concrete,
  ];
  return (
    <Chip
      size="small"
      label={label}
      sx={{color, backgroundColor: fill, borderRadius: '4px'}}
    />
  );
}

// datetime-local works in local time without a zone; the API in RFC 3339.
export function toLocalInput(iso?: string | null): string {
  if (!iso) {
    return '';
  }
  const d = new Date(iso);
  return isNaN(d.getTime()) ? '' : format(d, "yyyy-MM-dd'T'HH:mm");
}

export function toIso(local: string): string | null {
  if (!local) {
    return null;
  }
  const d = new Date(local);
  return isNaN(d.getTime()) ? null : d.toISOString();
}

function draftOf(e?: OrgEntitlement): Draft {
  return {
    enabled: e?.enabled ?? false,
    notAfter: toLocalInput(e?.not_after),
    graceDays: e?.grace_days !== undefined ? String(e.grace_days) : '',
  };
}

// Licensed features of an organization (Orc8r entitlements on its tenant):
// switch on/off, expiry and grace period, with the state Orc8r computes.
export default function OrganizationEntitlements({name}: {name: string}) {
  const url = `/host/organization/async/${encodeURIComponent(
    name,
  )}/entitlements`;
  const [features, setFeatures] = useState<Array<Feature>>([]);
  const [current, setCurrent] = useState<Record<string, OrgEntitlement>>({});
  const [drafts, setDrafts] = useState<Record<string, Draft>>({});
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const [saving, setSaving] = useState<string | null>(null);

  const load = useCallback(() => {
    axios
      .get<{features: Array<Feature>; entitlements: Array<OrgEntitlement>}>(url)
      .then(res => {
        const byFeature: Record<string, OrgEntitlement> = {};
        res.data.entitlements.forEach(e => (byFeature[e.feature] = e));
        setFeatures(res.data.features);
        setCurrent(byFeature);
        const d: Record<string, Draft> = {};
        res.data.features.forEach(
          f => (d[f.feature] = draftOf(byFeature[f.feature])),
        );
        setDrafts(d);
        setError(null);
      })
      .catch(e => setError(getErrorMessage(e)));
  }, [url]);

  useEffect(load, [load]);

  const save = async (feature: string) => {
    const d = drafts[feature];
    setSaving(feature);
    setSaved(null);
    try {
      await axios.put(`${url}/${encodeURIComponent(feature)}`, {
        enabled: d.enabled,
        not_after: toIso(d.notAfter),
        ...(d.graceDays !== '' ? {grace_days: parseInt(d.graceDays, 10)} : {}),
      });
      setSaved(feature);
      load();
    } catch (e) {
      setError(getErrorMessage(e));
    } finally {
      setSaving(null);
    }
  };

  const set = (feature: string, patch: Partial<Draft>) =>
    setDrafts({...drafts, [feature]: {...drafts[feature], ...patch}});

  return (
    <Paper elevation={0} data-testid="organization-entitlements">
      {error && (
        <Alert severity="error" onClose={() => setError(null)}>
          Couldn't read or save entitlements: {error}
        </Alert>
      )}
      {saved && (
        <Alert severity="success" onClose={() => setSaved(null)}>
          {saved} saved. The NMS applies it within a minute.
        </Alert>
      )}
      <Table size="small">
        <TableHead>
          <TableRow>
            <TableCell>Feature</TableCell>
            <TableCell>State</TableCell>
            <TableCell>Enabled</TableCell>
            <TableCell>Expires</TableCell>
            <TableCell>Grace days</TableCell>
            <TableCell />
          </TableRow>
        </TableHead>
        <TableBody>
          {features.map(f => {
            const d = drafts[f.feature] ?? draftOf();
            return (
              <TableRow
                key={f.feature}
                data-testid={`entitlement-${f.feature}`}>
                <TableCell>
                  <Text variant="body2" weight="medium">
                    {f.title}
                  </Text>
                </TableCell>
                <TableCell>
                  <StateChip state={current[f.feature]?.state} />
                </TableCell>
                <TableCell>
                  <Switch
                    checked={d.enabled}
                    onChange={e => set(f.feature, {enabled: e.target.checked})}
                    inputProps={{'aria-label': `${f.feature} enabled`}}
                  />
                </TableCell>
                <TableCell>
                  <TextField
                    type="datetime-local"
                    size="small"
                    value={d.notAfter}
                    helperText={d.notAfter ? ' ' : 'Never'}
                    onChange={e => set(f.feature, {notAfter: e.target.value})}
                    inputProps={{'data-testid': `${f.feature}-not-after`}}
                  />
                </TableCell>
                <TableCell>
                  <TextField
                    type="number"
                    size="small"
                    sx={{width: 100}}
                    placeholder="30"
                    value={d.graceDays}
                    helperText=" "
                    onChange={e => set(f.feature, {graceDays: e.target.value})}
                    inputProps={{min: 0, max: 3650}}
                  />
                </TableCell>
                <TableCell>
                  <Button
                    variant="outlined"
                    disabled={saving === f.feature}
                    onClick={() => void save(f.feature)}>
                    Save
                  </Button>
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
      <Grid sx={{p: 2}}>
        <Text variant="body3">
          Past its expiry a feature keeps working for the grace days, then
          freezes: read-only in the NMS and on the gateways, nothing is removed.
        </Text>
      </Grid>
    </Paper>
  );
}
