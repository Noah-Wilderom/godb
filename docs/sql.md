# First SQL execution layer

`pkg/sql` implements a small, strict SQL dialect directly over the existing catalog,
typed rows, primary index, and encoded command format. It is usable from Go on a
standalone database, or through the server’s Go API on a Raft leader. The opt-in
[MySQL endpoint](mysql.md) supports Go/PDO round trips and session setup; broader MySQL client
compatibility is still pending.

## Supported statements

Execute each statement separately:

```sql
CREATE TABLE users (
    id BIGINT PRIMARY KEY,
    name TEXT NOT NULL,
    active BOOLEAN,
    note TEXT
);

INSERT INTO users (id, name, active) VALUES (1, 'Noah', TRUE);

SELECT id, name, active FROM users WHERE id = 1;
```

| Operation | Current scope |
| --- | --- |
| CREATE TABLE | BIGINT, BIGINT UNSIGNED, TEXT, BOOLEAN/BOOL; NULL/NOT NULL; one primary key, inline or `PRIMARY KEY (column)`; inline/table UNIQUE and secondary INDEX/KEY declarations |
| INSERT | One row, explicit column list, literals or `?` parameters; omitted nullable columns become NULL |
| UPDATE | Partial assignments of literals/parameters; one required primary-key equality predicate; primary key cannot change |
| DELETE | One required primary-key equality predicate; atomically removes the row and its indexes |
| SELECT | `*`, named projections, or one COUNT expression, optional WHERE with up to 64 comparison or IS [NOT] NULL predicates joined by AND; primary/unique lookup, primary/secondary-index range, composite-index prefix, or bounded table scan, plus row filtering; optional ORDER BY and LIMIT/OFFSET; at most 1,024 candidate rows |
| EXPLAIN SELECT | One plan-description row for a supported user-table SELECT, including COUNT; validates and binds without reading user rows |

Keywords are case-insensitive. Table and column names are case-sensitive, matching
the current catalog. Unquoted names use ASCII letters/underscore followed by
letters, digits, or underscores; backticks allow other UTF-8 names and doubled
backticks escape a backtick. Identifiers contain 1–64 bytes. Single-quoted strings
support doubled quotes (`'it''s valid'`). Backslash escapes, double-quoted strings,
comments, multiple statements, and trailing unsupported clauses are rejected.
One optional final semicolon is accepted.

Integers are checked against their signed/unsigned 64-bit range. Boolean columns
accept TRUE/FALSE and integer 0/1. Other implicit conversions, including numeric
strings, are rejected. Primary keys cannot be nullable; an omitted NULL constraint
is implicitly made NOT NULL for a primary key. An equality predicate comparing any column
to NULL returns no rows; IS NULL explicitly matches SQL NULL values. Missing rows still return result-column metadata.

This is an initial MySQL-oriented dialect, not MySQL compatibility. For example,
TEXT primary keys use GoDB's existing bytewise index semantics, booleans are strict,
and identifier/collation behavior is not MySQL's full behavior. The broader targets
are described by MySQL's [CREATE TABLE](https://dev.mysql.com/doc/refman/8.4/en/create-table.html)
and [SELECT](https://dev.mysql.com/doc/refman/8.4/en/select.html) documentation; syntax
outside the supported subset fails instead of being ignored.

## Explaining a SELECT

```sql
EXPLAIN SELECT id FROM scores
WHERE tenant = 7 AND score >= 50 AND score < 100
ORDER BY score LIMIT 20;
```

EXPLAIN uses the same SELECT planner, index preferences, parameter binding, and
validation as execution. It returns exactly one description row, even when the
SELECT has LIMIT 0, a positive OFFSET, or an impossible NULL predicate. It does
not read user rows, calculate a count, sort results, or apply a command. Planning
still reads the catalog. A query that would exceed the candidate cap can be
explained successfully; no actual cardinality or cost is estimated.

The result schema is specific to GoDB, rather than MySQL's EXPLAIN column layout:

| Column | Meaning |
| --- | --- |
| table | Resolved user table |
| access | primary, unique, primary_range, index_range, index_scan, or table_scan |
| index | PRIMARY or the selected secondary-index name; NULL for a table scan |
| equality_prefix_columns | Number of leading equality/IS NULL key components; zero for an unprefixed range |
| range_column | Column with planned ordered bounds; NULL for other access paths |
| lower_bound / upper_bound | inclusive, exclusive, or unbounded; NULL when there is no ordered range |
| candidate_limit | SELECT candidate cap: 1 for a point lookup, 1,024 for scans/ranges, 0 for a known NULL no-match shortcut |
| result_limit / result_offset | SELECT's bound result window (default 1,024 / 0); does not limit the EXPLAIN row |
| extra | Predicate rechecking, sorting, COUNT, and known NULL no-match information |

Bound fields describe the endpoint rules after intersection, without returning
literal or parameter values. `candidate_limit` is a cap, not a row estimate or
physical I/O count: scan execution probes one extra entry to detect overflow.
LIMIT does not reduce this cap. Empty or contradictory ranges can still show
1,024 because the planner does not use cardinality estimates. `extra` describes
planned work, which a NULL shortcut may skip. Sorting refers to captured rows;
there is no reverse traversal or early LIMIT optimization.

Go callers use `Prepare`/`Plan.Read` or `LocalEngine.Execute`; an explained plan
can be read without a RowReader. It describes the catalog snapshot used during
preparation. Server PrepareSQL and ExecuteSQL retain their quorum/application
barriers, including for plans that describe no matching rows. No Raft command is
appended. MySQL text queries and native prepared statements work through Go and
PHP PDO, with stable result metadata and normal typed parameter validation. The
wire handler replans on execution using the supplied values.

Only user-table SELECT is supported. EXPLAIN ANALYZE, FORMAT options, mutations,
nested EXPLAIN, session queries, and INFORMATION_SCHEMA plans are unsupported.
EXPLAIN is a reserved word; quote an identifier named `explain` with backticks.

Metrics use `statement="explain"` in the existing SQL duration histogram and its
execution count. The plan row contributes one returned row. SELECT access-path
and aggregate execution counters are unchanged, even for EXPLAIN SELECT COUNT(*).
The existing SQL spans and server read-barrier metrics cover EXPLAIN. SQL text,
index/table names, and bound values are never added to telemetry labels.

## Indexes declared at table creation

```sql
CREATE TABLE accounts (
    id BIGINT PRIMARY KEY,
    handle TEXT,
    tenant BIGINT NOT NULL,
    token TEXT UNIQUE,
    CONSTRAINT tenant_handle UNIQUE (tenant, handle),
    INDEX handle_lookup (handle)
);
```

Supported forms are column-level `UNIQUE [KEY]`, table-level
`UNIQUE [KEY | INDEX] [name] (columns...)`, `[KEY | INDEX] [name] (columns...)`,
and `CONSTRAINT name UNIQUE [KEY | INDEX] (columns...)`. A named CONSTRAINT cannot
also provide a separate index name. Column references can appear before their
column definitions in the CREATE statement. The primary key remains one column;
secondary indexes may be composite and preserve the declared column order.

SQL indexable types currently are BIGINT, BIGINT UNSIGNED, and TEXT. BOOLEAN indexes
are rejected because the storage key codec does not support them yet. TEXT uses the
whole value and GoDB's bytewise semantics, without MySQL TEXT prefix requirements or
collation rules. Prefix lengths, expressions, ASC/DESC options, USING clauses,
visibility options, foreign keys, CHECK, and index options are rejected. Standalone
CREATE INDEX, ALTER TABLE, DROP INDEX, and building an index over an existing table
remain unsupported.

Explicit index names are checked case-insensitively within the new table;
`PRIMARY` is reserved. These SQL checks do not rewrite existing Go-API-created
catalogs. Table/column name resolution remains case-sensitive. Unnamed indexes use
the first indexed column name, with `_2`, `_3`, etc. on collisions. Explicit names
are reserved first, including those declared later. Generated names are shortened
at UTF-8 boundaries when a suffix would exceed 64 bytes. Index IDs start at one in
declaration order; zero remains the implicit primary index. This resolution is
deterministic and happens before command encoding.

At most 63 secondary indexes are allowed, so inserting one row stays within the
64-entry primary-plus-index mutation bound. The existing catalog page-entry limit
may reject a smaller definition. Each index has at most 64 distinct named columns;
unknown or repeated columns fail preparation. Index keys and row values must still
fit the pager entry limits on insert. Values are never silently truncated.

CREATE stores the complete table definition in the existing atomic catalog command.
Preparation, including native COM_STMT_PREPARE, validates without writing a table
or allocating a table ID. Replicas apply the encoded schema rather than regenerating
names or IDs. There is no catalog or file-format version change.

INSERT maintains all declared indexes and enforces primary/unique conflicts before
writing the row. NULL-containing unique keys retain the storage engine's existing
semantics and can occur more than once. Ordinary secondary indexes permit duplicates.
SHOW INDEX and INFORMATION_SCHEMA immediately expose successful declarations.

Tests cover native/text creation, generated names and bounds, atomic failures,
unique/nullable behavior, composite key order, nonunique index reads, reopen,
replication, and enforcement after leader changes.

## Indexed equality reads

```sql
SELECT id, handle FROM accounts WHERE token = 'unique-token';
SELECT id, tenant FROM accounts WHERE handle = 'alice';
SELECT id, handle FROM accounts WHERE tenant = 7;
```

WHERE accepts up to 64 comparison or NULL-test predicates joined by AND:

```sql
SELECT id, handle FROM accounts WHERE tenant = 7 AND handle = 'alice';
SELECT id FROM accounts WHERE handle = ? AND tenant = ?;
SELECT id FROM accounts WHERE tenant = 7 AND token = 'a';
```

Predicates may appear in any order. Parameters bind in SQL order; lookup values
are arranged into the declared index-column order. An index lookup requires a
predicate that constrains the primary key or the first column of a secondary index. An index on
`(tenant, handle)` supports `tenant = 7` and `handle = 'alice' AND tenant = 7`,
but `handle = 'alice'` alone uses another usable index or falls back to a table scan.

Selection is deterministic: prefer the primary key, then a unique index whose
entire key is constrained to non-NULL values. Next prefer a primary-key range,
then a secondary-index prefix or range. Prefer more constrained leading columns;
equality prefixes win a tie with ranges. Remaining ties prefer fewer total index
columns, then the lowest index ID. A range constrains one column after any equality prefix. Complete non-NULL unique keys use direct lookups; other paths
read an index range. If no usable index exists, SELECT scans the table primary-key
range. There is no cost-based optimizer or index intersection. A missing middle
column stops prefix construction even if a later column is constrained.

Every predicate is checked against the captured rows, including conditions not
used by the selected index. This permits filters on unindexed columns, including
BOOLEAN, using an index when possible and a bounded table scan otherwise. Repeated
predicates are allowed and all must hold; `id = 1 AND id = 2` returns no rows. Binding and column
validation still covers the complete statement. Supported comparison types are the
SQL types BIGINT, BIGINT UNSIGNED, TEXT, and BOOLEAN; Go-created BYTES/UUID columns
also support typed comparisons through the Go API. Other native types are rejected
for comparisons; NULL tests only inspect nullness and support all stored column types.

`MaxSelectRows` bounds the candidate rows read from the selected access path to 1,024,
**before filtering**. The scan checks one extra entry; exceeding the bound returns
`ErrLimit` with no partial result (MySQL error 1105), even when filters would discard
all candidates. There is no implicit truncation or SQL cursor. Narrowing a composite
prefix can avoid this limit. All entries and their primary rows are read under one
storage read lock, then decoded, filtered, and projected from those captured bytes.
Server execution remains behind the Raft quorum/application barrier. No read-side
Raft command is appended. The wire endpoint also retains its separate 4 MiB result
payload limit.

Both text queries and native prepared statements support these lookups. Empty
results retain projection metadata. Any `column = NULL` predicate yields no rows.
TEXT equality uses bytewise semantics, without locale-aware or case-insensitive
comparison. Queries without ORDER BY currently follow index order; applications
should not depend on this implicit order.

OR, parentheses, and arbitrary expressions remain
outside user-table SELECT syntax. The separate INFORMATION_SCHEMA
grammar retains its own capabilities and limits.

## Ordered comparisons

```sql
SELECT id FROM users WHERE id >= ? AND id < ? ORDER BY id;
SELECT COUNT(*) FROM accounts WHERE tenant = ? AND id > ?;
SELECT id FROM users WHERE name IS NOT NULL AND name < 'm' LIMIT 20;
```

SELECT and COUNT accept `column < value`, `column <= value`, `column > value`, and
`column >= value`. The right side is a literal or a typed placeholder. Combine
these predicates with equality and IS [NOT] NULL using AND, up to the existing
64-predicate limit. The two characters in <= and >= must be adjacent. Column-to-column
comparisons, expressions, OR, BETWEEN, IN, `<>`, `!=`, and `<=>` remain unsupported.
UPDATE/DELETE retain their single primary-key equality requirement.

INT64 and UINT64 comparisons use their full integer precision, including values
above 2^53 and their respective endpoints. Existing checked parameter conversion
still applies: out-of-range signed/unsigned values and numeric strings are rejected.
BOOLEAN orders false before true; integer 0/1 can bind to it under the existing
rules. TEXT compares by bytes, including case and trailing spaces, rather than
locale-aware MySQL collation. Native Go-created BYTES and UUID columns compare by
bytes; other storage types remain unsupported for ordered comparison in SQL.

NULL on either side produces no WHERE match. A NULL bound produces an empty SELECT
or COUNT zero after complete validation, still behind the server read barrier.
This uses the conventional meanings of the four
[SQL comparison operators](https://dev.mysql.com/doc/refman/8.4/en/comparison-operators.html)
while retaining GoDB's strict type and bytewise-text rules.

Primary-key comparisons now produce a bounded primary-index range when no point
lookup takes precedence. Secondary indexes support equality/IS NULL prefixes
followed by a range on the next column. Other comparisons remain residual row filters.

## Primary-key range scans

```sql
SELECT id FROM users WHERE id > 1000 AND id <= 1020 ORDER BY id;
SELECT COUNT(*) FROM users WHERE id >= ? AND id < ?;
```

The planner intersects all non-NULL `<`, `<=`, `>`, and `>=` bounds on the primary
key. Repeated lower bounds choose the greatest value, repeated upper bounds choose
the least, and an exclusive bound wins a tie. Contradictory bounds and equal
endpoints with an exclusive side yield an empty range. COUNT returns zero unless
LIMIT/OFFSET suppresses the result. Every predicate is still checked against the
captured rows, including those used to select the range.

Selection order is primary-key equality, a complete non-NULL unique lookup,
primary-key range, secondary-index prefix or range, then table scan. This is deterministic,
without cost statistics or index intersection. A wide primary range may exceed the
candidate cap even when another index range would be smaller.

The B+ tree seeks directly to the lower bound and stops at the upper bound.
The 1,024-candidate cap now applies to rows **inside the selected range**, before
other filters, COUNT, ordering, or LIMIT. A narrow range can succeed on a larger
table; a range containing 1,025 rows fails with MySQL 1105 even with LIMIT 1 or
LIMIT 0. Non-primary residual filters do not reduce this candidate budget.

`ScanRowsRangePage(table, lower, upper, limit, cursor)` exposes the same storage
read to Go callers. Bounds are `*database.KeyBound{Value, Inclusive}`; nil means
unbounded on that side. Values must match the primary-key type and cannot be NULL.
The method maps endpoints into the existing half-open encoded key space, without
incrementing or decrementing numeric values. Integer endpoints, UUIDs, empty text,
embedded NUL bytes, and keys sharing a text/byte prefix preserve their ordering.
Empty/reversed ranges still validate the limit, schema, and database state.

Cursors bind to the database handle, encoded range, and row schema. Changing bounds
or reopening the database invalidates them; equivalent encoded bounds can share a
cursor. Separate pages observe separate snapshots. SQL reads one bounded page and
does not expose the cursor. DESC still sorts captured rows rather than traversing
the tree backwards; SQL LIMIT does not stop candidate collection early.

Schema validation and row-byte capture use one read lock. Counts and residual
filters use those captured bytes. Raft quorum/application barriers apply to all
server executions, including empty ranges, NULL bounds, and LIMIT 0. No command,
page, header, or row format changes are introduced. Custom SQL RowReader
implementations must add ScanRowsRangePage. Metrics report `access="primary_range"`;
existing returned-row and aggregate counters retain their meaning.

## Secondary-index range scans

```sql
CREATE TABLE scores(id BIGINT PRIMARY KEY, tenant BIGINT, score BIGINT,
                    INDEX by_score(tenant, score));
SELECT id FROM scores WHERE tenant = 7 AND score >= 50 AND score < 100;
SELECT COUNT(*) FROM scores WHERE tenant IS NULL AND score <= 50;
```

An index can bind consecutive leading columns using equality or IS NULL, then
apply intersected ordered bounds to the next column. A range can also constrain
the first column with no fixed prefix. Repeated bounds use the same tightest-bound
rules as primary ranges. A missing leading column prevents using a later range;
columns after the ranged column remain residual filters. All predicates are
rechecked against captured rows. NULL in the ranged column never matches, while
NULL in a fixed prefix is supported for nullable columns.

Primary equality, complete non-NULL unique lookups, and primary ranges retain
priority. Among secondary paths, more constrained leading columns win, counting
one for the range. An equality-only prefix wins an equal-length tie against a
range. Among ranges, prefer the longest fixed prefix, fewer index columns, then
lowest index ID. These are deterministic rules without selectivity statistics;
a chosen path can exceed the cap even if another index would have fewer candidates.

`ScanRowsByIndexRangePage(table, indexID, lower, upper, limit, cursor, values...)`
exposes this read to Go callers. `values` binds the leading prefix and must leave
at least one index column for the range. Bounds use `KeyBound`; nil means unbounded,
and non-nil values must match the next column's type and cannot be NULL. Even with
both bounds nil, the ranged column excludes NULL. Existing prefix scans still
include NULL values. Custom SQL `RowReader` implementations must add this method.

Boundaries include or exclude **every** entry with the boundary value, including
ordinary-index duplicates, NULL-containing unique tuples, and trailing composite
components. The storage layer uses the successor of an encoded component prefix
for exclusive lower and inclusive upper bounds. It does not increment numeric
values or confuse text `a` with `ab`. Prefix scope keeps scans inside the selected
tenant/index/table. No storage or replication format change is required.

The 1,024-candidate cap applies inside the selected range before residual filtering,
COUNT, ORDER BY, or LIMIT. A narrow range succeeds on larger tables. Index references
and primary rows are captured under one read lock and validated before returning;
corruption fails without partial rows. Cursors bind to the handle, encoded bounds,
and schema; pages do not share a snapshot. SQL does not expose these cursors.
Server reads retain the quorum/application barrier, including empty ranges.
Metrics report `access="index_range"`. DESC still sorts captured rows, and LIMIT
does not stop collection early.

## NULL predicates

```sql
SELECT id FROM users WHERE note IS NULL;
SELECT id FROM users WHERE note IS NOT NULL AND active = ?;
SELECT COUNT(*) AS aggregate FROM accounts WHERE tenant = ? AND handle IS NULL;
```

SELECT supports `column IS NULL` and `column IS NOT NULL`, combined with comparison
predicates through AND. They consume no parameters: `IS ?`, `IS NOT ?`, IS TRUE,
IS FALSE, and other IS expressions are rejected. Parameters in surrounding comparison
filters and LIMIT/OFFSET retain SQL order. UPDATE and DELETE still require their
single primary-key equality predicate; this does not broaden their mutation scope.

IS NULL matches the SQL NULL value; IS NOT NULL matches values including false,
zero, and empty strings. A JSON value containing JSON null is also a non-NULL SQL
value. Equality to NULL still never matches. These distinctions follow
[MySQL's NULL rules](https://dev.mysql.com/doc/refman/8.4/en/working-with-null.html).
NULL tests work with ordinary SELECT and COUNT. COUNT(column) over `column IS NULL`
returns zero even when COUNT(*) would count matching rows.

IS NULL can constrain a leading nullable secondary-index column, including a
component within a composite prefix. A unique index key containing NULL uses a
range scan because multiple rows may share that NULL-containing key. Fully
constrained non-NULL unique keys retain priority as point lookups. IS NOT NULL
is currently a row filter: use another equality/IS NULL prefix when available,
otherwise scan the table. No non-NULL range optimization is implemented yet.

IS NULL on a declared NOT NULL column, including the primary key, produces no
matches after complete column/type/binding validation. COUNT then returns zero.
Server execution still passes the Raft quorum/application barrier. Contradictory
nullable filters are evaluated against the captured candidates; no general
contradiction optimizer is introduced. The 1,024-candidate cap still applies before
filtering, counting, sorting, and LIMIT. Duplicate NULLs in a unique index count
toward that cap. Unsupported syntax fails without executing a partial query.
No storage format, command encoding, or new index implementation is required.

## Bounded table scans

```sql
SELECT * FROM accounts;
SELECT id, handle FROM accounts ORDER BY handle, id LIMIT 20;
SELECT id FROM users WHERE active = TRUE ORDER BY id;
```

WHERE is optional for SELECT. When no predicate can use the primary key or a
leading secondary-index column, the planner falls back to a primary-row scan.
The last example uses the unindexed BOOLEAN column from the initial users table.
Index selection keeps the existing deterministic preferences whenever a usable index exists.

The storage method `ScanRowsPage(table, limit, cursor)` reads only the table's
primary-key range. Catalog entries, other tables, and secondary index entries are
excluded. It checks the persisted schema and captures owned row bytes under one
read lock; decoding, filtering, ordering, and result slicing then use those captured
bytes. Every decoded row's primary key must agree with its stored key. Corrupt rows
fail the read without returning partial results. This does not validate every
secondary index; use database validation for that.

SQL asks for at most 1,024 primary rows and checks for one extra row. Exceeding this
candidate limit returns `ErrLimit` (MySQL 1105) before emitting results, even with
LIMIT 0, LIMIT 1, or a filter that would remove every candidate. LIMIT is result
slicing, not a scan-work override. Equality to NULL retains the existing no-match
short circuit after complete statement validation. All server reads still pass the
Raft quorum/application barrier, including LIMIT 0 and NULL predicates.

The storage cursor is scoped to the database handle, table range, and row schema;
it resumes after the last key. Separate calls see separate snapshots. SQL currently
uses a single page and exposes no cursor, so it cannot traverse tables larger than
this bound through fallback scans. Explicit ORDER BY remains necessary for a
promised result order. Scanning adds no persisted format or replication command.
Custom implementations of the exported SQL `RowReader` interface must implement
`ScanRowsPage` and `ScanRowsRangePage` as well as the existing indexed read methods.

## Ordering and bounded result pages

```sql
SELECT id, handle FROM accounts
WHERE tenant = ?
ORDER BY handle ASC, id DESC
LIMIT ? OFFSET ?;

SELECT id FROM accounts WHERE tenant = 7 ORDER BY id LIMIT 10, 20;
```

ORDER BY accepts up to 64 named columns, each optionally followed by ASC (default)
or DESC. Sort columns need not appear in the projection or have an index. Numeric
values sort numerically, BOOLEAN sorts false before true, and TEXT/BYTES/UUID use
bytewise key ordering. NULL sorts first ascending and last descending, following
[MySQL's NULL ordering](https://dev.mysql.com/doc/refman/8.4/en/working-with-null.html).
The supported column types match comparison predicates; other native types fail
at preparation. Repeated sort columns are redundant after their first occurrence.
Positional references such as ORDER BY 1, expressions, aliases, direction parameters,
and explicit NULLS FIRST/LAST are not supported.

Supported limit forms are `LIMIT count`, `LIMIT count OFFSET offset`, and
`LIMIT offset, count`, consistent with the corresponding forms in
[MySQL SELECT](https://dev.mysql.com/doc/refman/8.4/en/select.html). Each count and
offset must be 0–1,024. Literals must be unsigned decimal integers; placeholders
accept nonnegative INT64 or UINT64 values. NULL, strings, booleans, floating-point
values, and negative values fail binding. Values exceeding the bound return
`ErrLimit`. OFFSET requires LIMIT. Native PREPARE describes these placeholders as
nonnullable BIGINT UNSIGNED, in their original SQL order.

Execution reads one bounded snapshot, filters all predicates, sorts the remaining
rows in memory, then applies OFFSET/LIMIT before projection. LIMIT 0 and offsets
past the result return no rows with normal column metadata. **LIMIT never bypasses
the 1,024-candidate scan bound**, even for LIMIT 0 or LIMIT 1. A query exceeding
that bound fails before emitting any result rows. No early scan termination or
index-assisted sorting is implemented yet.

The sort is stable within a read, but equal sort keys retain the chosen index's
order. Include a unique tie-breaker, such as ORDER BY handle, id, for predictable
pages. Separate page queries observe separate snapshots; concurrent writes can
shift offsets. This is bounded result slicing, not a persistent cursor or a way
to traverse result sets larger than the candidate bound. Raft barriers still apply
to every execution, including LIMIT 0.

## Counting matching rows

```sql
SELECT COUNT(*) AS aggregate FROM users;
SELECT COUNT(note) AS notes FROM users WHERE active = TRUE;
SELECT COUNT(*) FROM accounts WHERE tenant = ?;
```

COUNT accepts either `*` or one named column, with an optional `AS alias`. The
result is a nonnullable signed BIGINT. Without an alias the result-column name is
`COUNT(*)` or `COUNT(column)`. Aliases follow the existing identifier rules and may
use backticks. Only one COUNT expression is allowed in the projection; mixed
columns/counts, multiple counts, DISTINCT, COUNT(1), arbitrary expressions,
GROUP BY, HAVING, and window functions are not supported. ORDER BY with COUNT
is rejected in this initial subset.

COUNT(*) counts all rows that pass WHERE. COUNT(column) counts matching rows whose
column value is not NULL; empty strings, zero, and false still count. Empty tables,
missing matches, all-NULL counted columns, and NULL equality predicates produce
one row containing zero. These semantics follow
[MySQL's COUNT definition](https://dev.mysql.com/doc/refman/8.4/en/aggregate-functions.html).
Column and parameter validation still happens before any no-match shortcut.

LIMIT/OFFSET applies to the single aggregate result row, after counting. LIMIT 1
returns the complete count; LIMIT 0 or OFFSET 1 suppresses that result row while
retaining column metadata. Placeholder types and bounds match ordinary SELECT.
Native PREPARE describes the BIGINT result and filter/limit parameters without
reading rows or calculating a count. Execution uses the current captured rows.

COUNT reuses the normal primary/unique/index-scan/table-scan selection and one-read
snapshot. The 1,024-candidate limit applies before filtering and counting, even if
LIMIT would hide the aggregate row. Exceeding it fails with no partial count
(MySQL 1105). The existing equality-to-NULL shortcut returns zero after validation;
server execution still requires a Raft quorum, including NULL predicates and LIMIT 0.
No stored row counters, cached totals, index-only counting, command changes, or
file-format changes are introduced. Counting a large table remains unsupported
until scan traversal and resource limits are extended.

Counts use the existing SELECT latency, access-path, and read-barrier metrics.
`godb_sql_aggregate_total{function="count",outcome}` adds aggregate execution counts.
Returned-row metrics count the aggregate result row, not the number inside it:
a returned zero contributes one row, while a suppressed result contributes none.
INFORMATION_SCHEMA retains its separate metadata-count grammar and limits.

## Updating a row

```sql
UPDATE accounts SET handle = ?, tenant = ? WHERE id = ?;
UPDATE accounts SET handle = NULL WHERE id = 1;
```

UPDATE accepts 1–64 distinct assignments and exactly one required
`WHERE primary_key = literal_or_parameter` predicate. Assignment values use the
existing strict type/nullability checks. SET cannot change the primary key, and
unknown or repeated assignment columns are rejected. Expressions, column-to-column
assignments, DEFAULT, extra predicates, secondary-index predicates, multirow updates,
ORDER BY, and LIMIT are not supported for UPDATE yet. `WHERE primary_key = NULL`
is a successful no-op after validating the entire statement.

Preparation validates the catalog and encodes the key and assigned values into
`CommandPatchRow`. It does not read or copy the current row. On application, the
writer merges those values into the current stored row and updates its primary and
secondary entries in one atomic command. Independent updates to different columns
therefore preserve each other's changes, including plans prepared before either
write commits. Assignments to the same column follow committed command order.
Unique conflicts reject the complete update. A resulting row/key that exceeds
storage constraints returns `invalid_update`; the handle remains usable.

`AffectedRows` counts changed rows; `MatchedRows` counts existing rows selected by
the primary key. A changed row reports 1/1, an unchanged match 0/1, and a missing
row or NULL predicate 0/0. These are commit-time results, not preparation estimates.
Replicated request records persist both counts, so retries with the same immutable
plan and request ID return the original counts without reapplying an old assignment
over newer data. Native PREPARE describes assignment/key parameter types without
performing a write. Every EXECUTE is a new operation unless the Go server caller
explicitly supplies the original request ID and plan.

The MySQL endpoint returns changed rows by default and matched rows when the
connection negotiates `CLIENT_FOUND_ROWS`, matching the corresponding
[MySQL affected-row behavior](https://dev.mysql.com/doc/c-api/8.4/en/mysql-affected-rows.html).
Both text and native prepared UPDATEs use this convention. The SQL metrics always
count actual changed rows separately from UPDATE matches, regardless of this flag.

## Deleting a row

```sql
DELETE FROM accounts WHERE id = ?;
```

DELETE requires exactly one primary-key equality predicate. Literals and typed
parameters are accepted; unknown columns and invalid key types fail preparation.
Missing rows and `WHERE id = NULL` succeed with zero counts. Deleting an existing
row returns `AffectedRows = 1` and `MatchedRows = 1`, including on a connection
using CLIENT_FOUND_ROWS. Native PREPARE only describes and validates the statement.
Unqualified deletes, secondary-index predicates, extra predicates, ORDER BY, LIMIT,
RETURNING, and multirow deletes are not supported yet.

The writer selects the current row at command application and removes its primary
entry and every expected secondary index entry atomically. Unique values become
available for reuse. Preparation never captures stale index keys. The counted
command and its outcome share the same WAL commit. Retrying the original immutable
plan and request ID returns the saved count, even after leader changes, without
deleting a replacement inserted later under the same primary key. A fresh request
ID is a new deletion attempt. Native MySQL EXECUTE creates a new request each time.

SQL uses additive command kind `08` (`CommandDeleteRowCounted`); existing logged
`CommandDeleteRow` operations retain their original zero-count result behavior.
Upgrade every replica before submitting these commands. This uses the existing
version-2 saved-result metadata for nonzero counts; header and row versions stay
unchanged. Storage deletion still does not reclaim pages or shrink files.

## Using it from Go

Import `github.com/Noah-Wilderom/godb/pkg/sql` (for example as `sqlengine`). Given an
open standalone `*database.Database`, call `sqlengine.NewLocal(db)`, then
`engine.Execute(ctx, statement, args...)`. The engine borrows the handle; its owner
closes it. Use one LocalEngine per database handle. It is safe for concurrent calls;
local table-ID selection and creation are serialized within that engine.

Parameters are `record.Value` values passed separately from SQL text:

```go
result, err := engine.Execute(ctx,
    "SELECT name FROM users WHERE id = ?",
    record.NewInt64(1),
)
```

Do not interpolate user input into a statement. The parameter count must match
exactly; parameter data is never parsed as SQL. Typed values must match the column
type, except checked signed/unsigned conversion and integer 0/1 to boolean.

`Result.Columns` contains projection metadata in result order, and `Result.Rows`
contains values in that same order. INSERT reports one affected/matched row; CREATE reports zero. UPDATE reports
commit-time changed/matched counts as described above. DELETE reports removed rows in both fields. Deterministic write rejections return `*sql.CommandError` with the existing
database command result code and support `errors.Is(err, sql.ErrRejected)`.
Parser/binding/type/limit errors have separate sentinels; byte offsets identify
syntax failures. The MySQL endpoint maps duplicate/invalid index names to error
1061 and duplicate primary/unique values to 1062.

The local engine selects unused table IDs by checking the existing catalog from
an in-memory candidate. Reopening starts the search again, so persisted IDs remain
reserved. This helper is not a replicated allocator. Concurrent catalog writers
outside the engine can cause a checked ID/name rejection, and uncataloged raw rows
are not a supported source for automatic schema creation.

Context cancellation is checked before work, during ID search, and before storage
execution. It does not interrupt a storage operation already executing or a mutex
wait; a write that completes successfully is reported as successful. Limits are
64 KiB of SQL text, 4096 tokens, and 64 columns/list items. Existing page-entry and
command-size limits still apply and may reject a much smaller schema or row.

## Parser, plan, execution, and replication

1. `Parse` tokenizes the entire statement and constructs a bounded syntax tree.
2. `Prepare` resolves the catalog, checks names/types/parameters, and builds a plan.
3. A write plan's `Command()` returns an owned copy of existing GODBCMD1 command
   bytes. A read plan's `Read(RowReader)` executes its selected lookup or scan.

`PrepareOptions.CreateTableID` is required for preparing CREATE TABLE directly.
It assigns the table ID before encoding. Preparation does not mutate the database;
ID/name uniqueness and schema checks are still enforced when commands are applied.
Tests replay prepared CREATE/INSERT commands through `ApplyLogEntry` into two
databases and verify identical rows. SQL parsing does not run during log replay.

## SQL through a Raft server

Use the owning server handle rather than opening its database files separately:

```go
plan, err := member.PrepareSQL(ctx,
    "INSERT INTO users (id, name) VALUES (?, ?)",
    record.NewInt64(7), nameValue,
)
// Handle err before submitting. Keep this plan and a fresh nonzero request ID.
result, err := member.ExecuteSQL(ctx, requestID, plan)
```

`PrepareSQL` parses the statement, obtains a quorum-confirmed catalog view, binds
parameters, and returns an immutable `*server.SQLPlan`. `ExecuteSQL` sends write
commands through Raft and waits for their local committed application. SELECT
performs a new quorum check and applied-index barrier before the row lookup. Both
methods reject followers with `replication.ErrNotLeader`; there is no automatic
routing or forwarding yet. Use bounded contexts. The SELECT request ID is ignored.

A read uses [etcd Raft's ReadIndex](https://pkg.go.dev/go.etcd.io/raft/v3#RawNode.ReadIndex)
in safe/quorum mode. It must receive a matching read state and reach its applied
index before executing local work. A stale leader cannot answer a new SQL read
without quorum confirmation. Leadership/term changes reject outstanding reads.
Catalog preparation and reads execute on the server event loop, keeping storage
ownership and shutdown ordered; this initial path is intended for bounded point
queries. Large scans will need a separate snapshot/execution design.

Retain **the same plan and request ID** after an ambiguous timeout, shutdown, or
leader change. The plan can be passed to another server in the same group. Its
`Command()` method returns an owned copy of the exact bytes for durable caller-side
retry storage with the request ID; these bytes can also be submitted with `Propose`.
Repreparing a CREATE can assign a different ID, so it is not a safe retry. SQL plans
are Go objects, not MySQL protocol prepared-statement handles. A successful retry
reports the original command outcome, not an additional mutation. New intended
operations need new request IDs.

CREATE preparation serially selects an unused uint32 table ID from the leader's
catalog, advancing an in-memory candidate. This avoids duplicate IDs among plans
prepared on that server instance. The actual table and ID become durable only when
its CREATE command commits. Startup/new leaders check the committed catalog again.
A scan checks at most 128 candidates per preparation; `ErrBusy` asks the caller to
retry preparation while the cursor advances. Invalid/abandoned plans can leave gaps.

This is **checked allocation, not a durable reservation service**. An old unsubmitted
plan can collide with a table created by another leader or by a caller using raw
`Propose`. Application atomically returns `table_id_exists` or `table_name_exists`
instead of overwriting data. After a definite rejection, a new intended attempt may
prepare afresh and use a new request ID; never do so merely because an outcome is
unknown. Durable reservations and a serialized retry token format remain follow-up
work for the remote client API.

Read admission has a queue of 32 and at most 128 outstanding barriers. Canceled
barriers retain their slot until acknowledged or leadership changes: repeatedly
canceling callers cannot grow Raft's internal pending-read state without bound.
Cancellation stops waiting; it cannot interrupt an executing storage read or undo
a proposed write. SQL preparation is not counted as statement execution.

`NewLocal` still rejects a Raft-bound database. Never use a local engine concurrently
with binding its handle to replication. `Plan.Read` alone supplies no quorum barrier;
only the server path above does. This step adds no storage format changes.

## Metrics and validation

`LocalEngine.Metrics()` returns a private Prometheus collector for standalone use.
Servers automatically register their SQL collectors with the existing metrics
endpoint, group/node labels, and private registry.

- `godb_sql_statement_duration_seconds{statement,outcome}` measures the complete
  local Execute call, or the server ExecuteSQL call (excluding preparation). Statement labels are create_table, insert, update, delete, select, explain, or unknown;
  outcomes are ok, error, or canceled. Histogram counts also give execution counts.
- `godb_sql_aggregate_total{function,outcome}` counts prepared aggregate SELECT
  executions (currently function=count), including failures and suppressed results.
- `godb_sql_select_total{access,outcome}` counts prepared SELECT executions by
  primary, unique, primary_range, index_range, index_scan, or table_scan access path and ok/error/canceled outcome.
  It includes NULL short circuits and, on servers, quorum failures; it is not a
  physical page-scan counter. Preparation failures are excluded.
- `godb_sql_rows_deleted_total` counts removed rows in successful DELETE responses,
  including deduplicated retries; missing rows contribute zero.
- `godb_sql_rows_matched_total` counts rows matched by successful UPDATE responses,
  including unchanged matches and deduplicated retries.
- `godb_sql_statements_in_flight` measures calls still executing or waiting.
- `godb_sql_rows_returned_total` and `godb_sql_rows_affected_total` count successful
  results. Affected rows include inserted, changed, and deleted rows, exclude table creation and count successful deduplicated
  retries as successful responses, not new physical writes.

The dashboard includes a collapsed SQL section with aggregate execution rates, SELECT access-path rates, statement throughput/p99,
in-flight calls, returned/affected/matched/deleted rows, preparation latency, and read barrier
latency/backlog. Server-only metrics are `godb_sql_prepare_duration_seconds{outcome}`,
`godb_read_duration_seconds{outcome}`, `godb_reads_pending`, and
`godb_read_queue_entries`. Read latency includes local work after the barrier.
Spans `godb.sql.prepare`, `godb.sql.describe`, `godb.sql.execute`, and `godb.read` use the configured
tracer provider and bounded outcome attributes. SQL text, values, table names, and
request IDs never become metric labels or span attributes.

An idle server exports zero SQL gauges/counters; histogram series appear after use.
The CLI optionally exposes MySQL client connections; default Compose leaves that
listener disabled until client credentials and a suitable bind address are configured.

Tests cover persistence/reopen, malformed/trailing syntax, constraints, exact
parameter binding, integer boundaries, duplicate inserts, NULL predicates,
concurrency, canceled calls, metrics, and deterministic log application. Parser
fuzzing and primary/unique/nonunique/composite-prefix SELECT benchmarks are included:

```sh
go test -race ./pkg/sql ./pkg/server ./pkg/replication
go test ./pkg/sql -run='^$' -fuzz=FuzzParseSQL -fuzztime=15s
go test ./pkg/sql -run='^$' -bench=. -benchmem
```

Server tests also cover three-member replication, leader changes, identical-command
retries, quorum loss, application barriers, bounded canceled requests, concurrent
CREATE preparation, restart, ID collisions, and metrics. `BenchmarkServerSQL` measures
single-member prepare/execute point reads, not networked cluster throughput.

The MySQL wire endpoint, TLS/password authentication, and native prepared statements
are implemented. `sql.Describe` returns parameter/result metadata without requiring
parameter values or allocating a table ID; the server uses it behind a quorum barrier.
Native executions still bind current catalog state and generate encoded commands.
The endpoint also handles bounded local session settings, information queries, and
connection resets; see [MySQL compatibility](mysql.md). These do not access the catalog
or require quorum. Catalog discovery now supports SHOW TABLES/COLUMNS/INDEX and DESCRIBE through
quorum-confirmed reads; these wire commands are separate from the standalone SQL
parser. The wire endpoint also implements a bounded SELECT grammar over INFORMATION_SCHEMA
SCHEMATA/TABLES/COLUMNS/STATISTICS/TABLE_CONSTRAINTS/KEY_COLUMN_USAGE, including typed
filters, ordering, and COUNT. Primary and unique constraint metadata reflects the
existing catalog; it does not add foreign keys. CREATE TABLE now supports the index declarations above.
Next work includes larger-result traversal with index-aware pagination, client routing, and remote retry
semantics (including durable CREATE reservations if required). Broader
UPDATE/DELETE predicates, expressions, joins, additional aggregates, more types, session transactions,
and broader driver/ORM initialization and migration queries
remain explicit follow-up work.
