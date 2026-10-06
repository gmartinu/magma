# acsd: reaching claimed CPEs

How a task queued for a claimed CPE (one outside the Magma core, on the
`cwmp_wan` listener) gets to it. Core CPEs on mtr0 are not covered here:
acsd sets no Connection Request credential on them, so their tasks run at
their next Inform.

## The problem

A TR-069 ACS gets a CPE to open a session now by sending it a Connection
Request: an HTTP GET to the `ManagementServer.ConnectionRequestURL` the CPE
reports. A CPE on a carrier network reports an address inside that network
(a private or CGNAT `100.64.0.0/10` address), behind a carrier NAT that
acsd cannot get through. Its sessions reach acsd from the NAT's public
address. So for most claimed CPEs the only way in is the CPE's own next
periodic Inform. TR-111 (STUN) is not implemented.

## What acsd does

1. **Short periodic Inform at BOOTSTRAP.** Every claimed session whose
   Inform carries `0 BOOTSTRAP` queues an internal task that sets
   `ManagementServer.PeriodicInformEnable=true` and
   `PeriodicInformInterval` (`cwmp_reach.periodic_inform_interval` in
   acsd.yml, 300 s by default; the AcsD mconfig `periodic_inform_interval`
   wins when set; 0 leaves the CPE's own). It runs after the credential
   rotation, as its own SetParameterValues, so a CPE refusing the value
   cannot block the rotation. On success the values go into the CPE's
   snapshot, so its model, online state and `next_inform` follow them.
   It is set again at every BOOTSTRAP because a factory reset, the usual
   cause of one, restores the factory interval.

2. **Per-CPE Connection Request credential.** The credential rotation
   sets `ConnectionRequestUsername` (the per-CPE ACS username) and
   `ConnectionRequestPassword` (its own random password) in the same SPV
   as `Username`/`Password`, so the CPE applies both or neither, and both
   are promoted or dropped under one generation. The Connection Request
   password is stored in Redis in clear: acsd is the Digest client there
   and only learns the CPE's realm from its challenge. It only lets its
   holder ask the CPE to open a session to its configured ACS URL, which
   still needs the ACS credential (stored as HA1 only). A CPE already on
   its own credential but without a Connection Request one rotates again
   in its next session.

3. **Connection Request only when plausibly reachable.** With
   `cwmp_reach.connection_request: auto` (default) acsd sends one only when
   the URL host is the source address of the CPE's sessions (no NAT in
   between), or a public address, or a name that resolves to one. A
   private, CGNAT, link-local or loopback address seen from a different
   source is behind a NAT; it is also an address inside the AGW's own
   networks, so the rule keeps a CPE from making acsd call into them.
   `always` skips the address rule (CPEs reached over a VPN), `off` never
   sends one. EnqueueTask sends it from a worker thread, one at a time per
   CPE and not again within 30 s of an accepted one the CPE has not
   answered. The `ConnectionRequest` RPC sends one on demand. A request
   times out after `connection_request_timeout_secs` (5 s); one that fails
   is not retried until the CPE Informs again.

## What Orc8r and the NMS see

`Cpe` (CpeManager gRPC) and the `cpe_acs` state carry:

| field | meaning |
|-------|---------|
| `reach` | `CONNECTION_REQUEST`: tasks run within seconds. `NEXT_INFORM`: they wait for the next periodic Inform |
| `reach_reason` | why `NEXT_INFORM`: `behind_nat`, `no_url`, `no_credential` (core CPEs, claimed ones not rotated yet), `disabled`, `connection_request_failed` (until the next Inform), `frozen` |
| `next_inform` | Unix time the next periodic Inform is due (last Inform + the CPE's interval); in the past when the CPE is late |

Pending `CpeTask`s carry their CPE's `reach` and `next_inform`, so a UI
can say "runs at the next check-in (~N s)" with `N = next_inform - now`.
`ConnectionRequestResponse` says whether the CPE accepted (`sent`), and
otherwise the reach, reason, `next_inform` and the HTTP status or error.

## Operations

- `acsd_cli.py rotate-credentials <claim>`: new ACS and Connection Request
  credentials at the CPE's next session, e.g. when its Connection Requests
  get HTTP 401. The CPE stays managed.
- `acsd_cli.py reset-credentials <claim>`: forget every credential so the
  CPE may bootstrap again (after a factory reset). Until it does, it has
  no Connection Request credential.
- `acsd_cli.py claim-list`: the `CR` column says whether acsd holds a
  Connection Request credential for the CPE.

## Known limits

- No STUN (TR-111) and no XMPP (TR-069 Annex K): a CPE behind a carrier
  NAT is only reached at its next periodic Inform.
- A CPE that answers the rotation SPV with status 1 and comes back on the
  bootstrap credential is refused until `reset-credentials` (from 2C.2).
- None of this has been run against a real CPE yet; the tests use
  synthetic Informs and a local HTTP server standing in for the CPE.
