# Ordered index keys, version 1

`schema.EncodePrimaryKey(table, value)` validates the table and the supplied primary-key value, then produces a key for the existing byte-sorted B+tree. It takes a single value so lookups do not require constructing an entire row. Row insertion must separately validate and encode the row.

| Field | Bytes | Encoding |
| --- | --- | --- |
| Key format version | 1 | Currently 1 |
| Table ID | 4 | Big-endian |
| Index ID | 4 | Big-endian; 0 is reserved for the primary index |
| Key component | Variable | `record.EncodeKeyValue` |

Keys for different tables have different prefixes. Stable table IDs and index IDs identify namespaces; names and column positions are not encoded. Secondary index keys use nonzero index IDs within each table. These namespaces are a format convention; the low-level raw `Put` API does not enforce them.

## Ordered components

Each component starts with its record type tag. Ordering is defined between values of the same declared type; different types are not numerically coerced.

| Type | Payload |
| --- | --- |
| Int64 | Two's-complement bits with the sign bit flipped, then 8-byte big-endian |
| Uint64 | 8-byte big-endian |
| UUID | The UUID's 16 raw bytes, ordered lexicographically |
| Text, Bytes | Each zero byte becomes `00 ff`; the component ends with `00 00` |

Signed integer order becomes `MinInt64 < ... < -1 < 0 < ... < MaxInt64`. Text uses case-sensitive UTF-8 byte order, without locale collation, normalization, or case folding. Empty text and bytes are valid keys. The primary-key encoder and `record.EncodeKeyValue` reject null; secondary entries handle nullable columns as described below. Nil and empty byte slices encode identically.

Escaping and termination preserve byte ordering, including embedded zeros and prefix strings. Components can be concatenated unambiguously: tuples such as `(ab, c)` and `(a, bc)` remain distinct. A composite index compares a fixed sequence of declared column types. Descending components are not implemented.

These bytes differ from `record.EncodeValue`, which is a value-storage format and does not preserve numeric ordering. Key format versioning is separate from row format and database file versioning. The encoded component and complete key are bounded by `MaxUint32` and the platform's `MaxInt`; the pager still enforces its much smaller key and key-plus-value size limits on writes.

## Secondary-index definitions

`Table.Indexes` holds optional secondary-index definitions. Each `Index` has a stable nonzero `ID`, a nonempty UTF-8 `Name`, ordered `ColumnIDs`, and a `Unique` flag. ID 0 remains reserved for the primary index, which is declared separately through `PrimaryKeyColumnID`.

`ValidateIndexes(columns, indexes)` validates the columns and index definitions. `ValidateTable` includes this validation, so row and primary-key codecs also reject invalid index definitions.

Index IDs and names must be unique within their table; names use exact, case-sensitive comparison. Every index must reference at least one existing column, without repeating a column within that index. Column order is preserved: `(7, 42)` and `(42, 7)` describe different keys. Column renames or table column reordering do not change these references. Index IDs and names may be reused by a different table.

Indexed columns currently support Int64, Uint64, Text, Bytes, and UUID, matching the ordered component encoder. Other types remain valid in unindexed columns. Nullable columns are allowed in secondary-index definitions. `Unique` determines entry encoding and whether a uniqueness check is required; encoding alone does not enforce uniqueness or modify stored rows.

## Secondary-index entries

`schema.EncodeIndexEntry(table, indexID, row)` validates a complete row and selects the secondary index by its stable ID. It returns an `IndexEntry` containing `Key`, `Value`, and `RequiresUniqueCheck`. ID 0 is not accepted by this API. Errors return a zero entry, without partial results.

`schema.EncodeIndexEntries(table, row)` builds all secondary entries in `Table.Indexes` order after a single validation pass. It shares the primary-key encoding work while returning independent buffers for every entry. With no indexes it returns nil entries after validating the row. On error it returns nil entries, never a partial result.

The key starts with the same version/table/index header as the primary key. Indexed components follow in `Index.ColumnIDs` order. A null component is one byte, `00` (the null type tag), and sorts before any non-null component. Non-null components use the existing ordered value encoding, with no additional marker or length prefix. Their boundaries remain unambiguous for the declared index schema.

| Index and row | Key after header | RequiresUniqueCheck |
| --- | --- | --- |
| Non-unique | Indexed components, then ordered primary-key component | false |
| Unique, all indexed values non-null | Indexed components only | true |
| Unique, any indexed value null | Indexed components, then ordered primary-key component | false |

The primary-key suffix breaks ties between equal indexed values and allows multiple rows with a null-containing unique tuple. For example, a unique `(name, group)` index allows two rows with `(null, 1)` but requires conflict checking for two rows with `("Noah", 1)`. A null in any component has this effect, including the last component.

Every entry's `Value` is the full encoded primary key, including its table ID and primary index ID. A future lookup can pass it directly to the underlying database `Get`. Key and value buffers are independently owned; encoding does not change the row or schema. `RequiresUniqueCheck` is returned control metadata, not a byte stored in the key or value.

When `RequiresUniqueCheck` is true, the future serialized write path must reject an existing entry pointing to a different primary key. An existing entry for the same primary key can be treated as an update or replay. Blindly passing these entries to the current `Put` or `PutBatch` would overwrite conflicts, so it is not a uniqueness implementation.

Changing indexed column order, types, null rules, or the `Unique` setting requires rebuilding affected index entries; changing only a name does not change the encoded bytes. Schema persistence and index rebuilds are still pending.

## Secondary-index work still to implement

- Persistence of index definitions in the table catalog.
- Uniqueness checks inside the serialized write path and storage of generated entries.
- Additional collation choices and descending-order support.
- Index scans, backfill, and removal of stale entries on row updates or deletion.
- Atomic maintenance of row and index entries, including crash recovery. Current write batching does not provide this guarantee.

For future Raft replication, index entries must be derived deterministically from the committed row mutation and schema. Replicas must not independently choose index IDs or other schema identifiers. Ordered key encoding alone does not implement replication or sharding.
