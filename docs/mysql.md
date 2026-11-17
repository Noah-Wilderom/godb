# MySQL client compatibility

GoDB has an opt-in MySQL classic-protocol endpoint. It accepts ordinary MySQL
connections over TLS and executes the existing CREATE TABLE, single-row INSERT, primary-key UPDATE/DELETE,
and bounded SELECT subset through Raft. Both the Go MySQL driver and PHP PDO
have integration tests covering native prepared statements, typed parameters, session setup, catalog discovery, and INFORMATION_SCHEMA queries.
The target is the wider MySQL driver/tool ecosystem, including Go, PHP, Python, Java,
and Node.js clients. Compatibility is implemented at the protocol and SQL layers,
without framework-specific query matching. This is partial protocol/SQL
compatibility, not a replacement for MySQL or a working Laravel migration backend.

## Enable it

Add these flags to an otherwise configured `godb server` command:

```sh
--mysql-listen 127.0.0.1:3307 \
--mysql-user app \
--mysql-password-file /path/to/private/password \
--mysql-database godb
```

The password file contains a nonempty password of at most 4096 bytes, optionally
followed by a line ending. Keep it private to the server user. It is read at startup;
rotation requires restart. There is no default password, anonymous user, or plaintext
mode. One configured user has full access to the SQL subset; per-user permissions
and account management are not implemented. The logical database name maps to the
member's one database file; selecting another database fails. Omitting a database
at login uses that configured database automatically.

The endpoint currently uses the member's `--tls-cert`/`--tls-key` certificate.
Clients must trust its CA and connect using a hostname/IP in its SANs. Client
password authentication is separate from peer mTLS; SQL clients do not need a peer
certificate. TLS 1.3 is required. The default Compose certificates identify node1,
node2, and node3, not localhost. Do not disable certificate verification to work
around a hostname mismatch. Default Compose does not enable this endpoint or mount
client credentials; it remains opt-in.

Connect to the current leader. Followers return MySQL error 1290 with the locally
known leader node ID when available. There is no forwarding, proxy, or discovery
endpoint yet. PING checks an authenticated connection, not quorum readiness.

## PHP PDO smoke example

For a member certificate valid for the address used below:

```php
$pdo = new PDO('mysql:host=127.0.0.1;port=3307;dbname=godb;charset=utf8mb4', 'app', $password, [
    PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION,
    PDO::ATTR_EMULATE_PREPARES => false,
    Pdo\Mysql::ATTR_SSL_CA => '/path/to/ca.crt',
    Pdo\Mysql::ATTR_SSL_VERIFY_SERVER_CERT => true,
]);

$pdo->exec('CREATE TABLE users (id BIGINT PRIMARY KEY, name TEXT NOT NULL)');
$insert = $pdo->prepare('INSERT INTO users (id, name) VALUES (?, ?)');
$insert->bindValue(1, 7, PDO::PARAM_INT);
$insert->bindValue(2, "Noah's example", PDO::PARAM_STR);
$insert->execute();

$query = $pdo->prepare('SELECT name FROM users WHERE id = ?');
$query->bindValue(1, 7, PDO::PARAM_INT);
$query->execute();
$name = $query->fetchColumn();
```

Older PHP versions expose the TLS constants as `PDO::MYSQL_ATTR_SSL_CA` and
`PDO::MYSQL_ATTR_SSL_VERIFY_SERVER_CERT`. Native parameters are separate from SQL
text: quotes, backslashes, NULs, and question marks in values are data. Bind integers
with `PDO::PARAM_INT`, booleans with `PDO::PARAM_BOOL`, and NULL with `PDO::PARAM_NULL`.
The engine retains strict typing; numeric strings are not implicitly converted to
integer columns. UTF-8 strings and PDO stream/LOB values can bind to TEXT columns;
arbitrary non-UTF-8 binary column storage is not exposed by this SQL subset yet.
Emulated string quoting can still use unsupported backslash escapes, so prefer
native statements. Do not replace parameter binding with SQL concatenation.

## Connection setup and session queries

These commands work through COM_QUERY and native prepared statements:

```sql
SET NAMES utf8mb4 COLLATE utf8mb4_bin;
SET SESSION autocommit = 1;
SET @@session.character_set_results = NULL;
USE godb;
SELECT VERSION() AS version, DATABASE() AS db, CONNECTION_ID() AS id;
SELECT @@session.autocommit, @@character_set_client, @@character_set_results;
SELECT 1;
SHOW SESSION VARIABLES LIKE 'character_set_%';
```

`SET NAMES utf8mb4` is also accepted. GoDB uses UTF-8 throughout; client/connection
character sets are fixed to `utf8mb4`, and the wire collation tag is fixed to
`utf8mb4_bin`. Incoming UTF-8 handshake collation IDs are normalized to that tag.
This does not implement MySQL collation rules such as trailing-space equivalence:
TEXT keys retain GoDB's bytewise comparison. Other character sets/collations fail.
`character_set_results=NULL` disables conversion; bytes stay UTF-8 because that is
already the engine encoding. `SET NAMES` restores `character_set_results=utf8mb4`.

The supported session variables are:

| Variable | Value / accepted assignments |
| --- | --- |
| `autocommit` | Always 1; accepts `1`, `ON`, `TRUE`; 0 is rejected |
| `character_set_client`, `character_set_connection` | `utf8mb4` only |
| `character_set_results` | `utf8mb4` or SQL NULL |
| `collation_connection` | `utf8mb4_bin` wire tag only |
| `version` | Read-only `8.0.0-GoDB`, the handshake compatibility string |
| `version_comment` | Read-only `GoDB` |
| `lower_case_table_names` | Read-only 0; identifiers are case-sensitive |
| `max_allowed_packet` | Read-only 131072, the packet payload limit |

`SET` supports unqualified names, `SESSION`/`LOCAL`, and `@@session.`/`@@local.`
qualification, comma-separated assignments, and bound `?` assignment values.
All assignments are validated before changing connection state. PREPARE validates
literals and writable names but does not apply settings. EXECUTE reads current
session values. Unknown variables, global/user variables, expressions, SQL modes,
time zones, isolation settings, and disabling autocommit return errors. Transaction
control SQL remains unsupported; no transaction is silently started or committed.

Information SELECTs can combine the listed variables, the three functions above,
and literal `1`, with optional `AS` aliases. They cannot mix in table reads or
arbitrary expressions. `SHOW [SESSION] VARIABLES` lists only the implemented
variables; optional `LIKE` supports `%` and `_` with case-insensitive matching.
Patterns are limited to 64 bytes; backslash escapes, comments, and multiple
statements are rejected. Result encoding allows bounded metadata lists; ordinary table SELECT supports bounded indexed reads, table scans, NULL tests, and COUNT. See the discovery limits below.

Session settings belong to the physical connection. `COM_RESET_CONNECTION`
restores defaults, releases prepared statements and long parameter buffers, and
retains authentication, the logical database, and the connection ID. Old statement
handles become invalid. No Raft command is generated for local session operations.
Information queries and `SELECT 1`, like PING, may succeed on followers or without
a quorum: use a real table read to check database readiness. Table operations still
use leader checks and quorum-confirmed reads.

Go-driver integration tests exercise its charset option, DSN-style session variables,
and automatic `max_allowed_packet` discovery. PHP PDO tests use native preparation
and explicit typed binding. Protocol tests cover pool resets over verified TLS.
These checks establish a tested subset, not full driver/ORM/tool compatibility.

The behavior follows the relevant parts of MySQL's
[SET NAMES](https://dev.mysql.com/doc/refman/8.4/en/set-names.html),
[SHOW VARIABLES](https://dev.mysql.com/doc/refman/8.4/en/show-variables.html), and
[connection reset](https://dev.mysql.com/doc/c-api/8.4/en/mysql-reset-connection.html)
contracts, with the restrictions above.

## Creating indexes with SQL

CREATE TABLE now accepts inline `UNIQUE [KEY]`, named/unnamed table-level UNIQUE
indexes, ordinary INDEX/KEY declarations, and `CONSTRAINT name UNIQUE (...)`:

```sql
CREATE TABLE accounts (
    id BIGINT PRIMARY KEY,
    handle TEXT,
    tenant BIGINT NOT NULL,
    token TEXT UNIQUE KEY,
    UNIQUE KEY tenant_handle (tenant, handle),
    INDEX handle_lookup (handle)
);
```

This works through COM_QUERY and native prepared statements. PREPARE does not
create the table; EXECUTE applies the complete catalog command through Raft.
Metadata queries report the declared names, uniqueness, and composite key order.
Duplicate index names (case-insensitive within the new table) or reserved PRIMARY
names fail; the name-validation error maps to 1061 when reached during preparation.
Primary/unique value conflicts on INSERT return 1062. NULL-containing unique keys
are permitted more than once. Failed inserts leave no partial row/index entries.

Unnamed indexes receive deterministic names based on their first column with
collision suffixes; explicit names are reserved before generating defaults. See
[SQL index declarations](sql.md#indexes-declared-at-table-creation) for exact grammar,
name generation, supported key types, and limits. Full TEXT values use GoDB's key
semantics; MySQL-style prefixes and collations are not implemented. SQL SELECT supports AND-connected equality on primary keys and composite-index
prefixes, including complete unique keys and additional row filters.
See [indexed equality reads](sql.md#indexed-equality-reads) for selection rules and
the 64-predicate and 1,024-candidate-row bounds. User-table SELECT also supports
ORDER BY named columns with ASC/DESC and LIMIT count [OFFSET offset] or LIMIT
offset,count. Count/offset can be integer placeholders, each bounded to 0–1,024;
these do not relax the candidate scan bound. See [ordering and bounded pages](sql.md#ordering-and-bounded-result-pages).
CREATE INDEX/ALTER TABLE are not yet supported.

## Updating rows

`UPDATE table SET column = value [, ...] WHERE primary_key = value` supports typed
literals and parameters. Unassigned columns are preserved at commit time. Both
native and text executions update secondary indexes atomically; unique conflicts
return 1062. Missing rows and unchanged values succeed. The default affected count
is changed rows; negotiated `CLIENT_FOUND_ROWS` reports matched rows, including
unchanged matches. INSERT still returns one under either mode. This connection
capability survives COM_RESET_CONNECTION.

Only a single primary-key equality predicate is accepted; primary-key changes,
expressions, extra predicates, and multirow updates remain unsupported. See
[SQL UPDATE](sql.md#updating-a-row) for durability, retry, and capacity semantics.

## Schema discovery for clients and tools

Discovery uses standard command forms rather than framework-specific query strings:

```sql
SHOW DATABASES;
SHOW FULL TABLES FROM godb LIKE 'user%';
SHOW COLUMNS FROM godb.users;
SHOW FULL COLUMNS FROM users FROM godb LIKE 'name';
DESCRIBE users;
SHOW INDEX FROM users;
```

Aliases include `SHOW SCHEMAS`, `SHOW FIELDS`, `DESC`, `SHOW INDEXES`, and
`SHOW KEYS`. `IN` can replace `FROM`. Table names may be database-qualified or
backtick-quoted. Optional `LIKE` filters work on database/table/column lists and
accept a quoted pattern or native `?` parameter; identifiers cannot be parameters.
`DESCRIBE table column` selects an exact column name, while `DESCRIBE table 'pattern'`
uses pattern matching. `%` and `_` match Unicode code points, case-sensitively to
match GoDB identifier semantics. Patterns are limited to 64 bytes; backslash
escapes, WHERE expressions, multiple statements, and unsupported tails fail.

Table names come from the persisted catalog and are sorted by UTF-8 bytes. FULL
TABLES reports `BASE TABLE`; GoDB has no views. Columns preserve catalog declaration
order and report name, type, nullability, key classification, NULL defaults, and
empty Extra. FULL COLUMNS also reports the UTF-8 wire collation tag for TEXT,
`select,insert` for currently exposed SQL column operations, and empty comments.
No auto-increment attributes, generated values, or comments are invented.

The four SQL-exposed types report `bigint`, `bigint unsigned`, `tinyint(1)` (BOOLEAN),
and `text`. Catalogs created through the Go API may contain additional record types;
these report explicit native names such as `godb_uuid`, `godb_decimal`, or
`godb_duration`. These names are not MySQL DDL types, and discovery does not enable
SQL reads/writes for those types. Likewise, the `utf8mb4_bin` tag does not change
GoDB's bytewise TEXT comparison into MySQL collation semantics.

Index results include the primary key and every stored secondary index, with one
row per indexed column. They report uniqueness, one-based column sequence,
ascending order, nullability, and BTREE storage. Cardinality is NULL because the
engine does not maintain those estimates; prefix length, packing, and expression
fields are also NULL. Stored indexes are reported as visible, with empty comments.
A single-column unique index gives its column UNI classification; composite unique
and nonunique index leading columns give MUL, with PRI taking precedence. Secondary
indexes can now be declared in CREATE TABLE and remain available through the Go
engine API. SQL equality reads select a matching index using deterministic rules;
complete unique keys and the longest constrained prefix take priority. Cost-based
optimization remains pending.

Every table/column/index discovery execution and native PREPARE uses the existing
linearizable catalog read barrier. Followers reject these requests, and quorum loss
returns an error rather than stale schema. Prepared executions re-read the catalog;
preparing SHOW TABLES before CREATE does not freeze the old list. Discovery performs
no catalog writes or table-ID allocation. SHOW DATABASES lists the single configured
logical database locally and does not establish quorum readiness.

Catalog enumeration is capped at 1024 tables, before LIKE filtering. Encoded results
are capped at 4096 rows and 4 MiB of combined column/row payloads, in addition to the
128 KiB per-packet limit. Exceeding a limit fails before any result packets are sent;
results are never silently truncated. The internal Go catalog API supports paging,
but wire discovery has no pagination clause yet. A missing table returns 1146 and an
unknown database returns 1049. Empty matching lists are valid empty result sets.

`SHOW CREATE TABLE`, `SHOW TABLE STATUS`, COM_FIELD_LIST, richer filters, migrations,
and full ORM/GUI initialization are not implemented. A bounded INFORMATION_SCHEMA
subset is described below. The tested
surface is Go's MySQL driver and PHP PDO, using text and native prepared results;
this does not certify all Java/JDBC, Python, Node.js, ORM, or GUI clients.

The result layouts follow the relevant portions of MySQL's
[SHOW TABLES](https://dev.mysql.com/doc/refman/8.4/en/show-tables.html),
[SHOW COLUMNS](https://dev.mysql.com/doc/refman/8.4/en/show-columns.html), and
[SHOW INDEX](https://dev.mysql.com/doc/refman/8.4/en/show-index.html) specifications,
with the explicit limitations above.

## INFORMATION_SCHEMA queries

The wire endpoint now exposes six read-only metadata views: `SCHEMATA`, `TABLES`,
`COLUMNS`, `STATISTICS`, `TABLE_CONSTRAINTS`, and `KEY_COLUMN_USAGE`. They describe the configured logical database and its
persisted catalog, using the same source as SHOW discovery. The virtual metadata
views do not enumerate themselves, and `USE information_schema` is not supported;
queries must use the qualified view name. This is an explicit subset of MySQL's
information schema, not a complete system catalog.

```sql
SELECT TABLE_NAME AS name, ENGINE, TABLE_ROWS
FROM information_schema.tables
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'
ORDER BY name;

SELECT c.COLUMN_NAME, DATA_TYPE, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT
FROM information_schema.columns AS c
WHERE c.TABLE_SCHEMA = DATABASE() AND c.TABLE_NAME = ?
ORDER BY ORDINAL_POSITION;

SELECT COUNT(*) AS aggregate
FROM information_schema.tables
WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?;

SELECT INDEX_NAME, SEQ_IN_INDEX, COLUMN_NAME, NON_UNIQUE
FROM information_schema.statistics
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?
ORDER BY INDEX_NAME, SEQ_IN_INDEX;
```

Supported query grammar:

- `SELECT *`, a list of column references with optional `AS` aliases, or a single
  `COUNT(*)` with an optional alias. COUNT counts matching metadata rows, not data
  rows in the underlying user tables.
- One qualified `information_schema.view`, optionally with a table alias. References
  can be unqualified or qualified by that alias (or the view name when unaliased).
- Optional AND-connected predicates: `column = value`, `column LIKE value`,
  `column IS NULL`, and `column IS NOT NULL`. Values can be quoted text, nonnegative
  integer literals, SQL NULL, `DATABASE()`, or bound `?` parameters.
- Optional `ORDER BY` column references or unambiguous projection aliases, each with
  ASC/DESC. Multiple keys and numeric ordinal ordering work; ascending puts NULL
  first. ORDER BY on COUNT is rejected.
- Optional literal `LIMIT n [OFFSET n]`, each from 0 through 4096. Limit parameters,
  comma-style limits, implicit projection aliases, `alias.*`, and positional ORDER
  BY are not implemented.

View/field names are case-insensitive, while metadata text values use case-sensitive
UTF-8 byte ordering. LIKE uses the same Unicode `%`/`_` matching as SHOW discovery.
Equality and LIKE against NULL never match; use IS NULL. Parameters retain their
record types; numeric strings are not converted to integers. Checked unsigned
parameters can bind to signed numeric metadata fields if they fit int64. Filters
are limited to 1024 text bytes; LIKE patterns to 64, without backslash escapes.
There are at most 64 projections, predicates, or ordering keys. Unknown fields,
views, joins, OR/IN, arbitrary functions, subqueries, grouping, writes, comments,
backslash-escaped literals, and trailing unsupported syntax return errors.

Supported fields (SELECT * returns this order):

| View | Fields |
| --- | --- |
| SCHEMATA | CATALOG_NAME, SCHEMA_NAME, DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME, SQL_PATH |
| TABLES | TABLE_CATALOG, TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE, ENGINE, TABLE_ROWS, AUTO_INCREMENT, TABLE_COLLATION, CREATE_OPTIONS, TABLE_COMMENT |
| COLUMNS | TABLE_CATALOG, TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, ORDINAL_POSITION, COLUMN_DEFAULT, IS_NULLABLE, DATA_TYPE, CHARACTER_MAXIMUM_LENGTH, CHARACTER_OCTET_LENGTH, NUMERIC_PRECISION, NUMERIC_SCALE, DATETIME_PRECISION, CHARACTER_SET_NAME, COLLATION_NAME, COLUMN_TYPE, COLUMN_KEY, EXTRA, PRIVILEGES, COLUMN_COMMENT, GENERATION_EXPRESSION |
| STATISTICS | TABLE_CATALOG, TABLE_SCHEMA, TABLE_NAME, NON_UNIQUE, INDEX_SCHEMA, INDEX_NAME, SEQ_IN_INDEX, COLUMN_NAME, COLLATION, CARDINALITY, SUB_PART, PACKED, NULLABLE, INDEX_TYPE, COMMENT, INDEX_COMMENT, IS_VISIBLE, EXPRESSION |
| TABLE_CONSTRAINTS | CONSTRAINT_CATALOG, CONSTRAINT_SCHEMA, CONSTRAINT_NAME, TABLE_SCHEMA, TABLE_NAME, CONSTRAINT_TYPE, ENFORCED |
| KEY_COLUMN_USAGE | CONSTRAINT_CATALOG, CONSTRAINT_SCHEMA, CONSTRAINT_NAME, TABLE_CATALOG, TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, ORDINAL_POSITION, POSITION_IN_UNIQUE_CONSTRAINT, REFERENCED_TABLE_SCHEMA, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME |

ENGINE reports `GoDB`. Row-count estimates, auto-increment values, index cardinality,
text length limits, and unsupported temporal precision are NULL. Column defaults
remain NULL; comments, generation expressions, and extra options are empty. Signed
and unsigned BIGINT report decimal precision 19 and 20 with scale zero. Boolean
reports `tinyint(1)` as its compatibility type, but precision 1 reflects GoDB's
actual 0/1 domain. Additional native record types retain the explicit `godb_*`
names described above. Collation tags retain the same documented limitations.

Constraint discovery includes the primary key and stored unique secondary indexes.
TABLE_CONSTRAINTS emits one row per constraint, with `PRIMARY KEY` or `UNIQUE` and
`ENFORCED=YES`. KEY_COLUMN_USAGE emits one row per constrained column, preserving
composite index order; ORDINAL_POSITION starts at one within the constraint, not
the table definition. Ordinary nonunique indexes appear only in STATISTICS.

Unique index names are preserved; the primary key is named `PRIMARY`. Filter on
TABLE_SCHEMA and TABLE_NAME as well as CONSTRAINT_NAME because names may repeat
across tables. POSITION_IN_UNIQUE_CONSTRAINT is a nullable integer, and all three
REFERENCED_* fields are nullable text. These four fields are always NULL for the
currently implemented primary/unique constraints. Foreign-key and CHECK constraints
are not implemented or synthesized. Filtering TABLE_CONSTRAINTS for those types
returns no matches; unsupported foreign-key/check metadata views still return errors.
Nullable unique keys retain the engine's behavior: NULL-containing keys do not
conflict with one another. Unique and ordinary indexes can now be declared through CREATE TABLE as well as
the Go catalog API. Altering indexes on existing tables is not yet supported.

```sql
SELECT CONSTRAINT_NAME, CONSTRAINT_TYPE, ENFORCED
FROM information_schema.table_constraints
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?
ORDER BY CONSTRAINT_NAME;

SELECT CONSTRAINT_NAME, COLUMN_NAME, ORDINAL_POSITION
FROM information_schema.key_column_usage
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?
ORDER BY CONSTRAINT_NAME, ORDINAL_POSITION;
```

Both queries use the same binding, read barriers, limits, ordering, and aggregate
support as the other metadata views. Joins between these views remain unsupported.
Tests compare their output with primary, single-column unique, composite unique,
and ordinary indexes, and verify duplicate rejection plus nullable unique behavior
through real writes. Go-driver and PDO integration tests cover native bindings,
NULL reference fields, preparation before table creation, and quorum rejection.

PREPARE validates the view, fields, and literal types without changing data; native
execution binds parameters separately and re-reads the catalog. Every accepted
information-schema query, including SCHEMATA, uses a single quorum-confirmed catalog
snapshot. Followers and members without quorum cannot return successful results.
Missing tables/databases in filters produce empty matches (or COUNT 0), not a
missing-table error. Failed queries leave the connection usable.

The existing 1024-table catalog cap applies before filtering. Execution examines at
most 65536 metadata rows. Non-aggregate queries materialize at most 4096 matching
rows with a conservative 4 MiB cell-data budget **before** projection, ordering,
and LIMIT. LIMIT does not bypass this work bound. COUNT streams matches without
materializing rows, so it can count more than 4096 within the scan cap. Encoded
responses still obey the existing packet/response limits. Exceeding any limit
returns an error rather than a partial list or partial count.

The result fields follow the relevant parts of MySQL's
[SCHEMATA](https://dev.mysql.com/doc/refman/8.4/en/information-schema-schemata-table.html),
[TABLES](https://dev.mysql.com/doc/refman/8.4/en/information-schema-tables-table.html),
[COLUMNS](https://dev.mysql.com/doc/refman/8.4/en/information-schema-columns-table.html),
[STATISTICS](https://dev.mysql.com/doc/refman/8.4/en/information-schema-statistics-table.html),
[TABLE_CONSTRAINTS](https://dev.mysql.com/doc/refman/8.4/en/information-schema-table-constraints-table.html),
and [KEY_COLUMN_USAGE](https://dev.mysql.com/doc/refman/8.4/en/information-schema-key-column-usage-table.html)
contracts, with the restrictions above. Go/PDO tests cover the examples, typed
parameters, NULLs, ordering, schema changes after preparation, resource bounds,
and quorum failures. Other drivers and full ORM workflows still need independent
compatibility testing. The standalone SQL Go API does not parse these wire-only
metadata commands yet.

## Implemented protocol

- Handshake v10, protocol 4.1, TLS upgrade, and `caching_sha2_password` full
  authentication over TLS. There is no password cache or insecure RSA fallback.
- CLIENT_FOUND_ROWS for matched UPDATE row counts.
- COM_QUERY, COM_PING, COM_INIT_DB, COM_RESET_CONNECTION, and COM_QUIT.
- COM_STMT_PREPARE, COM_STMT_EXECUTE, COM_STMT_RESET, COM_STMT_CLOSE, and bounded
  COM_STMT_SEND_LONG_DATA. Native execution returns binary result rows.
- Text result sets with column definitions, NULL values, EOF packets, affected-row
  counts, and error packets with SQLSTATE. Supported result types: signed/unsigned
  BIGINT, BOOLEAN, and TEXT.
- The handshake reports `8.0.0-GoDB` for client protocol compatibility; this is not
  a claim of MySQL 8 feature compatibility or the GoDB binary version.

Capabilities are limited to implemented features. Some clients advertise additional
capabilities regardless of the greeting; these do not enable features on GoDB.
Multi-statement SQL, compression, LOCAL INFILE, server cursors/COM_STMT_FETCH, query
attributes, session transactions, and broader SQL are not
implemented. Unsupported commands/SQL return errors rather than successful no-ops.
Text uses UTF-8 and existing bytewise index ordering, not MySQL's collation behavior.

Protocol references: MySQL's [connection packets](https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_connection_phase_packets.html),
[full SHA-2 authentication](https://dev.mysql.com/blog-archive/preparing-your-community-connector-for-mysql-8-part-2-sha256/),
and [text result sets](https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_com_query_response_text_resultset.html).

## Native statement lifecycle

PREPARE validates syntax, names, and literal bindings against a quorum-confirmed
catalog and returns parameter/result metadata. It performs no row mutation and
allocates no table ID. CREATE TABLE can be prepared with zero parameters; its
actual ID is assigned during execution.

Handles belong to one authenticated connection and are monotonically assigned;
closing a handle does not let an old ID refer to a replacement. At execution, the
server decodes values directly into `record.Value`, binds against the current
catalog, and runs the normal replicated SQL path. This is not a cached physical
query plan or a promise of faster execution. Changed result metadata returns error
1615 and requires a new prepare. There is no schema ALTER/DROP support yet.

Supported non-NULL input encodings are signed/unsigned TINY, SHORT, LONG/INT24,
LONGLONG, and UTF-8 STRING/VAR_STRING/VARCHAR/BLOB families. Small integers become
GoDB signed/unsigned 64-bit values and pass the existing range/type checks. Other
input encodings, including floats and date/time values, return an unsupported-type
error. Results use signed/unsigned BIGINT, BOOLEAN, TEXT, and NULL encodings.

EXECUTE supports new type descriptors and reuse of the previous valid descriptors.
Malformed/truncated/trailing input is rejected before the query handler runs.
RESET clears buffered parameter data and deferred errors while retaining the handle
and its cached types. CLOSE sends no response and frees the handle. Disconnect
releases every handle and buffered value.

SEND_LONG_DATA appends chunks for string parameters without replying. Invalid
parameter indexes or buffer overflow latch an error on that statement; its next
EXECUTE returns the error and performs no query. RESET also clears that error.
Buffered chunks are cleared after every execution attempt, successful or failed.
A NULL bitmap bit overrides buffered data for that parameter. These chunks remain
subject to the existing row/storage limits, which can be much smaller than the
transport limit.

Protocol references: [prepared statement execution](https://dev.mysql.com/doc/dev/mysql-server/8.4.10/page_protocol_com_stmt_execute.html),
[binary results](https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_binary_resultset.html),
[long parameter data](https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_com_stmt_send_long_data.html),
and [reset](https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_com_stmt_reset.html).

## Replication, deadlines, and retry semantics

Each query prepares against a quorum-confirmed catalog. Writes use a fresh
server-generated request ID and wait for committed local application; SELECT uses
a new ReadIndex/application barrier. Authentication and PING do not touch Raft.

Neither COM_QUERY nor a prepared statement handle carries GoDB's durable request ID or prepared command.
A timeout or lost connection after write submission has an **ambiguous outcome**:
the write can still commit. Retrying SQL on another connection creates a new
request, not an idempotent replay. Do not automatically retry ambiguous writes.
Exactly identified retries remain available through the server Go API. A remote
retry contract remains separate work; there is no claim of exactly-once wire writes.

| Limit | Default |
| --- | --- |
| Simultaneous accepted connections, including handshakes | 128 (`--mysql-max-connections`, maximum 4096) |
| Handshake deadline | 10 seconds |
| Idle/command read deadline | 1 minute (`--mysql-idle-timeout`) |
| Query deadline | 5 seconds (`--mysql-query-timeout`) |
| Response write deadline | A fresh query-timeout interval |
| Packet payload | 128 KiB; continuation packets are rejected |
| SQL statement | 64 KiB, plus existing parser/storage limits |
| Prepared handles per connection | 128 |
| Prepared SQL text per connection | 1 MiB |
| Buffered long parameter data per connection | 128 KiB across all handles |
| Parameters / result columns per statement | 64 each |

Admission overflow closes the new connection before authentication. Idle timeouts
close connections. Shutdown closes idle and handshaking sockets and waits for
handlers before closing storage. Cancellation cannot undo a proposed write.

## Metrics and validation

The server registry includes `godb_mysql_connections`, `godb_mysql_connections_peak`,
`godb_mysql_sessions`, and counters for connections opened/closed, rejected
connections, authentication failures, and malformed command packets. Connection
counts include unauthenticated handshakes; session counts include only authenticated
clients. Authentication failures include TLS/protocol failures, not just bad passwords.
Prepared-statement metrics add `godb_mysql_prepared_statements`,
`godb_mysql_prepared_statement_bytes`, and `godb_mysql_long_data_bytes` gauges.
`godb_mysql_stmt_prepare_total` counts prepare attempts;
`godb_mysql_stmt_execute_total` counts executions for known handles, including
failures; `godb_mysql_statement_errors_total` counts statement error responses.
SQL text bytes exclude metadata/allocation overhead. Limits are per connection,
so aggregate memory depends on the configured connection limit too.
Local session SQL has separate `godb_mysql_session_commands_total` and
`godb_mysql_session_errors_total` counters. Commands include execution failures,
but exclude PREPARE descriptions and protocol INIT_DB/PING. Successful resets use
`godb_mysql_connection_resets_total`. These operations bypass the database SQL
histograms and quorum-read metrics. The dashboard shows all three rates.
Schema discovery adds `godb_mysql_discovery_commands_total` and
`godb_mysql_discovery_errors_total` (execution attempts and errors before result
encoding; excludes PREPARE descriptions). The server records catalog access latency
in `godb_mysql_catalog_duration_seconds{outcome}`, including prepare, quorum wait,
and local enumeration. The `godb.mysql.catalog` span carries no table/query values.
Catalog access also contributes to the existing consistent-read metrics. Dedicated
dashboard panels show discovery rates and catalog p95 latency.
INFORMATION_SCHEMA executions use `godb_mysql_information_schema_commands_total`
and `godb_mysql_information_schema_errors_total`, excluding PREPARE descriptions
and post-execution result-encoding failures. Catalog access contributes to the
existing catalog latency histogram, read-barrier metrics, and catalog span. These
queries do not increment table-query or affected-row metrics. The dashboard shows
information-schema execution/error rates alongside SHOW discovery activity.
No user names, SQL, passwords, database names, or connection IDs become labels.

Existing SQL metrics/traces now cover wire queries too. The dashboard includes client
connections, sessions, connection churn, handshake/admission errors, and total open
connections including MySQL. SQL histograms exclude preparation; the separate
preparation/read graphs include it. Native PREPARE emits a `godb.sql.describe` span
and records preparation latency; EXECUTE uses the existing preparation/execution
spans and metrics. Additional panels show statement handles, buffered bytes, and
prepare/execute/error rates. Without the opt-in listener, MySQL series are absent.

```sh
go test -race ./pkg/mysqlwire ./pkg/server ./cmd/godb/cmd
go test ./pkg/mysqlwire -run='^$' -fuzz=FuzzLogin -fuzztime=15s
go test ./pkg/mysqlwire -run='^$' -fuzz=FuzzPacket -fuzztime=15s
go test ./pkg/mysqlwire -run='^$' -fuzz=FuzzExecuteParameters -fuzztime=15s
go test ./pkg/mysqlwire -run='^$' -fuzz=FuzzSessionStatements -fuzztime=15s
go test ./pkg/mysqlwire -run='^$' -fuzz=FuzzDiscovery -fuzztime=15s
go test ./pkg/mysqlwire -run='^$' -fuzz=FuzzInformationSchema -fuzztime=15s
go test ./pkg/server -run='^$' -bench='BenchmarkServerMySQL(Read|PreparedRead)$' -benchmem
```

The PDO test runs when PHP and pdo_mysql are installed; otherwise it skips. Tests
also verify the Go driver, credential failures, mandatory TLS, unsupported commands,
quorum loss, follower rejection, shutdown, admission bounds, framing, NULLs, and
integer boundaries, reused parameter types, long-data failure/reset behavior, and
statement cleanup. Benchmarks are single-member loopback tests.

Next steps for broader client support include traversal beyond the candidate scan bound, more
column types/operations, and explicit transaction semantics. ORM migrations, GUI database
browsers, and transaction-dependent applications still need those capabilities.
Python, Java/JDBC, and Node.js drivers are targets, not yet integration-tested claims.

## Primary-key DELETE

Text and native prepared `DELETE FROM table WHERE primary_key = value` statements
remove one row and all its index entries atomically. Literals and placeholders are
supported. The OK packet reports one removed row, or zero for a missing row or NULL
predicate, with identical counts under CLIENT_FOUND_ROWS. PREPARE does not delete.
Non-primary predicates, missing WHERE, additional predicates, ORDER BY, and LIMIT
are rejected. See [SQL deletion](sql.md#deleting-a-row) for durable retry semantics
and the replica upgrade requirement. Go-driver and PHP PDO tests cover native
parameter binding, repeated execution, and affected-row counts.

## Table scans

User-table SELECT accepts an omitted WHERE clause or equality filters on unindexed
columns. Text queries and native prepared statements share the bounded table scan
path, ordering, typed parameters, and projection metadata. Go-driver and PHP PDO
tests cover both forms. A table scan exceeding 1,024 candidate rows returns error
1105 without partial rows, even if LIMIT is small or zero. This does not change
UPDATE/DELETE predicate requirements or INFORMATION_SCHEMA's separate grammar.
See [bounded table scans](sql.md#bounded-table-scans) for consistency and cursor scope.

## User-table COUNT

Text queries and native prepared statements support `SELECT COUNT(*)` and
`SELECT COUNT(column)`, optionally followed by `AS alias`, over ordinary tables.
They return one nonnullable BIGINT result; COUNT(column) excludes NULL values.
WHERE and LIMIT/OFFSET accept the existing typed parameters. LIMIT/OFFSET applies
to the aggregate result row, not the rows being counted. Empty matches return zero;
LIMIT 0 or a positive offset returns no result row with normal column metadata.

Go-driver and PHP PDO tests cover aliases, result types, zero counts, nullable
columns, and parameters. Candidate-limit failures return error 1105 without partial
counts, even with LIMIT 0. Multiple/mixed projections, DISTINCT, expressions,
GROUP BY, HAVING, windows, and ORDER BY with COUNT are rejected. See
[SQL counting](sql.md#counting-matching-rows) for the 1,024-candidate bound and quorum
semantics. INFORMATION_SCHEMA counts keep their existing separate grammar.

## NULL predicates on user tables

Text queries and native prepared SELECT statements accept `IS NULL` and
`IS NOT NULL`, including AND combinations, COUNT, ordering, and LIMIT/OFFSET.
NULL tests consume no bound values; placeholders remain available in equality
filters and LIMIT/OFFSET. `IS ?` is rejected. Existing INFORMATION_SCHEMA NULL
predicates retain their separate grammar. Mutation predicates remain unchanged.

A nullable unique-index lookup uses a scan when its constrained key contains NULL,
so it can return all matching NULL rows. IS NOT NULL filters another selected
index range or a bounded table scan. The existing scan cap and MySQL 1105 failure
remain in force. Go-driver and PHP PDO tests exercise duplicate NULLs, composite
prefixes, parameter order, COUNT, and re-execution after an update. See
[SQL NULL predicates](sql.md#null-predicates).

## Ordered comparisons on user tables

Text and native prepared SELECT statements support `<`, `<=`, `>`, and `>=` with
literal or typed parameter values, including COUNT and AND combinations with
existing equality/NULL filters. Prepare describes parameters using the target
column type. NULL comparison parameters produce no matches; COUNT returns zero.
Tests cover full-width unsigned integers, mixed filters, pagination parameters,
PHP PDO, and re-execution after replicated writes and leader changes.

Primary and eligible secondary-index comparisons can seek bounded ranges. Other
comparisons filter the captured candidates. The 1,024-candidate limit applies
inside the selected range; LIMIT never bypasses it.
Integer precision is preserved; text uses GoDB's bytewise comparison and strict
binding rules. UPDATE/DELETE predicates and INFORMATION_SCHEMA's independent
grammar are unchanged. See [ordered comparisons](sql.md#ordered-comparisons).

## Primary-key ranges

Text and native prepared SELECT/COUNT queries intersect primary-key bounds and
seek the B+ tree directly. A table larger than 1,024 rows can be queried through a
smaller range. The driver tests verify narrow-range success, COUNT, metadata for
empty matches, and error 1105 for oversized ranges despite LIMIT. PHP PDO's existing
bound comparison queries use the same path. Point lookups retain priority, then
primary ranges precede secondary prefixes/ranges and table scans. Parameters, NULL
semantics, and Raft barriers are unchanged. See [primary-key range scans](sql.md#primary-key-range-scans).

## Secondary-index ranges

Text and native prepared SELECT/COUNT use ordered bounds on the first secondary
index column, or the next column after an equality/IS NULL prefix. For an index
on `(tenant, score)`, `tenant = ? AND score >= ? AND score < ?` seeks directly to
that tenant's matching scores. Duplicate boundary values are included/excluded as
a group; NULL in the ranged column does not match. Go driver and PHP PDO tests
cover prepared bounds, empty results, counts, and NULL parameters. Large-table
driver tests verify narrow ranges succeed and oversized ranges return 1105 even
with LIMIT. Leader-change tests retain quorum checks for empty and nonempty ranges.
See [secondary-index range scans](sql.md#secondary-index-range-scans) for planner
preferences, candidate limits, cursor scope, and storage semantics.

## EXPLAIN SELECT

EXPLAIN returns one GoDB plan-description row for a supported user-table SELECT,
including COUNT. It reports the selected index/access path, equality-prefix length,
range boundary rules, candidate cap, and result window. This output uses GoDB's
own column layout; MySQL-specific EXPLAIN result parsers are not supported. There
are no cost or row-count estimates, and EXPLAIN ANALYZE/FORMAT/mutations are rejected.

Text queries and native prepared statements use the same typed SELECT planning.
Go/PDO tests cover bound parameters, NULL re-execution, result metadata, and LIMIT 0.
The explain result always has one row, including for queries that would hit the
1,024-candidate limit. User rows are not read. Catalog preparation and server
execution remain quorum-confirmed; follower/quorum failures still fail. See
[EXPLAIN semantics and columns](sql.md#explaining-a-select).
