# Local database and observability stack

Run from the repository root with Docker Engine and Docker Compose v2 or newer:

```sh
docker compose up -d --build --wait --wait-timeout 120
```

Or use `task dev:up`. The build uses Go 1.27.0 in Docker; you do not need Go,
OpenSSL, or prebuilt release artifacts on the host. The separate development
Dockerfile leaves the GoReleaser scratch image unchanged. Override `GODB_VERSION`
to set the version embedded with the Go linker, for example
`GODB_VERSION=local-test task dev:up`.

| Service | Address | Purpose |
| --- | --- | --- |
| Grafana | http://localhost:3000 | Preconfigured GoDB overview dashboard |
| Prometheus | http://localhost:9090 | Metrics queries, target status, and alert rules |
| node1, node2, node3 | Docker network only | Three persistent, authenticated Raft members |

Grafana's local development login is `admin` / `godb-local-dev`. Set
`GRAFANA_ADMIN_PASSWORD` before the first start to choose another password. Grafana
stores it in its volume; changing this variable later does not reset an existing
account. `GRAFANA_PORT` and `PROMETHEUS_PORT` override the host ports. Published
ports bind to loopback. Peer ports and unauthenticated metrics ports are not
published to the host.

The stack uses subnet `10.89.71.0/24` because this development host has exhausted
Docker's automatic network pools. Override `GODB_SUBNET` with another unused CIDR
if it conflicts with your LAN, VPN, or another Compose project. No existing Docker
networks or daemon settings need to be changed.

The dashboard and Prometheus data source are provisioned automatically. Open
**Dashboards → GoDB → GoDB overview**, or use the home dashboard. After two scrapes
(about ten seconds), rates begin to appear. Three targets should be up and one
member should report itself as leader. Write/proposal latency panels may have no
data: default Compose leaves the optional MySQL listener disabled and does not
generate synthetic writes. See the [MySQL endpoint guide](mysql.md) for enabling
client access with credentials and a certificate matching the client hostname.
Raft heartbeats, runtime metrics, and persistence from elections are real traffic.
Dashboard descriptions distinguish raw engine operations from user commands.
Connection panels include peer transports, metrics HTTP connections, and MySQL
TCP connections when enabled. The MySQL session panel counts authenticated clients
separately from handshakes. Additional panels cover RPC activity/bandwidth, writer
pressure, explicit transactions, page I/O, replication apply lag, and process
resources. See the [metric definitions](observability.md) for their scope.
Prometheus uses [DNS discovery](https://prometheus.io/docs/prometheus/latest/configuration/configuration/#dns_sd_config)
to refresh container addresses every five seconds while retaining stable member
labels across recreations.

## Initialization and persistence

A one-shot service generates a development CA and member certificates. Each node
receives only its own certificate/key and the public CA, in a read-only volume.
The CA private key stays in a separate volume mounted only by the initializer.
Certificates use the `node1`, `node2`, and `node3` Docker DNS names and the expected
group/node URI identities. Leaf certificates expire after one year. Existing
certificates are reused and checked; an incomplete or expiring setup fails visibly
instead of silently rotating identities.

Each member runs as UID/GID 10001, creates its database only in an empty data
directory, then starts the server. Each has its own database/WAL/Raft journal
volume. Restarting recovers these files; it does not recreate or clone them.
The group UUID and three-member topology are fixed for this development stack.
Do not use `--scale` to add members or mix these files with another cluster.

Prometheus retains up to seven days/approximately 1 GB of time-series blocks.
Its WAL/head and filesystem overhead are additional. Grafana retains its settings
in a separate volume. The existing six alert rules plus a Compose-specific rule
for missing discovered members are loaded into Prometheus; they
are visible in its UI, but no Alertmanager notification destination is configured.

Member health checks prove the metrics endpoint responds, not quorum or write
readiness. Grafana and Prometheus can start independently so monitoring remains
accessible when a member is unavailable. Dependencies wait for successful
certificate initialization using Compose's
[startup conditions](https://docs.docker.com/compose/how-tos/startup-order/).

## Optional OpenTelemetry traces

```sh
docker compose -f compose.yaml -f compose.tracing.yaml up -d --build --wait --wait-timeout 120
```

Or use `task dev:up:tracing`. This adds Tempo with persistent local storage,
provisions its Grafana data source, and enables OTLP/HTTP export from each member.
Trace block retention is 24 hours; this is a time policy, not a hard disk quota.
In Grafana, open **Explore**, select **Tempo**, and search for
`{ resource.service.name = "godb" }`. Peer RPC traces appear without a command
workload. Allow about a minute for the first searchable traces and use a recent
time range.

Root sampling defaults to 10%; `GODB_TRACE_SAMPLE_RATIO=1 task dev:up:tracing`
samples all new roots for local debugging. The exporter sends directly to Tempo;
a separate OpenTelemetry Collector isn't required for this setup. Tempo's OTLP
and query ports remain inside Docker. This is a local single-binary backend,
not a production trace storage deployment. It follows the upstream
[Tempo single-binary example](https://github.com/grafana/tempo/tree/v3.1.1/example/docker-compose/single-binary).
These are individual proposal/peer traces, not yet one trace spanning every Raft
batch caused by a proposal; see [observability](observability.md).

To return to metrics only, run `task dev:down`, then `task dev:up`. Stopping with
both Compose files also stops the optional Tempo services and keeps their volume.
Grafana may retain its provisioned Tempo data source after disabling tracing;
it is unavailable until Tempo is started again.

## Everyday commands

```sh
task dev:validate
docker compose ps
task dev:logs -- node1 node2 node3
docker compose restart node1
task dev:down
```

`task dev:validate` checks Compose, Prometheus configuration, and alert rule syntax
using the pinned Prometheus image. JSON dashboard and data source files live in
`deploy/grafana`; edit them in Git rather than in Grafana's UI. Provisioning uses
Grafana's [file provisioning](https://grafana.com/docs/grafana/latest/administration/provisioning/).

`dev:down` preserves databases, certificates, and monitoring history. Docker's
`down --volumes` option deletes those volumes: use it only when intentionally
discarding the entire local cluster, never as a recovery procedure. Backups and
coordinated Raft restore still need the work described in
[production readiness](production-readiness.md).

This stack is for local development. It does not configure off-host backups,
production credentials/PKI, durable object storage for traces, alert delivery,
or host disk monitoring. It makes the current engine and replication behavior
observable while those capabilities are developed.
