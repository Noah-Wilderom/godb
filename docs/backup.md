# Database identity, backup, and restore

New databases use header format 2 and have a random UUID available through
`db.ID()`. It identifies this physical database incarnation, persists across
restarts and tree splits, and is included in each new WAL record. It is separate
from table IDs, transaction revisions, future Raft node IDs, and Raft group IDs.

Format-1 databases remain supported without automatic migration. Their ID is
zero, and writes preserve their header format. To obtain the new identity
protection, back up the old database and restore it to a new path. Old binaries
cannot open format-2 files or replay the new WAL format.

## Commands and API

```sh
godb database backup ./example.db ./example.godb-backup
godb database restore ./example.godb-backup ./restored.db
godb database validate ./restored.db
```

The package equivalents are `db.Backup(archivePath)` and
`database.Restore(archivePath, destinationPath)`. Paths are used exactly as given;
restore does not append an extension. Use `.db` for the restored database.
Backup files are archives, not directly openable database files.

The CLI opens the source, performs any required WAL recovery, backs it up, and
closes it. It needs exclusive ownership of that database file, so it cannot open
a database already owned by a server or another handle. Code that already owns
the handle can call `db.Backup` directly while other goroutines use it.

## Consistent backups

Backup takes a shared database lock for validation and the entire copy. Writers
and close wait; ordinary readers can continue. The snapshot contains complete
commits visible when that lock was acquired. It need not include requests still
waiting in the writer queue. No pending transaction builder is included.

The source header/file size, reachable tree, catalog, rows, and indexes are fully
validated before copying. The archive includes every allocated page and a SHA-256
checksum over its metadata and payload. A failed database handle must be closed
and recovered before backing it up. The source WAL is not copied: the database is
at a completed checkpoint while backup holds the lock.

This implementation blocks writes for the full validation/copy duration. It does
not use MVCC snapshots, incremental backups, WAL archiving, compression, encryption,
or point-in-time recovery. Copying uses bounded buffers, but validation retains its
existing tree/catalog traversal memory requirements.

## Verified restore and publication

Restore checks archive magic, declared size, exact file length, checksum, source
identity, and the complete database structure. It then assigns a fresh UUID and
writes a format-2 header. Data pages, catalog definitions, and row/index encodings
are preserved. Restoring the same archive twice creates two distinct identities.
A WAL belonging to the source database cannot be replayed onto either restored
database. If the source has [log-application metadata](log-application.md), restore
preserves its group binding, applied index, and saved request outcomes. Such a
restored database still requires logged mutations. This is not a Raft snapshot-install
protocol or a way to clone a live cluster member; consensus metadata and membership
must be coordinated separately.

Both operations write a mode-0600 temporary file in the destination directory,
sync and close it, then publish it with a hard link that cannot replace an existing
path. The directory is synced before success. Restore also refuses any existing
destination WAL, including empty files and dangling symlinks. Existing destinations
are never truncated or overwritten. Destination filesystems must support hard links
and directory sync; there is no fallback to an overwriting rename.

Errors before publication leave the destination absent and clean up the temporary
file. A crash can leave a hidden `.godb-*` temporary file. An error after publication
(for example, directory sync failure) can leave a complete destination present;
inspect it rather than assuming no file was created. A published restored database
needs no WAL until its first open creates an empty one. Source files are not removed.

Checksums detect accidental corruption, not maliciously rewritten archives.
Advisory locking protects cooperating database users; unrelated raw file writes
or live file replacements are unsupported.

## Identity and recovery

Header format 2 retains the first 32 bytes' existing fields and offsets, with the
version set to 2 and header length set to 80. Bytes 32..47 contain the UUID and
48..79 contain SHA-256 of those 16 identity bytes. The remainder of the page and
the existing reserved bytes 12..15 must be zero.

Before replaying a complete valid WAL, recovery compares its UUID with both its
logged page-zero header and the current file's checksummed identity. A mismatch
or damaged current identity returns `ErrWALIdentityMismatch` without replaying or
clearing the log. Mutable header/root fields may be repaired when the identity
region remains intact. If the entire header or its identity region is destroyed,
recovery refuses to guess the file's identity; restore from a backup instead.

Legacy format-1 files and WALs have no identity protection. New WAL records for
legacy files carry a zero ID. The old WAL format is accepted only for identity-less
files. Keep legacy file/log pairs together until you migrate through backup/restore.

A UUID does not prove freshness: manually copying a file preserves its identity,
and an old WAL from that same incarnation is not rejected solely by UUID. WAL
sequence numbers and page checksums remain future work. Use coordinated restore
instead of mixing copied database and log files.

## Archive format 1

All integer fields are little endian. The archive is:

| Offset | Bytes | Content |
| --- | --- | --- |
| 0 | 8 | `GODBBKP1` |
| 8 | 8 | Database payload size, a multiple of 4096 |
| 16 | 16 | Source database UUID, zero for legacy databases |
| 32 | Payload size | Complete database file image |
| 32 + payload size | 32 | SHA-256 of the header and payload |

Trailing bytes are rejected. The payload must contain at least header and root
pages and must agree with its own database header's size and identity.
