# Row format, version 1

`schema.EncodeRow(table, row)` validates a complete row and returns its binary representation. `schema.DecodeRow(table, encoded)` returns a validated row with values owned by the caller. On error, both return a nil result.

All header integers use little-endian encoding.

| Field | Bytes | Meaning |
| --- | --- | --- |
| Version | 1 | Row format version, currently 1 |
| Table ID | 4 | Must match the supplied table |
| Column count | 4 | Must match the supplied table's column count |

The header is followed by one entry per column, in strictly increasing column ID order:

| Field | Bytes | Meaning |
| --- | --- | --- |
| Column ID | 4 | Nonzero stable column ID |
| Value length | 4 | Number of bytes in the encoded value, including its type tag |
| Value | Variable | Existing `record.EncodeValue` representation |

Every column must be present. Nullable columns use an explicitly supplied null value, whose encoded length is one byte. Unknown IDs, duplicate or unsorted IDs, invalid values, schema mismatches, truncated data, and trailing bytes are rejected.

Map insertion order, column display order, and table or column names do not affect encoded bytes. The value codec also sorts nested map keys. This does not normalize JSON text or decimal scale: their existing representations are preserved.

The total encoded row is limited to the smaller of `MaxUint32` and the platform's `MaxInt`. Length and count fields are checked against available bytes before slicing or allocating from them. Storage page limits remain separate and can be much smaller.

The version describes this binary format, not a schema revision or the database file format. Decoding requires a compatible table definition; schema migration and historical schema versions are not implemented yet. Table ID matching does not authenticate the bytes.

This is a row value format. It is not an order-preserving primary-key encoding, a durable write protocol, or a replication protocol.
