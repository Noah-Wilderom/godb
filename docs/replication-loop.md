# Local Raft processing loop

`pkg/replication` connects the durable [Raft journal](raft-storage.md) to the
database's [committed-entry apply API](log-application.md). It uses etcd Raft's
`RawNode` and processes work synchronously. There are no background goroutines,
timers, network listeners, or client RPC handlers in this package.

## Opening a member

Open the database and journal, then construct the node:

```go
node, output, err := replication.New(replication.Config{
    Store: journal,
    Database: db,
    BootstrapPeers: []uint64{1, 2, 3},
})
```

Check `err` before consuming `output`. Supply bootstrap peers only when creating a
new group. The list must contain this member, contain 1–64 distinct nonzero IDs,
and be identical across initial members. The runtime sorts it so callers' ordering
does not change the initial log. IDs reserved by the Raft library are rejected.
Every member needs its own journal and database, the same group UUID, and equivalent
starting database contents. Fresh empty databases are the straightforward starting
point. A nil peer list with a fresh journal creates an unconfigured member; it does
not automatically join or create a group.

On restart, omit BootstrapPeers. New checks the database's group, applied index,
and applied term against the durable journal. A database ahead of the saved commit
index, a mismatched group/term, or compacted storage is rejected. Replay also checks
previously applied command identities against the database's retained metadata.
Rebootstrapping a nonempty member is rejected before any changes.

New reconstructs membership by replaying from index 1, applies any committed work
missing from the database, and finishes that recovery before returning a usable
node. It intentionally keeps Raft's initial Applied setting at zero. Already
applied database entries return their saved outcomes without repeating mutations.
Recovery does not accumulate historical result notifications in Output; use the
database's AppliedRequest or retry a request through the leader to retrieve an
outcome. Outgoing messages, if any, must still be handled.

The caller owns both storage handles and must give them to only one active node.
Do not call RawNode, journal Save, database mutations, or ApplyLogEntry independently
while this loop owns processing. `node.Close()` stops new events but does not close
either handle; close the node first, then the database and journal. It is safe to
call Close repeatedly. Once initialized through bootstrap, the database also
enforces its existing restriction against writes outside the log.

## Events and outputs

| Method | Meaning |
| --- | --- |
| `Tick()` | Advance Raft's logical clock by one tick |
| `Campaign()` | Request an election; this does not guarantee leadership |
| `Propose(requestID, command)` | Submit a validated command on the local leader |
| `ReadIndex(id)` | Start a leader-only quorum read barrier with a unique nonzero uint64 context |
| `Step(message)` | Process a peer message for this group/member |
| `Status()` | Read local leader, term, committed index, and applied index |
| `Close()` | Stop the loop without taking ownership of the files |

Mutating events return `Output{Messages, Results, Reads}`. Deliver each message to its
`Raft.To` member and feed it into that member's Step method, retaining the GroupID.
Outbound protobuf messages are owned copies that can be marshaled after the event
returns. Inbound messages are copied before entering Raft; don't modify input data
concurrently with the call. Event methods are serialized by a mutex. A future
server should drive events and deliver their outputs through one ordered event
loop, with a bounded transport queue. Transport delivery and result handling happen
after the event method returns.

Propose returns `ErrNotLeader` on followers; it does not forward proposals. Status
reports the locally known leader ID, which may be zero or stale. A successful
Propose call alone is **not a commit acknowledgement**. It can return only outgoing
messages while waiting for a quorum, or no result while isolated. A submitted entry
can subsequently be overwritten by another leader.

An Output result contains the request ID, SHA-256 command digest, committed index/term,
and durable database command outcome. Only these applied results can satisfy a pending client write.
A non-`ok` command code, such as `unique_conflict`, is a committed outcome, not a Go
error. Results can appear on every replica, including followers applying entries;
the future server must match them to its own pending client requests. A retry uses
the same request ID and command bytes; different intended operations need new IDs.
The same ID with changed bytes produces the database's request-ID-conflict result.
Client timeouts and lost outputs do not cancel entries that may still commit.

Database reads remain local reads. Neither Status reporting leadership nor a recent
successful proposal makes arbitrary reads linearizable. `ReadIndex(id)` uses
ReadOnlySafe and emits `ReadResult{ID, Index}` only after quorum confirmation. IDs
must be unique for the lifetime of a node. Match the result to a waiting request
and wait for local Applied >= Index before reading. The server implements this
barrier for SQL preparation and SELECT; it rejects old-term requests and followers.

## Persistence and apply ordering

For every Ready batch the loop:

1. Saves entries and hard state through the journal and waits for its sync.
2. Applies committed entries in order through database.ApplyLogEntry.
3. Applies bootstrap configuration entries to RawNode after advancing the matching
   database position with a no-op.
4. Copies outbound messages and calls Advance.

Messages and result notifications are returned only after all pending Ready work
succeeds. This uses a conservative ordering allowed by the
[etcd Raft integration contract](https://github.com/etcd-io/raft/blob/v3.7.0/README.md#usage).
The loop enables pre-vote and quorum checking, with ten election ticks per heartbeat
tick. Async storage writes and proposal forwarding are disabled. Proposal and
replication message limits keep individual Ready saves within the journal's batch
capacity; uncommitted proposal data is limited to roughly four maximum envelopes.

Every committed index advances the database position, including empty leader
entries and bootstrap configuration entries. User entries carry a versioned
request envelope. Invalid commands are rejected before local proposal; incoming
append entries are also checked before entering Raft. A malformed committed journal
entry encountered during recovery stops startup rather than being skipped.

Any journal save or database apply failure stops the node with ErrStopped. All
later events and Status return that error. The failing call returns no output,
even if earlier work in the call already became durable. Close the node and reopen
both files before constructing another node. Recovery reconciles the two progress
markers: it applies committed-but-unapplied entries and reuses outcomes for entries
already applied before a lost acknowledgement. Merely appended, uncommitted entries
are not applied on startup; only Raft can subsequently commit or replace them.

Invalid proposals, unsupported peer messages, and ordinary Raft proposal rejection
do not stop an otherwise healthy node. Outputs are not a durable delivery queue;
Raft retries lost replication messages, and clients must retry ambiguous writes.

## Proposal envelope, version 1

| Bytes | Meaning |
| --- | --- |
| 0–7 | ASCII `GODBRPL1` |
| 8–23 | Nonzero request UUID |
| 24 onward | One complete canonical database command |

The size limit is the existing 1 MiB command limit plus the 24-byte envelope.
EncodeProposal and DecodeProposal validate the command and return owned byte
slices. An empty Raft entry means an internal no-op; it is not a valid proposal
envelope. Group identity is stored in the member journal and peer message wrapper,
rather than repeated inside every user command.

## Boundaries of this step

This runtime supports a fixed initial voting set. It replays the version-1 AddNode
entries emitted by bootstrap; dynamic membership changes, learners, joint consensus,
leadership transfer, and configuration-change proposal APIs remain unimplemented.
Nonempty snapshots and unsupported entry/message types are rejected. Inbound peer
messages are bounded to 8 MiB and must have a matching group, destination, sender,
and supported type. These routing checks are not authentication: the eventual
transport must authenticate trusted members and bind their identities to messages.

The [server](server.md) now drives this loop, provides authenticated gRPC peer
transport, waits for local proposal outcomes, and serves SQL point reads after
quorum/application barriers. Remote client RPCs, snapshot installation, compaction,
and online membership management remain
separate work.

Tests exercise three independent database/journal pairs, typed rows and unique
indexes, atomic command rejection, no-quorum proposals, leader replacement,
request deduplication, startup mismatch detection, concurrent events, injected
storage failures, and process exits between append, commit, and database apply.
Benchmarks measure local single-member proposal/apply and envelope decoding; they
do not measure networked cluster throughput or production disk latency.
