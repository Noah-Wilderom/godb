# Built-in metrics and optional tracing

For a ready-to-run local cluster with Prometheus, Grafana, and optional Tempo,
see the [Docker Compose guide](compose.md).

Metrics are collected by the database, WAL, Raft store, and server without a separate
exporter process. No metric registers with Prometheus's global registry. Each database
or Raft store exposes a `Metrics() prometheus.Collector`; each server automatically
registers its collectors with a private registry and exposes `MetricsHandler()`.
Embedding several engines in one process therefore does not cause duplicate global
registrations. Register multiple standalone engines under distinct constant labels
or separate registries; their metric names intentionally match.

Instrumentation is active even when no HTTP listener is enabled. Existing Stats
counters retain their behavior. Histogram handles are allocated once per engine,
and observations do no network I/O. Scraping engine collectors reads atomic counters,
histograms, and cached state rather than acquiring the writer or Raft storage lock.
Go/process collectors also read runtime and operating-system statistics.

## Server options

Append these options to the [server command](server.md):

```sh
--metrics-listen 127.0.0.1:9091
--otlp-http-endpoint http://127.0.0.1:4318/v1/traces
--trace-sample-ratio 0.1
```

All options are optional. No metrics socket opens without metrics-listen, and no
trace exporter starts without an OTLP endpoint. Each member on the same machine
needs a distinct metrics port. The HTTP endpoint is `GET /metrics` and supports
Prometheus/OpenMetrics negotiation. It has request timeouts and a two-scrape
concurrency limit. Server shutdown closes the listener. A requested bind failure
fails startup; an unexpected HTTP listener failure during operation is logged and
counted without stopping database writes.

The metrics listener is separate from the authenticated Raft listener and has no
built-in authentication or TLS. Use loopback with a local agent, or a protected
monitoring network/TLS proxy. Do not expose it as the public client endpoint. It
exposes operational identities and resource usage, although not database payloads.
Use `Server.MetricsAddress()` to discover a port assigned with `:0`.

## Metric families and semantics

Server-registered engine metrics carry constant `group` and `node` labels. The
scrape target supplies `job` and `instance`. Outcome labels have a fixed vocabulary;
keys, row values, SQL, request IDs, file paths, trace IDs, and error strings are never
metric labels. Process/Go metrics describe the whole process and have no group/node
labels. Do not sum duplicate process metrics from multiple embedded members as if
they were different processes.

| Families | What they measure |
| --- | --- |
| `godb_database_*_total` | Existing Stats read/write/request/error/batch/split/page-I/O counters |
| `godb_database_get_duration_seconds` | Raw Database.Get, including lock wait; not every typed lookup or scan |
| `godb_database_write_request_duration_seconds` | submitWrite through completion/rejection, including queue wait |
| `godb_database_sync_duration_seconds` | Database file sync attempts, including failures; excludes WAL |
| `godb_wal_sync_duration_seconds`, `godb_wal_sync_errors_total` | WAL commit, clearing, and recovery sync attempts/errors |
| `godb_database_file_bytes` | Size from recovered metadata or the last successful database sync |
| `godb_database_write_queue_entries` | Buffered engine write requests |
| `godb_database_write_queue_capacity`, `godb_database_writes_in_flight` | Queue capacity and outstanding submitWrite calls, including building, waiting, and execution |
| `godb_database_write_queue_wait_duration_seconds` | Enqueue attempt to batch dispatch; includes channel backpressure, excludes database lock wait/execution |
| `godb_database_transactions_active`, `godb_database_transactions_total{outcome}` | Explicit WriteTx handles and terminal outcomes: committed, rolled_back, conflict, error |
| `godb_database_transaction_duration_seconds` | Explicit transaction lifetime, including application think time and Commit execution |
| `godb_database_page_read_bytes_total`, `godb_database_page_write_bytes_total` | Successful page operations times page size; not device I/O or a buffer-cache hit ratio |
| `godb_raft_store_save_duration_seconds` | All Save calls, including empty/invalid updates and lock wait |
| `godb_raft_store_sync_duration_seconds` | Sync attempts inside nonempty Save; excludes initial creation/recovery sync |
| `godb_raft_store_saves_total`, `godb_raft_store_errors_total` | Successful nonempty saves; any Save error, including invalid updates |
| `godb_raft_store_bytes`, `godb_raft_store_last_index` | Last recovered or successfully saved journal state |
| `godb_proposal_duration_seconds{outcome}` | Caller latency through applied result, timeout, rejection, or shutdown |
| `godb_applied_commands_total{result}` | Applied command outcomes after server startup, including duplicate requests |
| `godb_raft_term`, `godb_raft_commit_index`, `godb_raft_applied_index` | Cached state after event-loop progress |
| `godb_raft_is_leader`, `godb_raft_has_leader` | Last locally observed leadership state |
| `godb_raft_leader_changes_total` | Changes in the observed leader ID, including transitions through unknown |
| `godb_event_loop_last_progress_timestamp_seconds` | Last completed event-loop iteration, in wall-clock seconds |
| `godb_server_running`, `godb_server_failures_total` | Local loop running state and fatal exits |
| `godb_proposals_pending`, `godb_proposal_queue_entries` | Waiting local callers and buffered submissions |
| `godb_peer_receive_queue_entries`, `godb_peer_send_queue_bytes` | Inbound queue and aggregate outstanding serialized peer bytes |
| `godb_peer_receive_duration_seconds`, `godb_peer_receive_errors_total` | Peer RPC handler duration and returned errors |
| `godb_peer_send_duration_seconds` | Outgoing RPC latency including connection wait/errors |
| `godb_peer_sent_total`, `godb_peer_failed_total`, `godb_peer_dropped_total` | Existing transport counters |
| `godb_connections{kind}`, `godb_connections_peak{kind}` | Current and per-kind peak established connections since member startup |
| `godb_connections_opened_total{kind}`, `godb_connections_closed_total{kind}` | Connection lifecycle counters, including reconnects |
| `godb_peer_rpc_in_flight{kind}`, `godb_peer_rpc_started_total{kind}`, `godb_peer_rpc_errors_total{kind}` | Active logical RPC calls, started calls, and calls returning non-OK status |
| `godb_peer_rpc_received_bytes_total{kind}`, `godb_peer_rpc_sent_bytes_total{kind}` | Payload bytes including gRPC framing/compression; excludes headers and TCP/TLS overhead |
| `godb_metrics_endpoint_errors_total` | Unexpected HTTP metrics server exits |
| `go_*`, `process_*` | Standard Go runtime and process collectors |

The [SQL engine](sql.md) exposes statement latency/counts, in-flight statements,
and returned/affected/matched/deleted rows. The server automatically registers these plus SQL
preparation latency, read barrier latency, outstanding barriers, and the read queue.
The dashboard has a collapsed SQL section for all of these. Idle members export
zero SQL gauges/counters; histogram series appear after calls through the Go API.
The opt-in MySQL endpoint uses these SQL execution metrics and adds client
connection/session, authentication-failure, admission-rejection, and protocol-error
metrics, plus open prepared handles, retained SQL/parameter bytes, and statement
prepare/execute/error counters; see [the endpoint guide](mysql.md). SQL preparation is measured separately from
execution; successful deduplicated write retries count as responses, not additional
physical mutations. Read latency includes local work after quorum/application wait.

Durations use seconds and classic histogram buckets; `_count`, `_sum`, and `_bucket`
are exported. Counts and sums reset when handles/processes are recreated. Byte/index
gauges recover from disk. A failed save can leave additional physical bytes that
aren't represented by the last-success gauge until recovery. DB writes include
internal replication metadata. Startup replay doesn't emit applied-command counters.
Outcome series appear after their first observation.

The inherited read counters reflect existing API/internal accounting; they are not
a normalized count of all logical queries. Use the proposal histogram's counts to
measure local write-call outcomes. A proposal timeout is an ambiguous result, not
evidence that no write committed. The `result` label describes deterministic command
outcomes; the `outcome` label additionally includes canceled, timeout, not_leader,
busy, pending, invalid, closed, and error.

Scrapes are not transactional snapshots across all families. Cached roles and indexes
can become stale when the event loop stalls: check the progress timestamp alongside
them. Neither leadership gauges nor server_running prove quorum, readiness, or
linearizable reads. A fatal server may disappear before its error counter is scraped;
also alert on Prometheus `up` and inspect process logs.

## Connections, transactions, and the database monitoring baseline

Connections have three bounded `kind` labels: `peer_inbound`, `peer_outbound`,
and `metrics_http`. Peer values count established gRPC transports after TLS,
including idle transports, rather than ClientConn handles or RPC streams. Pending
TCP/TLS handshakes and rejected handshakes are not counted. Metrics HTTP connections
include scrape connections and Compose health checks. External servers using only
`MetricsHandler()` must instrument their own listener; GoDB can only count its own
HTTP listener. The opt-in MySQL listener exposes separate `godb_mysql_connections` and
`godb_mysql_sessions` gauges, included in the updated total-connections dashboard.

For a member's total observed established connections use:

```promql
sum by (group, node) (godb_connections{job="godb"})
```

This is not the host's total socket count. Summing across a cluster counts each
peer link at both ends. Peak is calculated per kind; adding peaks does not give
a simultaneous total peak. Counters reset on restart. RPC concurrency is separate
because HTTP/2 multiplexes calls. RPC errors include cancellation and connection
wait failures, not just failures returned by a healthy peer. Outbound counters wrap
the whole call, including transparent retries; inbound counters cover decoded
calls reaching the handler, excluding unknown methods and decoding failures.
Active-call accounting uses unary interceptors so retry/shutdown event ordering
cannot leave an unmatched stats event counted as an active call. No address, connection
ID, request ID, or error text is a metric label. Connection accounting composes
with the OTel stats handler and preserves trace context.

Transaction metrics cover only the explicit `BeginWrite` API. They count a handle
until its first Commit completes or Rollback succeeds; repeated terminal calls do
not count again. Successful empty commits count, conflicts are distinct from other
commit errors, and a disk error is not proof that no bytes were written. Abandoned
handles remain active in the gauge until the application closes them, even if it
closes the database. Ordinary Put batches and replicated commands do not pretend
to be explicit transactions: use their existing write/applied-command metrics.

The dashboard now covers the monitoring categories represented by MySQL's
[connection/activity status variables](https://dev.mysql.com/doc/refman/8.4/en/server-status-variables.html)
and PostgreSQL's [database activity and I/O statistics](https://www.postgresql.org/docs/current/monitoring-stats.html):
connections/churn, active work, latency, errors, transactions, I/O, storage size,
replication, uptime, memory, CPU, and file descriptors. The metrics are scoped to
GoDB's implemented behavior; they are not numerically interchangeable with those
databases' statistics. Explicit transaction panels stay at zero without WriteTx
workloads. Page reads may be served by the OS cache; no buffer pool is implemented.
Local commit-minus-apply lag is in entries, not seconds or a follower's distance
from the leader, and is cached after event-loop progress.

Keep instrumentation, metric definitions, lifecycle/failure tests, and dashboard
panels together when adding engine or server features. In particular, add real
client sessions/authentication failures and query latency when the client API
lands; lock wait/deadlock metrics when their mechanisms exist; buffer-pool hit/miss
metrics with a buffer pool; and snapshot/backup progress, failures, and age with
those lifecycle operations. Avoid scanning tables during a scrape or introducing
per-key/query/connection labels. Add alerts against meaningful limits, not an
invented max-connections value when the server has no such global limit.

## Tracing

The server accepts an explicit `TracerProvider`; otherwise it uses a no-op provider.
No code replaces OpenTelemetry's global provider, propagator, or meter provider.
The injected provider remains owned by the caller. The CLI optionally creates an
OTLP/HTTP exporter and a bounded batch processor, then allows up to three seconds
to flush on shutdown. Queue saturation can drop traces rather than block writes.
Export failures are diagnostic and do not become database command failures.

The default root sampling ratio when export is enabled is 10%. Parent-based sampling
honors upstream decisions. Ratio zero disables new root sampling, not a previously
sampled parent. Use HTTPS for remote collectors; explicit HTTP is supported for a
local agent. An endpoint with no path uses `/v1/traces`. Standard exporter TLS/header
environment configuration remains available; secrets belong there rather than in
endpoint userinfo, which is rejected. In a scratch container, mount the required
collector CA and configure the exporter accordingly.

Resource attributes are `service.name=godb` and a configured service.instance.id
(group/node in the CLI). Local spans cover `godb.propose` and its event-loop processing.
gRPC stats handlers create peer client/server spans and propagate W3C trace context;
the inbound event-loop span shares that RPC trace. Raw errors and database contents
are not added to custom spans; outcomes are bounded codes. Automatic RPC spans may
include peer endpoint and transport error details.

Trace context is never written into a command, Raft entry, or database page. Peer
sender workers create independent RPC traces: this is not yet a causal distributed
trace linking a proposal to every replicated batch. Fine-grained spans inside the
pager/WAL, replay spans, exemplars, OTLP metrics, and OTel logs are not implemented.
Prometheus remains the metrics path, avoiding duplicate metric SDK pipelines.

The instrumentation follows the scope and cardinality guidance from
[Prometheus](https://prometheus.io/docs/practices/instrumentation/), and the provider
and exporter model from [OpenTelemetry Go](https://opentelemetry.io/docs/languages/go/instrumentation/).

## Scraping and starter alerts

`deploy/prometheus/prometheus.yml` scrapes local members on ports 9091–9093 and loads
`alerts.yml`. Adjust addresses when Prometheus runs in a container: its loopback is
not the database host. Starter alerts cover scrape failure, no observed leader,
stalled progress, persistence errors, sync latency, and peer drops. Tune thresholds
to measured hardware and tick intervals; they are starting points, not service SLOs.
Leader observation alerts do not establish cluster consensus, and missing targets
need the separate scrape-failure alert. WAL and journal sync quantiles can be queried
using the same histogram pattern as the database-sync rule.

Before deployment validate with `promtool check config prometheus.yml` and
`promtool check rules alerts.yml`. Operating-system disk capacity, disk latency,
network saturation, and filesystem health still need host/container monitoring.

MySQL local session SQL uses `godb_mysql_session_commands_total` and
`godb_mysql_session_errors_total`; successful protocol resets use
`godb_mysql_connection_resets_total`. These counters have no per-query/session labels.
The dashboard's session activity panel shows execution, error, and reset rates.
Local metadata queries and settings do not enter the replicated SQL latency or
quorum-read histograms; native session PREPARE/EXECUTE still count in wire statement
metrics. See [supported session semantics](mysql.md#connection-setup-and-session-queries).

Catalog discovery has `godb_mysql_discovery_commands_total` and
`godb_mysql_discovery_errors_total`, excluding PREPARE descriptions; errors count
before result encoding. `godb_mysql_catalog_duration_seconds{outcome}` measures
catalog reads during both prepare and execution, including quorum wait. They also
appear in the existing read-barrier metrics, but do not enter table SQL execution
histograms or affected-row counts. The `godb.mysql.catalog` span has no SQL, table,
or column attributes. The dashboard includes discovery activity and catalog p95
latency; SHOW DATABASES is local and does not add catalog-read samples.

INFORMATION_SCHEMA executions have separate
`godb_mysql_information_schema_commands_total` and
`godb_mysql_information_schema_errors_total` counters. PREPARE descriptions and
post-execution encoding failures are excluded. Catalog access during preparation
and execution contributes to `godb_mysql_catalog_duration_seconds`, consistent-read
metrics, and the existing catalog span. Bounded SELECT evaluation over metadata
is not reported as a user-table SQL statement. The discovery activity panel now
includes both SHOW discovery and INFORMATION_SCHEMA rates without adding query,
table, or column labels.

TABLE_CONSTRAINTS and KEY_COLUMN_USAGE use the existing INFORMATION_SCHEMA execution
and error counters, catalog latency histogram, read-barrier metrics, and catalog
span. Their queries appear in the same dashboard activity/latency panels; no
constraint-name, table-name, or per-view labels are added. Prepared descriptions
still contribute to catalog reads but not execution counts.

CREATE TABLE index declarations use the existing `create_table` SQL duration/count
series, preparation metrics, Raft proposal/application metrics, and storage counters.
No index names or new statement-kind labels are introduced. INSERT index work and
unique rejections remain covered by the existing row-write and command-result
metrics. The SQL statement dashboard describes this expanded CREATE scope; metadata
panels discover the resulting indexes and constraints through the same counters.

Indexed user-table SELECTs use the existing `select` statement duration/count and
read-barrier metrics for primary/unique lookups, primary ranges, and index-prefix reads. Returned-row
counters count every row in a successful result, including nonunique matches.
AND-connected predicates and complete composite keys use the same metrics; returned
rows are counted after predicates, sorting, and OFFSET/LIMIT have been applied.
Sorting time is included in SQL execution and server read-barrier duration; no
column names or limit values are attached as labels. A query exceeding the
1,024-candidate-row scan bound before filtering records an execution error and adds
no returned rows. The SQL rate, latency, and row panels cover these access paths;
no table/index names or predicates are added as metric labels. Wire encoding limits
remain a later stage, so engine success counters do not prove client delivery.

SQL UPDATE uses the bounded `update` statement label and existing proposal/apply
metrics and spans. `godb_sql_rows_affected_total` counts INSERT rows and changed
UPDATE rows, plus removed DELETE rows; it excludes unchanged/missing mutations. The new
`godb_sql_rows_matched_total` counts successful UPDATE matches, including unchanged
rows. Both include successful retry responses with their original durable counts;
they measure logical responses, not distinct physical mutations. CLIENT_FOUND_ROWS
changes the wire OK count only, not either metric. The SQL rows panel charts both
alongside returned rows. Constraint failures appear as statement/apply errors and
contribute no changed or matched rows. The bounded apply result label additionally
recognizes `invalid_update` for row/key capacity rejection.

The new matched-row series requires running the updated GoDB binary. Grafana can
load the panel immediately; an older server has no series to plot yet.

SQL DELETE uses the bounded `delete` statement label and existing proposal/apply
spans. Removed rows contribute to `godb_sql_rows_affected_total` and the dedicated
`godb_sql_rows_deleted_total`; missing/NULL predicates contribute zero.
`godb_sql_rows_matched_total` remains specific to UPDATE. The SQL rows panel includes
a deleted-row rate. These counters measure successful response counts, including
saved results returned for deduplicated retries, not distinct physical deletions.
Run updated server binaries to expose the deleted-row series; dashboard provisioning
alone does not update the engine.

## SELECT access paths

`godb_sql_select_total{access,outcome}` counts prepared SELECT executions by bounded
access path (`primary`, `unique`, `primary_range`, `index_range`, `index_scan`, `table_scan`) and outcome
(`ok`, `error`, `canceled`). The dashboard charts these paths, making fallback table
scans visible alongside indexed reads. NULL short circuits still count the selected
plan; server executions rejected at the quorum barrier also count. Preparation
failures and calls rejected before execution admission do not create access-path
series. These are execution counts, not physical scanned-row or page counters.
Returned-row metrics count only final successful results after filtering and LIMIT;
candidate-limit errors return zero rows. Existing SQL and read spans cover scans.

Updated server binaries are required to expose this series. Dashboard provisioning
alone does not rebuild the running database containers.

## Aggregate SELECT executions

`godb_sql_aggregate_total{function,outcome}` counts prepared aggregate SELECT
executions. Function is currently `count`; outcomes are `ok`, `error`, or `canceled`.
This counter includes suppressed result rows (LIMIT/OFFSET), NULL short circuits,
and server quorum failures. Preparation/admission failures are excluded. The
same calls also contribute to SELECT access-path and statement metrics. Aliases,
column names, SQL text, and values never become labels.

The new Aggregate SELECT panel charts execution rates. Returned-row counters count
one row for a visible COUNT result, including zero, not the value of the count;
LIMIT 0/positive OFFSET contributes no returned rows. Existing SQL/read spans and
latencies cover COUNT. Updated server binaries are required to expose the series;
provisioning the dashboard alone does not update running containers.

NULL predicates use the existing SELECT access-path, aggregate, latency, and
returned-row metrics. `IS NULL` against a nullable unique index is reported as
`index_scan`, since it may match several rows. `IS NOT NULL` uses another constrained
index when possible and otherwise reports `table_scan`. An impossible IS NULL
predicate on a NOT NULL column counts the selected plan even when it skips storage;
server quorum admission still applies. No predicate text, column names, or values
are added as labels. Dashboard descriptions identify these access-path semantics.

Ordered `<`, `<=`, `>`, and `>=` predicates reuse the existing SELECT, aggregate,
latency, and returned-row metrics. Primary-key ranges now report
`godb_sql_select_total{access="primary_range",outcome}`. Point lookups retain
priority, then primary ranges precede secondary prefixes/ranges and table scans.
Secondary ranges report `access="index_range"`, including ranges following an
equality or IS NULL prefix. Other ordered comparisons remain residual filters. Candidate-limit failures report
errors without returned rows; the cap is measured inside the chosen range.
Empty ranges still require server quorum admission and count as executions.

The existing SELECT access-path panel automatically plots the new bounded label.
Descriptions distinguish primary and secondary ranges from residual comparisons. Existing
page-read metrics can show reduced traversal. Query text, bounds, and values are
never labels. Updated server binaries are required to run the new path; dashboard
provisioning alone does not rebuild the containers.

## EXPLAIN executions

EXPLAIN SELECT uses `godb_sql_statement_duration_seconds{statement="explain",outcome}`
and its histogram count for latency and throughput. These series include execution
failures and quorum failures; server preparation is measured separately as before.
The SQL rate and p99 panels automatically show the bounded `explain` label. No new
high-cardinality labels are introduced.

Each successful EXPLAIN contributes one to `godb_sql_rows_returned_total`. It does
not increment `godb_sql_select_total` or `godb_sql_aggregate_total`, because the
explained SELECT is not executed. It does not affect changed/matched/deleted-row
counters. The existing SQL preparation, execution, and read-barrier spans cover it;
SQL text, index/table names, and values are not attached. Dashboard descriptions
state these distinctions. Rebuild server containers to execute the new feature;
dashboard provisioning alone updates only the dashboard.
