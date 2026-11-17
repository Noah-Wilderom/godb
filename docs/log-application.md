# Durable log application and request results

`db.ApplyLogEntry` is the storage boundary for applying an already committed log
entry. It is local infrastructure for a future Raft server, not a consensus
implementation or a client-facing RPC. The caller supplies a trusted ordered log;
the database does not establish that an entry has reached a quorum.

```go
result, err := db.ApplyLogEntry(database.LogEntry{
    GroupID:   groupID,
    Index:     committedIndex,
    Term:      committedTerm,
    RequestID: requestID,
    Command:   encodedCommand,
})
```

Handle a Go error before using `result`. As with `ApplyCommand`, a nil error can
return a deterministic non-`ok` command result.

## Group, index, and term

`GroupID` identifies the logical ordered log shared by replicas. It must be a
nonzero UUID chosen outside apply, and is distinct from each physical database's
`db.ID()`. The first durable entry binds the database to that group. Initial data
may already exist, but replicas must start with equivalent logical state.

Application starts at index 1 and accepts consecutive indexes with nonzero,
nondecreasing terms. Gaps or regressing terms return `ErrLogOrder`; another group
returns `ErrLogGroupMismatch`. Entries submitted concurrently out of order are not
buffered or reordered. A future Raft loop must feed committed entries in order.

An entry with an empty `Command` and zero `RequestID` is an explicit no-op. It
still persists its index and term. Use this for committed entries that have no
database mutation; actual Raft configuration changes remain the consensus layer's
responsibility. Nonempty commands require a nonzero request UUID and must pass the
versioned [command decoder](commands.md).

`db.AppliedPosition()` returns the durable group, index, and term. A database with
no applied entries returns a zero position. A failed or closed handle returns an
error instead of a potentially stale position. On open, WAL recovery completes
before this position is loaded and its latest-record references are checked.

## Replay and retries

Each applied index retains its term, request ID, SHA-256 command digest, and result.
Replaying an old index with those same fields returns the saved result without
writing, syncing, or rerunning the command. Different contents at an already applied
index return `ErrLogConflict`. Protocol errors do not advance the position or stop
the database handle; the future Raft integration must treat them as an integration
or log-consistency problem, not silently skip the entry.

The first occurrence of each nonzero request ID also retains its command digest,
original index, and result. At a later consecutive index:

- The same request ID and command bytes return the original result without executing
  the mutation again. The new log index is still committed.
- The same request ID with different command bytes records a deterministic
  `request_id_conflict` result and advances the index. The original request result
  remains intact.
- A new request ID executes the command once and records its result.

`db.AppliedRequest(requestID)` returns `(result, found, error)` for the original
request. A zero request ID is invalid. Missing IDs return `found == false`.
Rejected requests are remembered too: a retry does not become successful merely
because the database later changed. Use a fresh request ID for a new attempt.

These checks compare canonical command bytes by SHA-256, not arbitrary semantic
equivalence. Request IDs must remain stable across client retries and be distinct
for different intended operations. No random values are generated inside apply.

## Atomic persistence

Each new log index gets one isolated writer group. Data mutations, the applied
position, the log record, and a new request result are staged and committed through
one WAL record. This includes successful commands, deterministic rejections,
duplicate-request entries, and explicit no-ops.

When a command rejects, all its earlier staged data changes and splits are discarded.
A fresh overlay then persists only the outcome and application metadata. Rejected
commands therefore advance the log without leaving partial user changes.

Success is returned only after the database sync and WAL checkpoint cleanup. Storage
errors stop the handle with `ErrWriteFailed`; the returned result is empty. Recovery
can still commit a complete logged entry after an error or lost acknowledgement.
Reopen and retry the same entry/request to retrieve its saved outcome. Never advance
an external applied marker independently of this database commit.

Crash tests cover incomplete WAL writes, durable WAL before page writes, interrupted
database writes, database sync, and checkpoint clearing, for success, rejection, and
no-op entries. After recovery, data, results, and position appear together.

## Preventing writes outside the log

Once the first entry commits, ordinary `Put`, delete, catalog/row mutation APIs,
`ApplyCommand`, and mutating `WriteTx.Commit` calls return
`ErrReplicatedWriteRequired`. This is checked under the writer lock, including for
transactions built before binding. Readers, read-only transaction validation,
backup, and close remain available. Empty write batches remain harmless no-ops.

All keys starting with `00 03` are reserved for replication metadata. Public raw
writes, deletes, transactions, and encoded commands cannot mutate that prefix even
before binding; they return `ErrReservedKey` (wrapped by `ErrInvalidCommand` during
command validation). Raw reads and scans can expose these entries. Direct pager or
filesystem writes still bypass the database API and are unsupported.

There is no unbind or history-reset operation. Older binaries do not enforce these
restrictions; do not downgrade a database bound to a log.

## Validation, backup, and limits

Full validation checks the metadata namespace, consecutive retained index history,
monotonic terms, the last position, and request/result cross-references. Opening
checks the current position and latest references, rather than auditing the entire
history; run full validation for that. A missing position with orphan metadata is
rejected. Malformed metadata returns `ErrInvalidReplicationState`.

Backup and restore preserve all application metadata. Restore assigns a fresh
physical database UUID but retains the group binding, applied position, and request
history. The restored database therefore still requires `ApplyLogEntry` for writes.
This is not yet a complete Raft snapshot installation: the consensus layer must
coordinate membership, snapshot index/term, and its own retained log before serving
the restored state. Restore does not automatically join a cluster or fork a group.

The existing 64-key mutation reservation applies to user command operations. Engine
metadata adds three key writes for a new request, or two for a no-op or duplicate
request. These also consume staged-page/WAL capacity. Metadata is included in `Writes`,
`PageWrites`, and `Syncs`. Replayed indexes add no writes. Deterministic log rejections
are durable results and do not increment `WriteErrors`; API/protocol/storage errors do.
Every newly persisted index advances the local transaction revision, so it can
invalidate an existing read transaction even when user data did not change.

History and request results are currently retained indefinitely on disk. There is
no pruning, retention window, or log-index compaction. This keeps old retries
unambiguous but grows storage with applied indexes and distinct request IDs. A
future retention protocol must explicitly define which old retries remain valid.

The database header and command wire formats do not change. Metadata uses a new
internal namespace with its own version. [Raft log and voting/term persistence](raft-storage.md)
is provided separately by `pkg/raftstore`, and the [local replication loop](replication-loop.md)
connects committed Raft entries to this API. Transport,
membership management, snapshot installation, linearizable reads, portable
read-dependent command preconditions, and cross-shard transactions remain separate
work. The saved database outcome is not a replacement for Raft's own durable log.

## Metadata encoding

Key prefixes are `00 03 00` for the position, `00 03 01` followed by a 16-byte request
UUID for a request result, and `00 03 02` followed by an eight-byte big-endian index
for an applied log record. Big-endian indexes preserve numeric tree ordering.

Position values remain version `01`. Request/log values with zero row counts also
retain version `01`. Integers in values are little endian:

| Record | Remaining fields |
| --- | --- |
| Position | Group UUID (16), index (8), term (8) |
| Request | First index (8), command digest (32), result (1) |
| Log entry | Term (8), request UUID (16), command digest (32), result (1) |

Result bytes are: 1 `ok`, 2 `duplicate_primary_key`, 3 `unique_conflict`,
4 `table_id_exists`, 5 `table_name_exists`, 6 `table_not_found`, 7 `schema_mismatch`,
8 `mutation_limit`, 9 `request_id_conflict`, 10 `invalid_update`. Other values are invalid. Original
request records cannot contain result 9. No-op records have a zero request UUID,
the SHA-256 digest of empty input, and result 1. These hashes detect mismatched
retry content; they are not general database-page checksums.

Request and log results with nonzero matched-row counts use version `02`: the same
fields shown above, followed by changed/affected rows (uint64) and matched rows
(uint64). Their sizes are 58 and 74 bytes respectively. Counts must satisfy
`0 <= affected <= matched <= 64`, with matched nonzero, and the result must be `ok`.
Zero-count outcomes use the original 42/58-byte encoding; decoders accept both
versions and validate count agreement between each request and its log records.
Explicit no-op entries cannot carry counts.

These counts are committed in the same WAL transaction as the row/index changes.
Replay of an old index, duplicate-request application at a later index, reopening,
and `AppliedRequest` all return the saved counts. Conflicting request bytes and
rejected atomic commands return zero counts. Old binaries cannot read version-2
results; upgrade all members before using partial-row UPDATE or counted DELETE commands. The database
header and row format versions are unchanged.
