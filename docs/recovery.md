# Write-ahead logging and recovery

Databases opened with `database.Open` now use a redo log beside the database:
`example.db.wal`. New databases use header format 2 with a persistent UUID;
existing format-1 databases can still be opened without migration. A missing WAL is created on open.
Opening now requires write access, even when the caller only plans to read.

## Write-group protocol

The writer prepares a complete group using a private in-memory page overlay.
Reads during preparation see earlier changes in the group, including new pages
and roots. Database pages are not modified during preparation. Logical rejections
remain per request for ordinary batched writes. An explicit [write transaction](transactions.md)
aborts its entire group on a logical rejection. A fatal preparation failure prevents
the entire group from being written.

For a group that changes data:

1. Truncate and sync the WAL, ensuring no previous record can overlap the next one.
2. Write the final images of all modified pages, plus page zero and the target
   database size, followed by a commit marker and SHA-256 checksum.
3. Sync the WAL before writing any database pages.
4. Apply the prepared page operations and sync the database file.
5. Truncate and sync the WAL, then return success to callers.

The WAL records final images sorted by page ID. The database applies the prepared
operations in their original order; recovery applies only their final images.
This covers catalog writes, primary rows, secondary indexes, deletes, updates,
page appends, and root/header changes within a group. Missing-key and no-op groups
do not write or sync the WAL.

This first implementation checkpoints every group. It favors a straightforward
recovery protocol over minimizing sync calls or retaining a long-running log.
Each normal nonempty group performs three WAL syncs and one database sync.

## Opening and recovery

`Open` takes an exclusive advisory file lock before accessing the WAL and retains
it until `Close`. A second cooperating opener fails with `ErrDatabaseLocked`.
Symlinks are resolved before choosing the WAL path. Lock implementations are
provided for Linux, macOS, and the listed BSD targets; other targets fail explicitly.
The implementation and tests have been exercised on Linux, including GOARCH=386.

Recovery runs before inspecting the database header, which may have been torn by
an interrupted write. An empty WAL needs no replay. A truncated record without a
complete trailer is discarded: under the protocol, it cannot have started writing
database pages. A complete record must pass its header, size, record bounds,
checksum, page decoding, identity, and appended-page coverage checks before replay starts.
Malformed complete records return `ErrInvalidWAL` and are preserved for diagnosis.
A mismatched or damaged current database identity returns `ErrWALIdentityMismatch`.
Format-2 recovery requires the checksummed identity region to remain intact even
if other header fields are torn. See [identity and backup](backup.md).

Valid records are replayed in full, the database is synced, and the WAL is cleared
and synced. Replay is idempotent; interruption during recovery leaves the record
available for another attempt. A record that was fully written but whose WAL sync
was interrupted can also be replayed if its complete valid bytes survived.

`godb database recover <path>` opens and closes the database to perform this work.
It also succeeds when nothing needs replay. `database.Validate` and the validate
command remain non-mutating: a nonempty WAL returns `ErrRecoveryRequired` instead
of validating a potentially incomplete database. Recover first, then validate.

## Failure semantics

Storage, WAL, and checkpoint errors stop the current handle with `ErrWriteFailed`.
Close it and reopen to recover. After the WAL has been written, a failed or
unacknowledged request may still be committed by recovery. Returning an error or
false/zero result does not establish that the mutation was rolled back. Inspect
state before retrying non-idempotent operations; request deduplication remains
future work.

Successful calls return after database sync and checkpoint cleanup. Recovery is
atomic at the accepted write-group level. An explicit `WriteTx` occupies one group,
so all its mutations recover together. Ordinary groups can contain several
independent requests, with logical conflicts excluded before logging. Transactional
reads validate their local database revision before mutation preparation. Raft
replication remains a separate feature.

Durability depends on the filesystem and storage honoring sync ordering. Process
termination tests exercise recovery, but do not simulate every power-loss or
device failure mode. Checksums detect malformed log records; there are no general
database-page checksums or repairs for unrelated corruption yet.

## Files and limits

Treat the database and WAL as a pair. Do not remove a nonempty WAL, replace one
member of the pair, use hard-link aliases, or copy live files independently. The
WAL now carries the format-2 database UUID, which detects accidentally mismatched
file/log pairs. Legacy format-1 databases still lack this protection. UUIDs do not
detect a stale log from the same incarnation. Use the coordinated [backup and
restore API](backup.md) rather than combining copied file/log pairs.
Non-cooperating raw pager/file writes can bypass the advisory lock.

Creation syncs the database and its parent directory and refuses a pre-existing
WAL at the destination. Recovery of an interrupted initial `Create`, before any
write group has been logged, is not implemented. WAL directory entries are synced
on open before groups can commit.

A group may stage at most 8191 page operations; its log holds at most 8192 final
page images including page zero, approximately 32 MiB. The overlay and encoded
record also use memory. Exceeding the staging limit stops the handle before any
group pages are written. These limits are additional to the existing 64-key group
budget. The log has one record at a time; log rotation and asynchronous checkpointing
are not implemented.

`PageWrites`/`PageAppends` count successful database-file operations, not staged
changes. Overlay hits do not count as physical page reads. `Syncs`/`SyncErrors`
currently count database-file syncs only, not WAL syncs. Logical `Writes`/`Deletes`
increase only after checkpoint cleanup succeeds; they may remain unchanged on an
error even if recovery later commits the group. Recovery occurs before a handle
is returned and is not included in that handle's counters.

## WAL format version 2

All integer fields are little endian. The 48-byte header contains:

| Offset | Bytes | Meaning |
| --- | --- | --- |
| 0 | 8 | Magic/version `GODBWAL2` |
| 8 | 8 | Target database size in bytes, page aligned |
| 16 | 8 | Page-image count, 1..8192 |
| 24 | 8 | Reserved, zero |
| 32 | 16 | Database UUID; zero only for legacy format-1 files |

Each record is an eight-byte page ID followed by a 4096-byte page image. IDs are
strictly increasing; the first is page zero, whose decoded header must agree with
the target size. The 40-byte trailer contains `COMMIT01` followed by SHA-256 of
the header and page records. No trailing bytes are accepted.

Legacy `GODBWAL1` records have a 32-byte header without the UUID. Their recovery is
still supported for identity-less databases; new records always use version 2.
