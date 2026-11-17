# Schema catalog

`Database.CreateTable(schema.Table)` persists a definition containing the table ID,
name, columns, primary key column ID, and secondary index definitions.
`GetTable(id)` and `GetTableByName(name)` return `(schema.Table, bool, error)`;
missing entries return a zero table, `false`, and no error. Definitions remain
available after closing and reopening the database.

IDs are explicitly supplied, nonzero, and unique within a database. Column and
index IDs are scoped to their table. Table names are unique, case-sensitive UTF-8
strings. Creation rejects existing IDs with `ErrTableIDExists` and existing names
with `ErrTableNameExists`, even when the submitted definition is identical.
There is no automatic ID allocation, replacement, alteration, or drop operation.

Both uniqueness checks run in the existing writer under the database write lock,
before either catalog entry is written. Other requests in the same write group
see earlier accepted creates. Conflicts reject only that request and cause no
writes for it. Successful creation waits for the group's sync. Storage failures
stop the handle through `ErrWriteFailed`.

Both lookup methods read the definition and its name reference under one read
lock and verify their agreement. Returned slices are owned by the caller.
Malformed definitions or inconsistent references produce `ErrInvalidCatalog`.
Each catalog key lookup counts as one read in database statistics; a successful
table lookup normally performs two reads.

## Storage format

Catalog entries share the existing B+ tree. Their keys begin with `0x00`, separate
from version-1 row/index keys, which begin with `0x01`.

The separate `00 03` namespace is reserved for [log application metadata](log-application.md).

| Entry | Key | Value |
| --- | --- | --- |
| Table definition | `00 01` + table ID as four big-endian bytes | Catalog version byte `01` + canonical JSON definition |
| Table name reference | `00 02` + raw UTF-8 name bytes | Complete table definition key (six bytes) |

Definition JSON is the compact `encoding/json` representation of `schema.Table`:
`ID`, `Name`, `Columns`, `PrimaryKeyColumnID`, `Indexes`, in that order. Columns
contain `ID`, `Name`, `Type`, `Nullable`; indexes contain `ID`, `Name`, `ColumnIDs`,
`Unique`. Types are numeric record type codes. Slice order is preserved, including
composite index column order. Nil slices encode as `null`, empty slices as `[]`.
The decoder validates the schema and requires that re-encoding yields the exact
stored JSON, rejecting unknown/duplicate fields, extra whitespace, and trailing
data. These field names and encoding rules are part of catalog version 1; changing
them requires an explicit format compatibility decision.

JSON keeps this infrequently accessed metadata straightforward to inspect. Row,
index, and page encodings remain binary and unchanged. The catalog has its own
version; it is independent of the database header version. New database headers
use format 2 for persistent identity; the catalog encoding remains version 1.

Each entry must fit the existing pager entry limit. Large definitions are rejected
before any writes; there are no overflow pages for metadata yet. Tables with more
than 63 secondary indexes are also rejected because inserting their primary row
and index entries would exceed the current 64-entry write-request limit.

## Current scope

Load a definition using `GetTableByName`, then pass the returned table to
`InsertRow`, `UpdateRow`, `DeleteRow`, `GetRow`, or the index lookup/scan methods. These methods require a
registered definition: missing tables return `ErrTableNotFound`, and valid but
conflicting definitions return `ErrSchemaMismatch`. All table, column, and index
fields must match, including names and uniqueness. Column and index declaration
order may differ, but composite index column order must match. Nil and empty
index lists are equivalent. Invalid input may fail validation before the catalog
is read; an incompatible pagination cursor may likewise fail first.

Inserts own a copy of the supplied definition and check it inside the writer,
before checking row conflicts or writing any entries. Missing or mismatched
definitions reject only that request without poisoning the handle. The check sees
earlier accepted catalog writes in the same group. Corrupt catalog records or
storage failures encountered by the writer stop the handle with `ErrWriteFailed`.

Typed reads hold one read lock across checking the catalog and collecting row or
index data, then decode outside the lock using the owned persisted definition.
Both catalog lookups count toward `Reads`, including checks performed by inserts.
Missing tables require only the initial ID lookup. Schema mismatches and catalog
decode errors do not increment `ReadErrors`; underlying storage lookup failures do.

`PlanRowInsert` and the schema codecs remain pure, offline functions and do not
check the catalog. Creating a definition does not inspect existing rows or build
indexes over them. Use it for new table IDs; registering a definition for an ID
already used by uncataloged rows is not a supported migration path.

Raw `Put`/`PutBatch`, `Delete`/`DeleteBatch`, and scans still access the entire key space. Raw writes can
overwrite or remove catalog records or bypass table constraints. Existing files without
catalog records open normally but gain no inferred definitions; their raw entries
remain accessible, while typed row operations require registration. Database-wide
`Validate` checks the reachable storage tree, catalog references, rows, and secondary
indexes in both directions; see [validation](validation.md). Stop writers before
validating a file through a separate handle.

The two catalog writes are isolated from readers using the same handle and are
covered by write-group [WAL recovery](recovery.md). A failed or unacknowledged
create may be replayed on reopening; callers must check the catalog before retrying.
Exclusive advisory locking prevents competing cooperating openers. The catalog
remains local storage; Raft, server transport, and schema migrations are separate.

## Enumerating tables

`ListTables(afterName, limit)` returns owned definitions in ascending UTF-8 byte
order. An empty cursor starts at the first name; otherwise the cursor is exclusive.
Pass the last returned name to fetch the next page. Limits are 1 through
`MaxScanEntries` (1024). Each call holds one read lock across the name-range scan
and ID-reference validation. Corrupt or missing references fail the entire page.
Separate calls are separate local snapshots; callers needing a consistent multi-page
view must serialize catalog changes. The Raft server does this inside one event-loop
read callback after its quorum/applied-index barrier. This API neither scans user
rows nor creates missing catalog definitions.
