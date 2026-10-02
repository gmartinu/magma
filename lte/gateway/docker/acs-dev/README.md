# acsd dev environment (Docker Desktop, arm64 or amd64)

The smallest set of real services `acsd` needs: **redis + mobilityd + acsd**,
plus a one-shot simulated CPE for the smoke test. No pipelined/OVS, no
sctpd/MME, no Orc8r, no magmad.

All of them run the prebuilt AGW python image with this checkout bind-mounted
over `/magma`, so a code change only needs `docker compose restart acsd`. You
never build an image.

| Piece | What it does |
|-------|--------------|
| image | `linuxfoundation.jfrog.io/magma-docker/agw_gateway_python_arm:1.8.0`, native arm64, about 400 MB to pull and 1.8 GB on disk. On amd64, set `AGW_PYTHON_IMAGE=linuxfoundation.jfrog.io/magma-docker/agw_gateway_python:1.9.0`. |
| `protogen` | One-shot. Regenerates the Python protos from this checkout into `.gen/`, because the image predates the `AcsD` mconfig. Takes a few seconds. |
| `pydeps` | One-shot. Pip-installs `requirements.txt` (deps the image lacks, today only `sdnotify`) into `.deps/`. Skipped once that is done. |
| `redis` | Port 6380. It owns the network namespace that the others join, so the stock `service_registry.yml` (everything on 127.0.0.1) works unchanged. |
| `mobilityd` | IP_POOL `192.168.128.0/24` (from `configs/gateway.mconfig`), gRPC on 127.0.0.1:60051. |
| `acsd` | Service303 on 127.0.0.1:50086. CWMP on 0.0.0.0:48081 (`configs/acsd.yml` overrides the mtr0 bind, with no code change). Published to the host as `127.0.0.1:48081`. |
| `cpe-sim` | Profile `test`. Runs `smoke_test.py` inside the shared namespace with NET_ADMIN. |

The CPU and memory caps (0.5–1 CPU, 256–512 MB each) are set in the compose
file. At idle the stack uses about 100 MB in total.

## Use

```bash
cd lte/gateway/docker/acs-dev
docker compose up -d --wait            # protogen, pydeps, redis, mobilityd, acsd
docker compose run --rm cpe-sim        # smoke test, expects "10/10 passed"
docker compose logs -f acsd
docker compose restart acsd            # after editing acsd code
docker compose run --rm protogen && docker compose restart   # after editing a .proto
docker compose down                    # stop (add -v to drop the tmp volume)
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

Finally it releases the IP.

`configs/acsd.yml` sets a **dev-only** Digest credential (`acs-dev-cpe` /
`acs-dev-only-not-a-secret`); the shipped acsd.yml has none, so a real AGW
refuses every CPE until one is configured. To queue a task by hand:

```bash
docker compose exec acsd python3 -c "from magma.acsd import tasks; \
from magma.acsd.store import AcsStore; \
from magma.common.redis.client import get_default_client; \
print(tasks.enqueue_task(AcsStore(get_default_client()), 'IMSI001010000000001', 'reboot').task_id)"
```

An Inform sent from the macOS host to `127.0.0.1:48081` arrives from the
Docker bridge gateway, which has no IMSI, so it always gets 403. That is
expected: run the identity-bearing tests through `cpe-sim`.

## Not possible on macOS Docker Desktop

The LinuxKit VM kernel ships no `openvswitch` module (`modprobe openvswitch`
fails, and `ip link add ... type openvswitch` returns "Operation not
supported"). So pipelined, the `mtr0` port and the UE-to-mtr0 data path cannot
run here. To test them, use a Linux host: the upstream AGW Vagrant VM, or an
Ubuntu 20.04 VM/EC2 instance running the full `lte/gateway/docker` compose.
