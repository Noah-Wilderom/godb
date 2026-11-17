# Deletion

The database exposes three deletion methods:

| Method | Successful result |
| --- | --- |
| `Delete(key)` | `(true, nil)` if a raw entry was removed, `(false, nil)` if absent |
| `DeleteBatch(keys)` | Number of raw entries removed, with no error |
| `DeleteRow(table, primaryKey)` | `(true, nil)` if the row and its expected index entries were removed, `(false, nil)` if the primary row was absent |

All three use the existing writer queue, write lock, and group sync. Successful
removals return only after sync. Duplicate keys within a delete batch are processed
in order and count only once. A group consisting entirely of missing keys performs
no writes or sync. Empty batches are no-ops on a healthy open handle; closed or
failed handles still return their corresponding errors.

Raw delete batches have the same 64-entry limit as puts. Inputs are copied before
enqueueing. Raw deletion can remove any key, including catalog or row/index keys,
and therefore can bypass schema and index consistency. Use `DeleteRow` for typed
rows. `DeleteRow` reserves space for its primary key and every secondary index
when the writer builds a group, even though the actual index keys are determined
later. This keeps mixed put/delete groups within the existing limit.

## Row and index consistency

The writer checks the supplied definition against the persisted catalog, then
reads the current stored primary row and derives its index keys. It does this
inside the write lock, so a queued delete observes earlier accepted writes in the
same group. The request owns its schema and encoded primary key.

Every expected index entry must exist and point to that primary row before any
entry is removed. This includes unique, nonunique, nullable, and composite indexes.
Index keys are processed in ascending index ID order after the primary key.
Missing or mismatched schemas reject only the request. A malformed row, corrupt
catalog, or missing/incorrect expected index reference stops the handle with
`ErrWriteFailed` (and the underlying `ErrInvalidData`/`ErrInvalidCatalog` where
applicable). It is not silently repaired by deletion.

A missing primary row returns false without searching for orphaned indexes.
Deletion checks expected references, not arbitrary extra stale entries; use full
validation to find those. Deleting an existing row frees its unique values for a
later insert, including one in the same writer group.

Readers sharing the handle cannot observe a partially applied delete. Accepted
deletes are covered by write-group [WAL recovery](recovery.md). I/O or sync
failures can leave partial changes until reopening replays the committed log. On error, deletion methods return false/zero, which does
not guarantee that nothing changed. The error is authoritative; the handle stops
accepting further reads and writes after a storage failure.

## Tree behavior

Leaf deletion uses binary search, shifts the remaining keys and values, and clears
the vacated slice slots. The modified page is re-encoded without the removed
entry. This does not provide secure erasure from filesystem/device history.

Internal separators remain routing boundaries; they need not equal an existing
leaf key. For example, deleting `m` from a subtree whose range begins at `m` does
not change that range. Future keys in it still route to the same subtree. Sparse
and empty leaves remain linked into the tree, keep the same depth, and can accept
new entries. Scans skip empty leaves, and cursors can resume after a deleted key.

This first version does not merge pages, redistribute entries, shrink the root,
reclaim page IDs, or truncate the file. Lookup cost remains tied to the tree's
existing height even after many deletions. Page compaction and a free-page
inventory remain separate work; the file format has not changed.

## Statistics and benchmarks

`Writes` counts successfully synced key mutations, including removals. `Deletes`
counts the removal subset, not deleted rows: deleting one row with three secondary
indexes contributes four to both counters. Missing keys contribute to neither.
Both counters are updated only after the entire group's sync succeeds.

Deletes contribute to `WriteRequests`, `WriteErrors`, and `WriteBatches` using the
same rules as puts. Catalog checks count logical reads; internal delete lookups
contribute page reads without counting as public reads.

Benchmarks named `PutDeleteBatchCycle`, `InsertDeleteRowCycle`, and
`BPlusLeaf_PutDeleteCycle` measure a complete insert/delete cycle, including
reinsertion. They are not measurements of deletion alone. `DeleteMissing` measures
the no-write path.

## SQL deletion

[SQL DELETE](sql.md#deleting-a-row) uses `CommandDeleteRowCounted` with one primary
key. It reports removed rows rather than removed index entries, resolves the current
row under the writer lock, and commits counts with the mutation. Durable retries
return the original result without deleting a later replacement. The direct Go
methods and legacy `CommandDeleteRow` retain their existing behavior.
