# Durable Raft storage

`pkg/raftstore` persists the consensus log and Raft's term, vote, and commit index.
It implements `raft.Storage` for the pinned `go.etcd.io/raft/v3` v3.7.0 library.
This is separate from the database WAL and [applied-log metadata](log-application.md):
Raft must retain entries that have not yet reached a quorum or changed user data.

The [local replication loop](replication-loop.md) connects this package to database
application and handles synchronous Ready/Advance processing. The [server](server.md)
adds authenticated gRPC peer transport; distributed queries remain separate work.

## Creating and opening a member journal

```go
identity := raftstore.Identity{GroupID: groupID, NodeID: nodeID}
store, err := raftstore.Create("node.raft", identity)
// On subsequent starts, use raftstore.Open("node.raft", identity).
```

Check errors and close the store when finished. The group UUID must be nonzero and
shared by the replicas; each member's nonzero numeric node ID must be distinct.
These identifiers are chosen by the caller, never generated while replaying.
The group ID must match the one supplied to `database.ApplyLogEntry`. The journal
does not open a database or verify that pairing itself.

Create publishes a complete, synced header without overwriting an existing path.
Files have mode `0600`; the parent directory must already exist. Open requires an
existing journal and matching group/node identities. It never creates a replacement
for a missing journal. Both operations acquire a nonblocking exclusive file lock,
including against another handle reached through a symlink or hard link. Locking
currently supports the same Unix platforms as the pager. Other platforms return
an error. Do not delete, replace, or edit the journal while it is open.

## Saving a Ready batch

`store.Save(ready)` accepts the normal synchronous `raft.Ready` protocol. It saves
`Entries` and nonempty `HardState` as one checksummed journal record, with one file
sync per nonempty save. Memory is updated only after that sync succeeds. Nil or
empty hard state means unchanged. An empty storage update performs no I/O.
`MustSync == false` still syncs: this initial implementation favors a straightforward
durability contract. Caller-owned inputs must not be changed concurrently with Save;
after it returns, they may be reused. Reads return owned copies.

The future runtime must process each Ready in order:

1. Save its durable fields successfully.
2. Send outbound messages and apply committed entries in index order.
3. Apply configuration changes through Raft's `ApplyConfChange`. Normal commands
   use the database's durable apply boundary; every committed index, including
   configuration entries and leader no-ops, must also advance the database position.
4. Call `Advance` after the required work completes. Only then process another Ready.

This ordering follows the [etcd Raft integration contract](https://github.com/etcd-io/raft/blob/v3.7.0/README.md#usage).
Save does not send messages, apply user commands, or acknowledge client requests.
An appended entry is not necessarily committed; a committed entry is not necessarily
applied to the database yet. The runtime must handle each boundary explicitly.
`AsyncStorageWrites` must remain false. Save rejects asynchronous storage messages
instead of silently accepting a Ready whose persistence work is carried in messages.

## Log and election invariants

Entries start at index 1, are consecutive, and have positive, nondecreasing terms.
Their terms cannot exceed the saved current term. Entry types include normal and
both Raft configuration-change formats; payload bytes are opaque to this package.
Saving entries starting at index `i` replaces the old suffix from `i` onward.
An overlapping committed entry must be identical, and the replacement cannot
remove a committed tail. Different contents at the same index and term are also
rejected. Conflicting uncommitted entries with a new term can be replaced.

Current term and commit index cannot decrease. A vote already cast cannot be
changed or cleared within that term. A higher term may reset the vote to zero.
Commit cannot extend beyond the resulting log. Invalid requests return
`ErrInvalidUpdate` before writing and leave the handle usable.

Each batch is limited to 16 MiB of encoded payload and 65,536 entries. The limit
includes framing within the payload, not just application data. Oversized saves
return `ErrBatchTooLarge`; runtime proposal batching must respect these bounds.
The in-memory log also limits indexes to below the platform's maximum `int`.

## Recovery and failures

Records are append-only, with a checksum chained to the preceding record (and
initially the identity header). Reopening replays complete valid records. An
incomplete final record is truncated to the last valid boundary, then synced.
Complete records with invalid checksums, framing, or state transitions fail with
`ErrCorrupt` and are preserved for diagnosis. Wrong identity fails before recovery
can modify the file. Header truncation is corruption, not an empty database.

A write or sync error stops the handle with `ErrFailed`, including subsequent reads.
Do not send the Ready's dependent messages or call Advance after such an error.
Close and reopen before restarting Raft. A complete record can survive even when
Save returned an error or its acknowledgement was lost. Recovery adopts and syncs
that record; a partial record leaves both the old hard state and old log intact.

This assumes the filesystem honors file and directory sync. Checksums detect
accidental corruption; they do not authenticate data or detect removal of whole
records from the end. Process-crash tests do not simulate a disk controller that
lies about durability. Copying old journal files back over live state is unsupported.

## Membership replay and current limits

All log entries are retained, so `FirstIndex` is 1 and `Term(0)` is 0. The initial
configuration returned by `InitialState` is empty, matching the zero-index snapshot.
For a new group, use Raft bootstrap once with the same ordered peer list on each
initial member. On restart, use the existing log and **leave `raft.Config.Applied`
at zero**, then replay every committed configuration entry through ApplyConfChange
before elections, incoming traffic, or client work. Do not bootstrap again.

The database may already have applied those indexes. Its existing replay checks
return saved results without repeating mutations. Setting Raft's Applied directly
to the database's last applied index would skip configuration reconstruction and
leave the restarted node without its membership. A future snapshot implementation
will persist configuration alongside the matching snapshot index and term.

Snapshot returns the empty initial snapshot. Save explicitly rejects nonempty
snapshots with `ErrSnapshotUnsupported`; there is no compaction, snapshot creation,
installation, or retention policy. The journal and its in-memory retained log grow
with history. It is an initial storage implementation, not a complete server storage
lifecycle. Database backup archives do not include this journal and cannot by
themselves restore a Raft member. Joining a compacted cluster requires snapshot
installation work first.

Tests cover real single-node Raft bootstrap/restart, a three-node partition and
leader replacement, term/vote protection, committed-prefix protection, ownership,
concurrent reads, byte-by-byte tail truncation, corruption, short writes, sync
failures, and subprocess exits. Benchmarks cover synced single-entry and batched
saves, entry reads, and reopening a retained log.

## Journal format, version 1

All integers in the journal framing are little endian. Entry bodies use the pinned
Raft library's protobuf encoding; command bytes retain their own versioned format.

| Part | Encoding |
| --- | --- |
| File header | `GODBRFT1` (8), group UUID (16), node ID (8), SHA-256 of preceding header bytes (32) |
| Record prefix | `RAFTREC1` (8), payload length (8), bitwise complement of length (8) |
| Payload header | Current term (8), vote (8), commit (8), entry count (4) |
| Each entry | Protobuf length (4), protobuf bytes |
| Record trailer | `RAFTEND1` (8), chained SHA-256 (32) |

The record checksum hashes the previous digest followed by the entire current
prefix, payload, and end marker. There is no journal format migration yet. The
database page format, command format, and database WAL format are unchanged.
