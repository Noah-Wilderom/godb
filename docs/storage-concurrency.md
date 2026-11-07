# Concurrent storage and batching

`Open` starts one writer goroutine for that database handle. Share the returned
`*Database` between callers; do not copy it. Independent databases have independent
queues and locks. Only one open handle/process may own a database file for now;
there is no file lock preventing another handle from opening the same path.

## Writing

`Put(key, value)` remains synchronous. `PutBatch([]database.Entry)` accepts up to
`database.MaxBatchEntries` (64) entries. Entries are applied in slice order, so
the last entry for a repeated key wins. All entries are size-validated before
any are queued. An empty batch is a no-op on a healthy, open handle.

The queue holds 256 requests. A full queue blocks submitting callers, providing
backpressure. Input slices are copied before enqueueing; callers must not mutate
them concurrently with the call. Concurrent callers are ordered by their actual
channel sends, not by when their goroutines started.

The writer takes the next request and drains already available requests up to
64 total entries. It does not wait for a batching timer. If the next request
would exceed the limit, it is retained for the next batch without reordering.
An explicit `PutBatch` is never split across flush groups.

Each group holds the database write lock while applying entries and performing
one `Sync`. Every request in that group receives success only after `Sync`
succeeds. Splits write their pages and header without an intermediate sync; the
group performs the final sync. No write, including a header update, is skipped.

Decoded leaves are reused only within a group. A split clears this temporary
state, and the entire state is discarded after the group. This reduces repeated
decoding when a batch updates the same leaf. Entries are not reordered or
coalesced: changing group boundaries does not change their application order.

## Row insert plans

`database.PlanRowInsert(table, row)` prepares a `RowInsertPlan` without opening a database, checking stored data, enqueueing work, or writing pages. `Primary` contains the encoded primary key and row; `Indexes` contains the secondary entries sorted by ascending index ID. Sorting does not change the caller's table. The plan retains each secondary entry's `RequiresUniqueCheck` flag.

Every key/value pair is checked with the pager's `ValidateEntry`, including the complete key prefix, escaping, primary-key suffix, and primary-key reference value. The primary entry plus secondary entries must fit within `MaxBatchEntries`: currently at most 63 secondary indexes per insert. Definitions can declare more indexes, but this planner rejects them with `ErrBatchTooLarge`. Size errors preserve `pager.ErrPagerEntryTooLarge` through wrapping. Failures return a zero plan, even if earlier entries encoded successfully.

The plan owns its buffers independently of the row, schema, and other entries. It is a mutable preparation result, not a transaction or proof that insertion will succeed. A future executor must check primary-key absence and unique-index conflicts inside the serialized write path, then maintain all entries atomically with crash recovery. Passing plan entries to the current `PutBatch` would bypass those checks and would not provide atomicity. Plans must also be tied to the schema version used during execution when schema versioning is added.

This planner is for inserts. Updates and deletes need the old row to identify and remove stale index entries; they are not implemented here.

## Reads and shutdown

`Get` takes a read lock and accesses the tree directly. It never goes through the
writer queue. Readers can run concurrently, but wait while a group is modifying
pages or syncing. The 64-entry limit bounds group size, not wall-clock latency:
slow storage or cascading splits can still delay reads.

`Close` stops admission, drains accepted requests, waits for the writer to exit,
then closes the file under the write lock. Calls racing with shutdown either
enter the queue and complete or receive `ErrClosed`. Repeated `Close` returns
nil. All methods remain synchronous; no cancellation API is provided yet.

## Errors and durability limits

Invalid input is rejected before queueing and does not affect other requests.
Storage, decoding, or split-planning errors during application stop the handle
with `ErrWriteFailed`; the original cause remains available through `errors.Is`.
Every request in the affected group receives that failure, including those
whose entries were already written. Queued and later valid writes receive the
same failure without touching storage. Reads also fail until the handle is closed.

There is no rollback. A failed write or sync can leave partial changes on disk.
Closing and reopening is not recovery and may encounter corruption. Although
successful calls wait for `Sync`, the in-place page updates still lack a WAL or
copy-on-write recovery protocol. A group is not a crash-atomic transaction.

## Runtime statistics

`db.Stats()` returns ordinary numeric fields loaded from atomic counters. It
does not wait for the writer or the database lock. Fields are sampled separately,
so a snapshot during activity may span different instants. Counters belong to
one handle, remain readable after close, and reset when reopening. Opening and
validating the file are excluded. These are runtime metrics, not persisted file
metadata; opening a new CLI process cannot retrieve another handle's counters.

| Field | Meaning |
| --- | --- |
| `Reads` | Calls to `Get`, including misses and errors |
| `ReadErrors` | Failed `Get` calls |
| `WriteRequests` | Calls to `Put` or `PutBatch`, including empty/invalid calls |
| `WriteErrors` | Failed write API calls |
| `Writes` | Entries in successfully synced groups, including replacements |
| `WriteBatches` | Groups whose application started, including failed groups |
| `SplitOperations` | Completed split write plans, including cascades as one operation |
| `PageReads` | Successful page reads through the database engine |
| `PageWrites` | Successful page writes, including appends and header writes |
| `PageAppends` | Successful page appends; also included in `PageWrites` |
| `Syncs` | Successful group syncs |
| `SyncErrors` | Failed group syncs |

Page and split counters can increase for a group whose final sync fails.
`Writes` increases only after a successful sync. No cache-hit metric is exposed
because there is no persistent page cache yet.

## Replication boundary

`Entry` represents a logical key/value mutation. The queue and batching policy
are local scheduling decisions, not a replicated log. The ordered application
path is separated from admission so a future Raft adapter can feed committed
commands to storage in log order. Replicate logical commands, not local page IDs
or timing-dependent queue groups.

In a replicated server, client writes must first be proposed to Raft; only
committed commands may reach storage. Before enabling that mode we still need
durable applied-log metadata, replay/deduplication rules, snapshots and recovery.
Local `Get` also needs a Raft read barrier or another explicit consistency policy
before being advertised as a linearizable distributed read.

One writer is scoped to one database, not to the process or cluster. A future
sharded design can give each shard its own database and Raft group. Routing,
shard movement, and cross-shard transactions are separate work. This change
does not implement Raft or horizontal scaling.

References: [Go atomics](https://pkg.go.dev/sync/atomic),
[Go memory model](https://go.dev/ref/mem),
[Raft paper](https://raft.github.io/raft.pdf).

## Verification

`task test` runs the suite with the race detector. Writer tests cover grouped
syncs, queue limits and shutdown, input ownership, sync/write/append failures,
concurrent readers/writers/statistics, and replay across different batch sizes
with cascading splits and reopen verification.

`task bench` includes explicit batch sizes 1/8/64, parallel writes, parallel
reads, and a mixed workload. Batch benchmarks report `ns/entry` and
`syncs/entry`; their ordinary `ns/op` is for the whole batch. Existing serial
benchmarks remain available for comparisons. The fixtures use warm filesystem
caches; storage sync costs and scheduling overhead vary by machine.
