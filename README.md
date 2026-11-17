[![codecov](https://codecov.io/github/Noah-Wilderom/godb/graph/badge.svg?token=QAMIY0SJVO)](https://codecov.io/github/Noah-Wilderom/godb)

# GoDB

A Go database engine with B+ tree storage and Raft replication, under development.

Start three local members with Prometheus and a provisioned Grafana dashboard:

```sh
docker compose up -d --build --wait --wait-timeout 120
```

Open Grafana at http://localhost:3000 (`admin` / `godb-local-dev`) and Prometheus
at http://localhost:9090. See the [Compose guide](docs/compose.md) for persistent
storage, optional OpenTelemetry/Tempo tracing, configuration, and shutdown.

The server exposes peer RPCs and an opt-in [MySQL client endpoint](docs/mysql.md)
with TLS/password authentication, native prepared statements, and bounded session
setup, table/column/index discovery, six INFORMATION_SCHEMA views, and pool resets. Integration tests cover Go and PHP PDO clients. Broader MySQL
driver, tool, and ORM compatibility remains in development. A first [SQL execution layer](docs/sql.md) supports standalone and replicated server Go APIs
with CREATE TABLE (including unique/secondary indexes), single-row INSERT, primary-key UPDATE/DELETE, and quorum-confirmed SELECT with indexed lookups, primary/secondary-index ranges, or bounded table scans, optional AND-connected comparison/NULL predicates, ORDER BY, bounded LIMIT/OFFSET, and single COUNT aggregates. EXPLAIN SELECT describes the chosen plan without scanning user rows. See [server configuration](docs/server.md),
[observability](docs/observability.md), and [production readiness](docs/production-readiness.md).
