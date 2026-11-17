# Row updates

`db.UpdateRow(table, row)` replaces the complete stored row identified by
`row[table.PrimaryKeyColumnID]`. All columns are required, including explicit nulls
where applicable. It is not a patch or an upsert. The method returns `(true, nil)`
when the primary row exists, including an identical replacement, and `(false, nil)`
when it is missing. Invalid input and failed updates return `(false, err)`.

The primary key identifies the target; there is no separate old key argument.
Supplying another primary key targets that other row. Primary-key relocation is
not supported by this API.

## Writer execution

The new row is validated and encoded before queueing, and the request owns the
encoded buffers and a copy of its table definition. Inside the serialized writer:

1. Check the supplied definition against the persisted catalog.
2. Read and validate the current stored row and all its expected index references.
3. Derive which index keys changed, in ascending index ID order.
4. Check the mutation budget and new unique keys before writing anything.
5. Delete changed old index keys, replace the primary row, and insert changed new
   index entries. Unchanged indexes are left untouched.
6. Acknowledge the update after the write group's sync.

New unique keys may already point to the same primary row, but references to a
different primary row return `ErrUniqueConflict`. Nullable unique tuples use the
existing primary-key suffix rules. A new nonunique entry with an incorrect primary
reference is corruption, not a uniqueness conflict. Old references are checked
even when the replacement is identical.

Missing rows and identical replacements cause no writes. A group containing only
such requests does not sync. Changing only non-indexed values writes one primary
entry. With `k` changed indexes, an update uses `1 + 2*k` key mutations. More than
64 mutations returns `ErrBatchTooLarge` before any writes. Tables with many indexes
can still update unchanged/unindexed values or a sufficiently small subset of
indexes. Queue grouping reserves `min(1 + 2*indexCount, 64)` slots per update;
the exact mutation count is checked against the current stored row during execution.

Each update sees earlier accepted operations in its writer group. Concurrent
updates to the same primary key replace the row in queue order; there is no
compare-and-swap or optimistic version check. Readers sharing the handle see a
consistent view because the writer holds the lock across all changes and sync.

## Errors and current limits

Schema mismatches, missing table definitions, uniqueness conflicts, and mutation
budget errors reject only that request. Other requests can proceed. Corrupt stored
rows/index references and storage failures stop the handle with `ErrWriteFailed`.
An error after writing begins can leave partial changes; false on error does not
mean the old row is intact. Reopening uses [WAL recovery](recovery.md) to replay a complete valid write group. A failed or unacknowledged update can still commit during recovery.

The update checks expected old and new entries, not arbitrary extra stale entries
introduced by raw writes. Full database validation remains the tool for finding
those. Updates can be grouped with other mutations through [write transactions](transactions.md).
SQL `UPDATE` now uses [partial-row commands](sql.md#updating-a-row) to merge assigned
columns at commit time. The Go `UpdateRow` API still replaces the complete row.
Schema alteration and primary-key changes remain future work.

`Writes` counts all successfully synced key mutations; `Deletes` counts removed
old index entries. Both stay unchanged for no-op updates and for failed groups.
Catalog checks count logical reads, while internal row/index checks contribute
page reads. `WriteRequests`, `WriteErrors`, and `WriteBatches` include updates.
Pagination retains its existing live-view behavior: changing an indexed value can
move a row before or after a cursor, so pages do not form a repeatable-read snapshot.
