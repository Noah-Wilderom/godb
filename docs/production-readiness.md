# Production-readiness work remaining

Built-in metrics and sampled tracing make the engine easier to operate. They do not
make the current database production-ready. This is the remaining work, in priority
order, based on the implemented storage and fixed-membership server.

## 1. Establish correctness and recovery gates

- Extend crash tests to disk-full, short writes, sync failures, torn/corrupted pages,
  and process kills under sustained concurrent workloads. Run longer randomized
  Raft histories and verify invariants/linearizability against an independent model.
- Add Raft snapshot creation/installation and log compaction. Define retention for
  database applied-entry/request-result metadata before pruning it. Both histories
  currently grow indefinitely, and the Raft store retains entries in memory.
- Build coordinated member recovery and restore procedures. Existing database
  backups don't contain a complete Raft member state; never restore by independently
  replacing live database and journal files.
- Add checksums/recovery policy for ordinary data pages and exercise full validation
  after recovery. Current journal checksums do not cover every stored database page.

## 2. Define the public service contract

- Build the remote command API with deadlines, bounded queues, leader hints,
  stable request IDs, ambiguous-outcome documentation, and authorization.
- Extend the implemented SQL ReadIndex/application barriers with independent
  linearizability histories, remote leader routing, and future scan/snapshot semantics.
  Arbitrary local Get calls still provide no quorum barrier.
- Define liveness/readiness separately from quorum and write availability. Keep
  telemetry accessible during recoverable failures without declaring a stale node ready.
- Specify schema/command compatibility and migration/version gates before upgrades.

## 3. Complete the operational lifecycle

- Design safe membership changes, learner catch-up, leadership transfer, rolling
  restart, and disaster-recovery runbooks. Current membership is fixed at bootstrap.
- Exercise certificate expiry/rotation, CA rollover, revoked/removed member policy,
  and peer identity configuration. Add client credentials/permissions separately.
- Add bounded recovery/startup limits and capacity planning for memory, journal,
  data files, and request history. Define overload behavior beyond existing queue caps.
- Standardize structured logs and correlation fields. Raft still uses its default
  logger; avoid logging keys/values or command payloads. Keep profiling opt-in on a
  protected diagnostics endpoint rather than exposing pprof with metrics by default.

## 4. Validate operations against measured objectives

- Establish latency/error/availability SLOs under realistic multi-node I/O and
  network conditions. Development benchmarks aren't production throughput claims.
- Create dashboards and tune the starter alerts using load tests and failure drills.
  Test exporter outages, scrape failures, slow disks, and network partitions.
- Monitor host disk space/IOPS, memory pressure, descriptors, CPU, and filesystem
  errors in addition to engine metrics. Test backup restoration regularly.

Sharding, routing, cross-shard semantics, and rebalancing are separate design work.
Replicating one Raft group provides replicated state and failover, not automatic
horizontal write scaling. The initial [SQL layer](sql.md) supports a small
CREATE/INSERT/primary-key UPDATE/DELETE/bounded SELECT subset with replicated server execution. The initial MySQL
wire endpoint supports Go/PDO native prepared statements, typed binding, bounded
local session setup/information queries, connection resets, and quorum-confirmed
table/column/index discovery. Six INFORMATION_SCHEMA views now support bounded projection/filter/order/count
queries, including primary and unique constraint metadata. CREATE TABLE supports
unique and secondary-index declarations. SELECT supports primary/unique lookups and
bounded composite-prefix scans or fallback table scans, with optional AND-connected comparison/NULL filters, ORDER BY, and
LIMIT/OFFSET within the 1,024-candidate bound. COUNT(*) and COUNT(column) support
single aggregate projections under that same bound, including aliases and typed
parameters; grouping and broader aggregates remain unsupported. Primary-key comparisons
and eligible secondary-index comparisons now seek bounded ranges, including
composite equality/IS NULL prefixes. Other comparisons remain residual filters. EXPLAIN SELECT exposes the chosen plan
without scanning user rows, using a GoDB-specific output schema and no cost or
cardinality estimates. Traversal beyond that bound, foreign
keys, broader expressions, and migration DDL remain pending. Broader session
semantics, durable client retry/DDL reservation design, broader query planning,
and additional index implementations remain
product work alongside these operational gates.
