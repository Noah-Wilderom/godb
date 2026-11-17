# Write transactions

`db.BeginWrite()` returns a `*database.WriteTx`. Add mutations, then call `Commit`
to submit them as one atomic write transaction. `Rollback` discards an unsubmitted
transaction. Adding mutations holds no database lock and writes nothing. Reads
take a short shared database lock; no database lock is held between method calls.

```go
tx := db.BeginWrite()
defer tx.Rollback()

if err := tx.CreateTable(table); err != nil {
    return err
}
if err := tx.InsertRow(table, firstRow); err != nil {
    return err
}
if err := tx.InsertRow(table, secondRow); err != nil {
    return err
}
return tx.Commit()
```

## Operations and ordering

The available mutation methods are `Put`, `PutBatch`, `Delete`, `DeleteBatch`,
`CreateTable`, `InsertRow`, `UpdateRow`, and `DeleteRow`. Their arguments match the
database methods. Each returns an error from preparing and validating its input;
none changes database state until commit. Inputs are copied or encoded when added.
Do not mutate input buffers concurrently with the method that consumes them.

At commit, the writer executes operations in their addition order under one
database write lock. Transactions without reads use the latest committed state;
transactions with reads first verify that their database revision is unchanged.
Later operations see the
effects of earlier ones in the transaction. Creating a table followed by inserting
its rows works. Deleting a row releases its unique keys for subsequent inserts.
Uniqueness is checked at each operation, so swapping unique values requires a
sequence that avoids an intermediate conflict.

`UpdateRow` replaces an existing row with the same primary key. Updating or
deleting a missing row is a successful no-op. Transaction methods do not return
matched/deleted counts because those are not known until execution.

Each explicit transaction gets its own writer group. Ordinary queued writes keep
their existing batching behavior. A transaction cannot absorb neighboring writes
or have its operations interleaved with them. Readers through the database handle
cannot see a partially applied transaction. Successful commit waits for the WAL,
database sync, and checkpoint cleanup. A transaction containing only no-ops needs
no WAL write or database sync.

## Errors and lifecycle

A preparation or read error is sticky: later reads, additions, and `Commit` return that error,
and none of the previously added operations is submitted. This prevents an ignored
input error from committing only part of the intended work.

Primary-key, unique-index, catalog, and schema conflicts discovered during reads or commit
abort the entire transaction before logging or database writes. The staged pages,
newly allocated page IDs, and header/root changes are discarded. The database
remains usable; create a new transaction to retry.

`Commit` and `Rollback` finish the transaction once. Subsequent calls or additions
return `ErrTransactionClosed`. A deferred rollback after commit is harmless but
returns that error. Once commit starts, rollback cannot cancel it. A zero-value
`WriteTx` is unusable; obtain one from `BeginWrite`, and do not copy it.

Methods serialize access to a transaction's builder. Concurrent additions are
ordered by mutex acquisition, so use one goroutine when the operation order matters.
Only one concurrent commit can submit the transaction. `BeginWrite` does not check
database liveness; committing against a closed database returns `ErrClosed`.
Closing the database drains submitted commits, but does not submit pending builders.

Storage failures during commit stop the database handle. A commit error after the WAL was written
can still result in the **whole transaction** being recovered on reopening. An
error does not guarantee rollback in this case, and `Rollback` cannot undo a
durable WAL record. See [recovery](recovery.md) before retrying non-idempotent work.

## Transactional reads and conflicts

`WriteTx` supports `Get`, `GetTable`, `GetTableByName`, `GetRow`,
`GetRowByUniqueIndex`, `Scan`, and `ScanRowsByIndex`, with the same arguments and
return values as the corresponding database methods. Returned data is owned by
the caller. Missing keys and empty ranges are successful reads and participate
in conflict detection.

The first transactional read records the database's in-memory revision. It also
creates a private page overlay and applies all mutations queued so far. Further
reads apply only newly queued mutations to that overlay. This provides reads of
your own inserts, updates, deletes, index changes, and newly created tables,
including changes that split the tree. The overlay never writes database or WAL
files, and it is discarded on rollback, read failure, or commit. At commit, the
writer rebuilds the changes using the same mutation implementation after checking
the revision, so previewing a mutation never consumes or changes the queued request.

Every successfully committed group that performed a key mutation advances the
database revision. Before each subsequent read and again under the writer lock
at commit, the revision must still match. Otherwise the transaction returns
`ErrTransactionConflict`, with none of its mutations committed. This check includes
read-only commits, so call `Commit` to validate the final read view even if no
mutations were added. Read-only commits do not write or sync files.

This is conservative optimistic concurrency control: **any database write can
invalidate a reading transaction**, including an unrelated key or a raw put of
the same bytes. Missing-key deletes, identical row updates, and rejected groups
do not advance the revision. Changing a value and then changing it back still
invalidates readers. Inserts into a scanned range are detected, including ranges
that were empty. The revision is local to the open handle, not an on-disk version
or a Raft log index. Transactions cannot survive closing and reopening the handle.

Successful commits validate all transactional reads against one unchanged database
state plus the transaction's own preceding mutations. The view starts at the first
read, not at `BeginWrite`. This does not retain historical snapshots: once another
write commits, the old transaction fails rather than continuing to serve old data.
Per-key/range validation and MVCC are possible future improvements to reduce retries.

For read-modify-write work, read through `tx.GetRow` or `tx.Get`, derive the new
value, add the mutation to the same transaction, and commit. On
`ErrTransactionConflict`, start a **new transaction**, repeat the reads, and
recompute the changes. Do not resubmit the old computed value blindly. Keep retry
counts or deadlines bounded and avoid external side effects until commit succeeds.
A storage failure has the different, potentially committed outcome described above;
do not treat it as a conflict retry.

Ordinary `db.Get`/scan calls made while building a transaction remain independent
reads and do not establish a revision guard. Use the transaction's methods for
dependent reads. Transactional pagination, savepoints, and historical snapshots
are not implemented. Existing nontransactional pagination keeps its original
per-page consistency semantics.

## Limits and statistics

The reservation limit is 64 key mutations per transaction, including indexes:

| Operation | Reserved slots |
| --- | --- |
| Raw put/delete | One per entry/key |
| Create table | Two catalog entries |
| Insert/delete row | One primary entry plus the index count |
| Update row | `min(1 + 2*indexCount, 64)` |

Reservations are conservative, even for operations that will become no-ops.
Exceeding the budget returns `ErrBatchTooLarge` and makes the builder fail.
The existing staged-page and WAL-size limits also apply. Large transactions and
deferred constraint checking are not supported. Raw key operations continue to
bypass schema/index maintenance; use typed row operations for relational data.

`WriteRequests` counts a transaction submitted through `Commit` once. `WriteBatches`
counts its attempted mutation group; revision conflicts and read-only commits exit
before mutation preparation and do not add a batch. A logical abort adds no `Writes`, `Deletes`,
`PageWrites`, or `Syncs`. Reads and split-preparation counters can increase during
an aborted transaction. Mutations rejected during building never reach the writer.
Transactional reads and physical read operations performed during preview contribute
to `Reads`, `ReadErrors`, and `PageReads` using the existing read-path definitions.
Early lifecycle/revision errors do not reach those read paths. Preview mutations
do not add write, sync, or split counters; commit-time preparation retains the
existing split-counter behavior. The preview uses the same staged-page limit as
commit and adds bounded per-transaction memory for modified pages.

Transactions add no separate on-disk format. These transactions are local to one
database. [Log application](log-application.md) separately supports durable request
deduplication. Once a database is bound to a log, mutating local transaction commits
are rejected; read-only validation remains available. Raft and cross-shard
transactions remain separate work.

[Logical commands](commands.md) provide a separate versioned encoding and apply
API for future replication. A local transaction's read revision is not portable,
so read-dependent transactions cannot currently be exported as replicated commands.
