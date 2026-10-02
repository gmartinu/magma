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
import AcsAPI, {AcsClaim, AcsClaimRequest, AcsCpe} from './AcsAPI';
import AcsClaimsAPI, {CLAIMS_API_AVAILABLE} from './AcsClaimsStub';
import AddIcon from '@mui/icons-material/Add';
import Alert from '@mui/material/Alert';
import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import CardTitleRow from '../../components/layout/CardTitleRow';
import Dialog from '@mui/material/Dialog';
import DialogActions from '@mui/material/DialogActions';
import DialogContent from '@mui/material/DialogContent';
import DialogTitle from '@mui/material/DialogTitle';
import InboxIcon from '@mui/icons-material/Inbox';
import Link from '@mui/material/Link';
import React, {useState} from 'react';
import Table from '@mui/material/Table';
import TableBody from '@mui/material/TableBody';
import TableCell from '@mui/material/TableCell';
import TableHead from '@mui/material/TableHead';
import TableRow from '@mui/material/TableRow';
import TextField from '@mui/material/TextField';
import Tooltip from '@mui/material/Tooltip';
import nullthrows from '../../../shared/util/nullthrows';
import {
  AcsPaper,
  EmptyBlock,
  OnlineStatus,
  RetryButton,
  SkeletonRows,
  usePoll,
} from './AcsCommon';
import {Link as RouterLink, useParams} from 'react-router-dom';
import {cpePath, errorMessage, formatTime, httpStatus} from './AcsUtils';

const EMPTY_CLAIM: AcsClaimRequest = {
  oui: '',
  product_class: '',
  serial_number: '',
  label: '',
};

// OUI is six hex digits (IEEE); ProductClass and SerialNumber are free text
// on the CPE's label.
export function claimErrors(
  c: AcsClaimRequest,
): Partial<Record<keyof AcsClaimRequest, string>> {
  const errors: Partial<Record<keyof AcsClaimRequest, string>> = {};
  if (!/^[0-9A-Fa-f]{6}$/.test(c.oui.trim())) {
    errors.oui = 'Six hex digits, e.g. D4A5C2';
  }
  if (!c.product_class.trim()) {
    errors.product_class = 'Required';
  }
  if (!c.serial_number.trim()) {
    errors.serial_number = 'Required';
  }
  return errors;
}

export function ClaimDialog({
  networkId,
  open,
  onClose,
  onClaimed,
}: {
  networkId: string;
  open: boolean;
  onClose: () => void;
  onClaimed: (claim: AcsClaim) => void;
}) {
  const [claim, setClaim] = useState<AcsClaimRequest>(EMPTY_CLAIM);
  const [touched, setTouched] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const errors = claimErrors(claim);
  const valid = Object.keys(errors).length === 0;

  const field = (key: keyof AcsClaimRequest, label: string, mono = true) => (
    <TextField
      fullWidth
      margin="dense"
      label={label}
      value={claim[key] ?? ''}
      onChange={e => setClaim({...claim, [key]: e.target.value})}
      error={touched && !!errors[key]}
      helperText={touched ? errors[key] : undefined}
      inputProps={{
        'data-testid': `claim-${key}`,
        style: mono ? {fontFamily: 'monospace'} : undefined,
      }}
    />
  );

  const submit = async () => {
    setTouched(true);
    if (!valid) {
      return;
    }
    setSaving(true);
    setError(null);
    try {
      const created = await AcsClaimsAPI.createClaim(networkId, {
        oui: claim.oui.trim().toUpperCase(),
        product_class: claim.product_class.trim(),
        serial_number: claim.serial_number.trim(),
        label: claim.label?.trim() || undefined,
      });
      setClaim(EMPTY_CLAIM);
      setTouched(false);
      onClaimed(created);
    } catch (e) {
      setError(
        httpStatus(e) === 409
          ? 'This CPE is already claimed, by this network or another one.'
          : `The claim was not saved (${errorMessage(e)}).`,
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>Claim a CPE into {networkId}</DialogTitle>
      <DialogContent>
        <Box sx={{mt: 1}}>
          {field('oui', 'OUI')}
          {field('product_class', 'Product class')}
          {field('serial_number', 'Serial number')}
          {field('label', 'Label (optional)', false)}
        </Box>
        <p>
          <b>What happens when you claim it</b>
        </p>
        <ul>
          <li>
            The ACS accepts this CPE from any network, not only from behind a
            Magma gateway, and keys it by a claim (CLAIM…) instead of an IMSI.
          </li>
          <li>
            At its next Inform the ACS reads its parameter tree and starts
            recording RSRP, RSRQ and SINR for {networkId}.
          </li>
          <li>
            Every operator with access to {networkId} can then see its data and
            reboot or factory-reset it.
          </li>
        </ul>
        <Alert severity="warning">
          Point the CPE's ACS URL at the claimed-mode listener. Check the serial
          against the unit you installed: a claim for a wrong serial matches
          nothing.
        </Alert>
        {error && (
          <Alert severity="error" sx={{mt: 2}} data-testid="claim-error">
            {error}
          </Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          variant="contained"
          disabled={saving || (touched && !valid)}
          onClick={() => void submit()}>
          Claim into {networkId}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

type ClaimRow = {
  key: string;
  serial: string;
  oui?: string;
  productClass?: string;
  label?: string;
  created?: string;
  cpe?: AcsCpe;
};

// Without a claims API the table shows the claimed-mode CPEs the AGWs
// report; with one it shows every claim, seen or not.
export function claimRows(
  claims: Array<AcsClaim>,
  cpes: Array<AcsCpe>,
): Array<ClaimRow> {
  const byKey: Record<string, AcsCpe> = {};
  cpes.forEach(c => (byKey[c.cpe_key] = c));
  if (!CLAIMS_API_AVAILABLE) {
    return cpes.map(c => ({
      key: c.cpe_key,
      serial: c.serial_number ?? '',
      oui: c.oui,
      productClass: c.product_class,
      cpe: c,
    }));
  }
  return claims.map(c => {
    const key = c.cpe_key ?? `CLAIM${c.claim_id}`;
    return {
      key,
      serial: c.serial_number,
      oui: c.oui,
      productClass: c.product_class,
      label: c.label,
      created: c.created,
      cpe: byKey[key],
    };
  });
}

export default function AcsClaims() {
  const networkId = nullthrows(useParams().networkId);
  const [open, setOpen] = useState(false);
  const [claimed, setClaimed] = useState<AcsClaim | null>(null);
  const {data, error, isLoading, reload} = usePoll(async () => {
    const [claims, cpes] = await Promise.all([
      AcsClaimsAPI.listClaims(networkId),
      AcsAPI.listCpes(networkId, {mode: 'claimed'}),
    ]);
    return {claims, cpes};
  }, [networkId]);

  const rows = data ? claimRows(data.claims, data.cpes) : [];

  return (
    <>
      <Alert severity="info" sx={{mb: 2}}>
        <b>Claimed mode manages CPEs outside the Magma core.</b> The ACS only
        answers a CPE that is behind one of the network's gateways or that was
        claimed ahead of time by OUI, product class and serial number.
      </Alert>
      {!CLAIMS_API_AVAILABLE && (
        <Alert severity="warning" sx={{mb: 2}} data-testid="claims-coming-soon">
          <b>Claiming from the NMS is coming soon.</b> The Orchestrator has no
          claims API yet: add claims on the AGW with <code>acsd_cli.py</code>.
          The claimed CPEs the gateways report are listed below.
        </Alert>
      )}
      {claimed && (
        <Alert
          severity="success"
          sx={{mb: 2}}
          onClose={() => setClaimed(null)}
          data-testid="claim-success">
          <b>
            {claimed.serial_number} claimed into {networkId}.
          </b>{' '}
          Its parameters and KPIs appear after its next Inform.
        </Alert>
      )}
      {error ? (
        <Alert
          severity="error"
          sx={{mb: 2}}
          action={<RetryButton onClick={reload} />}>
          <b>Couldn't load claimed CPEs.</b> {errorMessage(error)}
        </Alert>
      ) : null}
      <CardTitleRow
        icon={InboxIcon}
        label={
          CLAIMS_API_AVAILABLE
            ? `Claims (${rows.length})`
            : `Claimed CPEs (${rows.length})`
        }
        filter={() => (
          <Tooltip
            title={
              CLAIMS_API_AVAILABLE ? '' : 'Coming soon: no claims API yet'
            }>
            <span>
              <Button
                variant="contained"
                startIcon={<AddIcon />}
                disabled={!CLAIMS_API_AVAILABLE}
                onClick={() => setOpen(true)}>
                New claim
              </Button>
            </span>
          </Tooltip>
        )}
      />
      <AcsPaper>
        {data && rows.length === 0 ? (
          <EmptyBlock title="No claimed CPEs">
            A claimed CPE appears here after its first Inform; CPEs behind the
            network's gateways need no claim and are listed under CPEs.
          </EmptyBlock>
        ) : (
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Serial number</TableCell>
                <TableCell>OUI</TableCell>
                <TableCell>Product class</TableCell>
                <TableCell>Label</TableCell>
                <TableCell>Claim key</TableCell>
                <TableCell>Created</TableCell>
                <TableCell>CPE</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {isLoading && !data ? (
                <SkeletonRows rows={3} cols={7} />
              ) : (
                rows.map(r => (
                  <TableRow key={r.key} data-testid="acs-claim-row">
                    <TableCell sx={{fontWeight: 500}}>
                      {r.serial || '—'}
                    </TableCell>
                    <TableCell sx={{fontFamily: 'monospace'}}>
                      {r.oui || '—'}
                    </TableCell>
                    <TableCell>{r.productClass || '—'}</TableCell>
                    <TableCell>{r.label || '—'}</TableCell>
                    <TableCell sx={{fontFamily: 'monospace'}}>
                      {r.key}
                    </TableCell>
                    <TableCell>{formatTime(r.created)}</TableCell>
                    <TableCell>
                      {r.cpe ? (
                        <Link
                          component={RouterLink}
                          to={cpePath(networkId, r.key)}>
                          <OnlineStatus online={r.cpe.online} />
                        </Link>
                      ) : (
                        'Not seen yet'
                      )}
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        )}
      </AcsPaper>
      <ClaimDialog
        networkId={networkId}
        open={open}
        onClose={() => setOpen(false)}
        onClaimed={c => {
          setOpen(false);
          setClaimed(c);
          reload();
        }}
      />
    </>
  );
}
