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
An explicit `PutBatch` or row insert request is never split across flush groups.

Each group holds the database write lock while applying entries and performing
one `Sync`. Every accepted request in that group receives success only after `Sync`
succeeds. Splits write their pages and header without an intermediate sync; the
group performs the final sync. No write, including a header update, is skipped.

Decoded leaves are reused only within a group. A split clears this temporary
state, and the entire state is discarded after the group. This reduces repeated
decoding when a batch updates the same leaf. Entries are not reordered or
coalesced: changing group boundaries does not change their application order.

## Row insert plans

`database.PlanRowInsert(table, row)` prepares a `RowInsertPlan` without opening a database, checking stored data, enqueueing work, or writing pages. `Primary` contains the encoded primary key and row; `Indexes` contains the secondary entries sorted by ascending index ID. Sorting does not change the caller's table. The plan retains each secondary entry's `RequiresUniqueCheck` flag.

Every key/value pair is checked with the pager's `ValidateEntry`, including the complete key prefix, escaping, primary-key suffix, and primary-key reference value. The primary entry plus secondary entries must fit within `MaxBatchEntries`: currently at most 63 secondary indexes per insert. Definitions can declare more indexes, but this planner rejects them with `ErrBatchTooLarge`. Size errors preserve `pager.ErrPagerEntryTooLarge` through wrapping. Failures return a zero plan, even if earlier entries encoded successfully.

The plan owns its buffers independently of the row, schema, and other entries. It is a mutable preparation result, not a transaction or proof that insertion will succeed. `InsertRow` builds a fresh plan internally rather than accepting arbitrary caller-edited plans. Passing plan entries directly to `PutBatch` bypasses insert conflict checks while still using the write-group WAL protocol. The insert request retains an owned schema copy and verifies it against the catalog during execution; schema versioning remains future work.

This planner is for inserts. `DeleteRow` reads the stored row inside the writer and uses its derived index keys for removal. `UpdateRow` compares that stored plan with the replacement row and writes only changed entries.

## Row insertion

`db.InsertRow(table, row)` encodes and validates a fresh plan before queueing it through the same writer as `Put` and `PutBatch`. The request owns the resulting bytes and a deep copy of the table definition. The writer verifies that definition against the persisted catalog before any primary/unique conflict checks or writes. Missing tables return `ErrTableNotFound`; conflicting definitions return `ErrSchemaMismatch`. These are per-request rejections, not storage failures. Callers must not mutate the supplied row or schema concurrently with the call.

Under the database write lock, the writer checks primary-key absence and every secondary entry marked `RequiresUniqueCheck` before writing any entry for that row. Checks see successful earlier mutations in queue order, including earlier requests in the same flush group. They use the writer's leaf cache and never call the public locking `Get` method.

- An existing primary key returns `ErrDuplicatePrimaryKey`, even for identical row bytes. This is an insert operation, not an upsert or a deduplicated replay API.
- A unique secondary key pointing to another primary key returns `ErrUniqueConflict`. An existing reference to the same primary key is allowed when the primary row itself is absent.
- Non-unique entries and unique entries containing null use primary-key suffixes, so duplicate indexed values remain separate.

A conflict rejects only that request, writes none of its entries, and does not poison the database. Other requests in the group can proceed. A group containing only rejected inserts performs no writes or sync. Successful inserts are acknowledged after the group sync; readers sharing the handle cannot observe a partially applied group.

Storage failures stop the handle. Accepted row/index mutations are covered by the write-group WAL; reopening replays a complete committed group. An error after logging does not imply rollback. In a group with a storage failure, requests already rejected by a conflict keep that conflict result; accepted or unprocessed requests receive the storage failure.

Tables must first be registered with `CreateTable`. The persisted catalog enforces matching table/index definitions and stable IDs; see [catalog semantics](schema-catalog.md). Raw `Put`/`PutBatch` remain unrestricted key/value APIs and can overwrite catalog, row, or index keys without enforcing schema constraints. Inserts are serialized within one shared handle; an exclusive advisory file lock rejects a second cooperating opener.

## Reads and shutdown

`db.GetRow(table, primaryKey)` validates and encodes the typed primary key, checks the supplied table against the catalog, reads the row, and decodes the stored bytes against the persisted table definition. It returns `(row, true, nil)` when present, `(nil, false, nil)` when missing, and `(nil, false, err)` on error. It also checks that the decoded row's primary key matches the lookup key. Invalid schemas or key types fail before reading storage; incompatible definitions fail during the catalog check; malformed row bytes fail during decoding. Returned rows own their data, and null column values remain distinct from a missing row.

As with inserts, caller-supplied definitions must match the catalog. Column/index declaration order may differ; names and all other fields must match. The catalog and row are read under one lock and copied, so row decoding can happen after that lock is released. Do not mutate the supplied schema concurrently with a call.

`db.GetRowByUniqueIndex(table, indexID, values...)` performs an exact lookup of a unique secondary index using values in declared column order. It holds one read lock across catalog verification and both row/index storage lookups so a writer cannot intervene between reading the index reference and the primary row. Both results are copied before releasing the lock. Decoding then verifies the primary key and re-encodes the row's indexed values to detect a dangling or stale reference. Missing index keys return `(nil, false, nil)`; dangling references and mismatched rows return errors. Null tuples and non-unique indexes use the scan API because they can identify multiple rows.

## Bounded scans

`db.Scan(start, end, limit)` returns raw key/value entries in ascending byte order for the half-open range `[start, end)`. A nil start or end is unbounded on that side. An empty non-nil end is the exclusive empty-key boundary, so it returns no entries. Equal bounds form an empty range. Reversed bounds or a limit outside `1..MaxScanEntries` (currently 1024) return `ErrInvalidScan` before reading pages.

Scans prune unrelated subtrees using internal separators, then traverse candidate children in key order. They stop once the requested number of entries has been collected. This uses existing internal-page links and does not add sibling links or change the page format. Traversal is iterative, checks visited page IDs for cycles/reuse, and rejects out-of-order returned keys. It is a bounded query, not a full-tree integrity check: unvisited pages are not validated.

`db.ScanRowsByIndex(table, indexID, limit, values...)` scans one secondary index. Values describe complete leading index components, in declared order. With no values it scans the entire index; with all values it finds equal tuples, including duplicates and null-containing unique tuples. Partial composite keys match all rows sharing those leading components. For example, an index on `(last_name, first_name)` can scan all rows with a supplied last name. A text value is matched exactly, not as a string prefix.

The typed scan holds one read lock while checking the catalog and collecting index entries and their referenced primary rows. Writers therefore cannot change that view during collection. Decoding and verification happen after the bytes have been copied and the lock released. Returned rows preserve index order; ties in non-unique or null-containing unique tuples use primary-key order. Dangling references, invalid row bytes, or mismatched index entries return an error and no partial results. Raw scan failures likewise return no partial results. Empty results are nil slices.

Both scan APIs return owned data and at most `limit` results. Use their paginated counterparts below when more matches are needed. Each call can block writers while reading pages, so choose a practical limit. Descending scans, text-prefix matching, and typed inequality-bound construction are not implemented; raw `Scan` supports explicit encoded byte bounds.

## Paginated scans

`db.ScanPage(start, end, limit, cursor)` returns an `EntryPage` with `Entries` and `Next`. `db.ScanRowsByIndexPage(table, indexID, limit, cursor, values...)` returns a `RowPage` with `Rows` and `Next`. Start with a nil cursor, then pass the returned `Next` to continue the same query. Stop when `Next` is nil. Passing nil again starts a new scan, including after the final page.

Both methods return at most `limit` results and read up to one additional index/raw entry to determine whether more keys exist. An exactly full final page has no continuation cursor. The extra entry is not returned; for row scans its primary reference is not fetched until it belongs to a returned page. A non-nil cursor therefore means another index key existed at read time, not that its referenced row has already been validated. On any error, the methods return a zero page with no cursor or partial results.

Continuation uses the full last returned byte key, including primary-key suffixes in secondary indexes. The next inclusive lower bound is that key followed by a zero byte, which is the smallest byte string strictly greater than it. This preserves keys that extend the previous key and handles empty keys without confusing them with the initial nil cursor. No physical page IDs are stored, so splits between calls do not invalidate the position.

`ScanCursor` is an opaque, immutable in-process value. It is bound to one database handle and the original bounds/query prefix. Raw and typed cursors cannot be exchanged. Typed cursors also bind the primary-key column ID, column IDs/types/nullability, and the selected index's column order and uniqueness. Column/index declaration-order changes are allowed. Caller-only renames are rejected by the catalog check; there is no schema alteration API yet. The page size may change. Incompatible reuse or a zero-value cursor returns `ErrInvalidCursor` before reading storage. Cursor fields own their data independently of input slices and returned rows/entries.

Cursors cannot be serialized, resumed after reopening the database, or used on another handle/node. Portable RPC tokens and persisted schema-version binding remain future work. Do not mutate query/schema arguments during a call.

Each page observes one read-locked view while copying index entries and primary rows, but pages do not share a snapshot. Inserts after the cursor position may appear on later pages; inserts at or before it are not revisited. Updated values can differ between calls. Future row-key/index changes could move rows across the boundary, so this is not repeatable-read pagination. Cursor reuse reruns the suffix query against current data. A nil `Next` only describes exhaustion at that page's read time.

Paginated scans count reads like ordinary scans; the extra lookahead adds page reads when needed, but no additional logical scan or primary-row lookup. Public limits remain `1..MaxScanEntries` even though the internal scan may collect one extra entry.

## Closing

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
Every request in the affected group that was not already rejected by a conflict
receives that failure, including those whose entries were already written. Queued and later valid writes receive the
same failure without touching storage. Reads also fail until the handle is closed.

Database changes are staged before a write-group WAL is synced. Reopening replays
a complete valid log before inspecting the database header. A failed or
unacknowledged request can therefore commit during recovery; it must not be
assumed rolled back. See [recovery](recovery.md) for ordering, checkpoint failures,
platform support, and current limits. [Write transactions](transactions.md) group
multiple mutations into one atomic commit; each occupies a separate writer group.
Logical rejection aborts the entire explicit transaction. Transactional reads use
a private page overlay and validate a database-wide revision on subsequent reads
and commit; historical snapshots remain separate work.

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
| `WriteRequests` | Calls to `Put`, `PutBatch`, `CreateTable`, `InsertRow`, `UpdateRow`, `Delete`, `DeleteBatch`, or `DeleteRow`, including empty/invalid calls |
| `WriteErrors` | Failed write API calls |
| `Deletes` | Removed keys in successfully synced groups; excludes missing keys |
| `Writes` | Key mutations in successfully synced groups, including replacements and removals |
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

Insert conflicts count as `WriteErrors`. A rejected-only group still counts as an attempted `WriteBatches` group, but adds no `Writes` or `Syncs`. Primary/unique conflict-check reads contribute to `PageReads`, not the public `Reads` counter. Catalog checks count their two key lookups toward `Reads`, including those performed by inserts; a missing table needs only the initial ID lookup.

Each `GetRow` call that passes catalog verification increments `Reads` once for the row lookup, including misses, in addition to the two catalog lookups. `ReadErrors` counts underlying `Get` failures; validation and decoding errors in the typed wrapper do not increment it, matching the existing `GetValue` behavior.

`GetRowByUniqueIndex` counts its underlying key lookups in addition to the two catalog reads: one read when the index key is missing, two when the primary-row lookup is attempted. Input validation errors add no reads. `ReadErrors` counts storage lookup failures; dangling-reference and row-verification errors remain wrapper errors.

`Scan` counts as one read. `ScanRowsByIndex` counts two catalog lookups, one scan, and one primary lookup per matching entry. Invalid arguments do not increment reads; traversal/storage failures increment `ReadErrors`, while typed row verification errors remain wrapper errors.

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

Row-insert tests also cover primary/unique conflicts, concurrent winners, mixed accepted/rejected groups, null-containing unique indexes, index persistence across splits, input ownership, and read/write/sync failure propagation.

`task bench` includes explicit batch sizes 1/8/64, parallel writes, parallel
reads, and a mixed workload. Batch benchmarks report `ns/entry` and
`syncs/entry`; their ordinary `ns/op` is for the whole batch. Existing serial
benchmarks remain available for comparisons. The fixtures use warm filesystem
caches; storage sync costs and scheduling overhead vary by machine.

## Deletion

`Delete`, `DeleteBatch`, and `DeleteRow` share the writer queue and group sync.
`Writes` includes synced removals; `Deletes` counts that subset. Row deletion checks
the current stored row and all expected index references before removing anything.
See [deletion semantics](deletion.md) for batching, failure behavior, and the current
policy of retaining sparse and empty pages.

## Row updates

`UpdateRow(table, row)` replaces a complete existing row at the same primary key.
The writer verifies existing references and new uniqueness constraints before
removing stale index keys or writing the replacement. See [updates](updates.md)
for no-op behavior, mutation limits, and failure guarantees.

## Recovery and file ownership

Public `Open` uses the write-ahead log and retains an exclusive advisory lock until
close. Each write group is staged, logged, applied, and checkpointed before success.
Database sync counters exclude WAL syncs. See [recovery](recovery.md).
