# acsd dev environment (Docker Desktop, arm64 or amd64)

The smallest set of real services `acsd` needs: **redis + mobilityd + acsd**,
plus a one-shot simulated CPE for the smoke test. It shows both connection
modes (core CPEs named by the IMSI behind their UE IP, claimed CPEs on the
HTTPS WAN listener) and the CpeManager gRPC API. No pipelined/OVS, no
sctpd/MME, no Orc8r, no magmad, no eventd.

All of them run the prebuilt AGW python image with this checkout bind-mounted
over `/magma`, so a code change only needs `docker compose restart acsd`. You
never build an image.

| Piece | What it does |
|-------|--------------|
| image | `linuxfoundation.jfrog.io/magma-docker/agw_gateway_python_arm:1.8.0`, native arm64, about 400 MB to pull and 1.8 GB on disk. On amd64, set `AGW_PYTHON_IMAGE=linuxfoundation.jfrog.io/magma-docker/agw_gateway_python:1.9.0`. |
| `protogen` | One-shot. Regenerates the Python protos from this checkout into `.gen/`, because the image predates the `AcsD` mconfig and `lte/protos/cpe_acs.proto` (the CpeManager stubs). Takes a few seconds. |
| `wancert` | One-shot. `acsd_cli.py dev-cert --cn 127.0.0.1` writes a self-signed cert for the claimed listener into the `acsd-certs` volume. Skipped once it exists. **Dev only.** |
| `pydeps` | One-shot. Pip-installs `requirements.txt` (deps the image lacks, today only `sdnotify`) into `.deps/`. Skipped once that is done. |
| `redis` | Port 6380. It owns the network namespace that the others join, so the stock `service_registry.yml` (everything on 127.0.0.1) works unchanged. |
| `mobilityd` | IP_POOL `192.168.128.0/24` (from `configs/gateway.mconfig`), gRPC on 127.0.0.1:60051. |
| `acsd` | Service303 and CpeManager gRPC on 127.0.0.1:50086. Core CWMP listener on 0.0.0.0:48081 (`configs/acsd.yml` overrides the mtr0 bind, with no code change). Claimed CWMP listener (`cwmp_wan`) on HTTPS 0.0.0.0:48443. Published to the host as `127.0.0.1:48081` and `127.0.0.1:48443`. |
| `cpe-sim` | Profile `test`. Runs `smoke_test.py` inside the shared namespace with NET_ADMIN. |

The CPU and memory caps (0.5–1 CPU, 256–512 MB each) are set in the compose
file. At idle the stack uses about 100 MB in total.

## Use

```bash
cd lte/gateway/docker/acs-dev
docker compose up -d --wait            # protogen, pydeps, redis, mobilityd, acsd
docker compose run --rm cpe-sim        # smoke test, expects "23/23 passed"
docker compose logs -f acsd
docker compose restart acsd            # after editing acsd code
docker compose run --rm protogen && docker compose restart   # after editing a .proto
docker compose down                    # stop (add -v to drop the tmp and cert volumes)
```

## Smoke test

`smoke_test.py` checks the following:

1. acsd answers Service303 `GetServiceInfo` with ALIVE and APP_HEALTHY.
2. mobilityd `AllocateIPAddress` returns an IP for a test IMSI (`SMOKE_IMSI`, default `001010000000001`).
3. acsd's own `MobilitydClient.get_imsi_for_ip` maps that IP back to the IMSI.
4. An Inform (`acsd/tests/fixtures/sim4000_inform.xml`) sent **from that IP** first gets a 401 Digest challenge, then, authenticated with the dev credential, HTTP 200 with an InformResponse. The CPE source IP is real: the script adds the UE IP to `lo` and binds the client socket to it.
5. The empty POST that follows ends the session with 204.
6. acsd stored the normalized model for the IMSI, with the source IP as its WAN address.
7. A Reboot task queued in Redis is sent in the next session, and the RebootResponse completes it.
8. An authenticated Inform from an unallocated IP gets HTTP 403.

9. Claimed mode: `acsd_cli.py claim-add` claims the fixture's device (`00A1B2/SIM4000/SIM0001`, claim `sim-smoke`). An Inform over HTTPS (the dev cert pinned) with the bootstrap credential gets 401 then 200; acsd's first request is the SetParameterValues that rotates the CPE to its own `ManagementServer.Username/Password`, and the answer ends the session with 204.
10. The claimed model is stored, and its WAN address is not the source IP (behind NAT that is not the CPE's).
11. The bootstrap credential is now refused (403) and the per-CPE one accepted (200).
12. gRPC `CpeManager.ListCpes` lists the core CPE (`IMSI...`, `CPE_MODE_CORE`) and the claimed one (`CLAIMsim-smoke`, `CPE_MODE_CLAIMED`). `EnqueueTask` queues a Reboot on each, the next session of each runs it, and `GetTask` reports `CPE_TASK_STATUS_DONE`.
13. Service303 `GetMetrics` has `acs_informs_total` and `acs_online_cpes`; `GetOperationalStates` has a `cpe_acs` row for both CPEs.

Finally it releases the IP. Each run removes and re-adds the claim, so the
claimed CPE bootstraps again and the test can run any number of times.

eventd is not in the stack, so acsd's `cpe_session_completed` and
`cpe_task_failed` events go nowhere here; the unit tests cover them.

`configs/acsd.yml` sets a **dev-only** Digest credential (`acs-dev-cpe` /
`acs-dev-only-not-a-secret`) and a **dev-only** claimed-mode bootstrap
credential (`acs-dev-bootstrap` / `acs-dev-bootstrap-not-a-secret`); the
shipped acsd.yml has neither, so a real AGW refuses every CPE until one is
configured. To queue a task by hand:

```bash
docker compose exec acsd python3 -c "from magma.acsd import tasks; \
from magma.acsd.store import AcsStore; \
from magma.common.redis.client import get_default_client; \
print(tasks.enqueue_task(AcsStore(get_default_client()), 'IMSI001010000000001', 'reboot').task_id)"
```

An Inform sent from the macOS host to `127.0.0.1:48081` arrives from the
Docker bridge gateway, which has no IMSI, so it always gets 403. That is
expected: run the identity-bearing tests through `cpe-sim`. The claimed
listener needs no IMSI, so a CPE or client on the host can use
`https://127.0.0.1:48443/` once its device is claimed.

To manage claims and read the CPEs by hand:

```bash
docker compose exec acsd python3 /magma/lte/gateway/python/scripts/acsd_cli.py claim-list
docker compose exec acsd python3 -c "import grpc; \
from lte.protos import cpe_acs_pb2 as pb; \
from lte.protos.cpe_acs_pb2_grpc import CpeManagerStub; \
print(CpeManagerStub(grpc.insecure_channel('127.0.0.1:50086')).ListCpes(pb.ListCpesRequest()))"
```

## End to end with a local Orc8r

`orc8r/` runs a minimal Orc8r (postgres, the controller with every module,
the nginx proxy) and `docker-compose.orc8r.yaml` adds magmad and
control_proxy to this env, so the path NMS -> Orc8r REST -> SyncRPC ->
magmad -> acsd and acsd's `cpe_acs` state -> Orc8r both run on the Mac.

Build the images once, natively (the controller Dockerfile picks arm64 Go
and protoc by itself; about 6 minutes on 4 CPUs):

```bash
cd orc8r/cloud/docker
python3 -c "import build; build.HOST_BUILD_CTX='/tmp/magma_orc8r_build'; \
  build._create_build_context(build._get_modules(build.MODULES))"
docker buildx build --platform linux/arm64 --build-arg PLATFORM=linux/arm64 \
  -f controller/Dockerfile -t orc8r_controller:acs-e2e-arm64 --load /tmp/magma_orc8r_build
docker buildx build -f nginx/Dockerfile -t orc8r_nginx:acs-e2e-arm64 --load /tmp/magma_orc8r_build
```

Then, with this env already up (it owns the `acs-dev_acsnet` network):

```bash
(cd orc8r && docker compose up -d && ./setup.sh)   # network acs_e2e, gateway agw1, tenant 1
docker compose -f docker-compose.yaml -f docker-compose.orc8r.yaml up -d magmad control_proxy
docker compose run --rm cpe-sim                    # CPEs report to Orc8r
```

The REST API is `https://localhost:9443/magma/v1`, with the admin cert
`orc8r/.certs/admin_operator.pem` + `.key.pem` (or `admin_operator.pfx`,
password `magma`, in a browser):

```bash
C="--cert orc8r/.certs/admin_operator.pem --key orc8r/.certs/admin_operator.key.pem -k"
curl $C https://localhost:9443/magma/v1/acs/acs_e2e/cpes
curl $C -X POST -H 'Content-Type: application/json' -d '{"type":"reboot"}' \
  https://localhost:9443/magma/v1/acs/acs_e2e/cpes/IMSI001010000000001/tasks
docker compose run --rm --entrypoint python3 cpe-sim \
  /magma/lte/gateway/docker/acs-dev/orc8r/cpe_session.py   # the CPE's next session runs it
```

NMS (optional): `(cd orc8r && docker compose --profile nms up -d nms-db)`,
then in `nms/` with `API_HOST=localhost:9443`, `API_CERT_FILENAME` /
`API_PRIVATE_KEY_FILENAME` = the admin cert above,
`NODE_TLS_REJECT_UNAUTHORIZED=0`, and `MYSQL_DIALECT=postgres MYSQL_HOST=127.0.0.1
MYSQL_PORT=55432 MYSQL_USER=nms MYSQL_PASS=nms MYSQL_DB=nms PORT=8081`: `yarn
migrate`, `yarn setAdminPassword host admin@magma.test password1234`, `yarn
createOrganization acs-e2e acs_e2e`, `yarn setAdminPassword acs-e2e
admin@magma.test password1234`, then `yarn start:dev`. The NMS mirrors each
organization to an Orc8r tenant with the organization's ID, so entitle that
tenant (`PUT /tenants/{id}/entitlements/acs`), and turn on the `acs` feature
flag for the organization (host portal, Features).

Entitlements are not enforced: `orc8r/overrides/orc8r/entitlements.yml`.
Set `enforce: true`, then `docker compose exec controller supervisorctl
restart entitlements` (in `orc8r/`).

magmad runs with `init_system: docker` but no Docker socket, so it cannot
restart services: the mconfig it streams lands in its own config volume and
acsd keeps the static dev mconfig.

Stop: `docker compose -f docker-compose.yaml -f docker-compose.orc8r.yaml
stop magmad control_proxy` and `(cd orc8r && docker compose down)`
(`down -v` also drops the Orc8r DB; `rm -rf orc8r/.certs orc8r/.agw` resets
the certs and the gateway identity, after which setup.sh registers a new
one).

## Not possible on macOS Docker Desktop

The LinuxKit VM kernel ships no `openvswitch` module (`modprobe openvswitch`
fails, and `ip link add ... type openvswitch` returns "Operation not
supported"). So pipelined, the `mtr0` port and the UE-to-mtr0 data path cannot
run here. To test them, use a Linux host: the upstream AGW Vagrant VM, or an
Ubuntu 20.04 VM/EC2 instance running the full `lte/gateway/docker` compose.
