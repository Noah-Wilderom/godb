# Database validation

`database.Validate(path)` and `godb database validate <path>` check the stored
database without writing or repairing anything. Stop all writers before calling
them: validation opens a separate handle and does not acquire a cross-process
lock or a snapshot. A nonempty WAL returns `ErrRecoveryRequired`; run `godb database recover <path>` first. `Open` replays a complete valid WAL and then performs its lightweight header, size, and root
checks; it does not run this full validation automatically.

Validation checks:

- The header, declared file size, and root page.
- Every page reachable from the root, including page decoding, child ID bounds,
  cycles/shared children, separator ranges, global key ordering, and equal leaf
  depths. Leaf keys must belong to their ancestor separator ranges.
- Every catalog definition and name reference, in both directions. Definitions
  must match their stored IDs and names; unknown catalog entry kinds are rejected.
- Every primary row against its registered schema and encoded primary key.
- Every row's expected secondary index entries, including their primary references.
- Every secondary index entry against its referenced row and registered index.
  This detects dangling references, stale/extra entries, unknown index IDs,
  cross-table references, and conflicting unique values.

An empty database, empty registered table, nullable unique index, or table without
secondary indexes is valid. The validator scans all entries, without the public
scan API's 1024-entry limit. It first walks the tree and collects catalog metadata,
then walks it again to validate rows and indexes using point lookups. It retains
visited page IDs and catalog metadata rather than all row payloads.

Keys beginning with `00` belong to the catalog, and keys beginning with `01` belong
to the version-1 row/index namespace. Malformed entries within those namespaces
are errors, even if they were inserted through raw `Put`. Raw entries outside
those namespaces, including the empty key, have no schema checks. Old uncataloged
typed rows are rejected; validation does not infer a schema or migrate them.

The first detected problem is returned with context. Catalog errors wrap
`ErrInvalidCatalog`, row/index consistency errors wrap `ErrInvalidData`, and
structural tree errors wrap `pager.ErrPagerCorruptPage`. I/O errors remain available
through `errors.Is`. On failure, the CLI does not print its success message.

Validation covers the reachable tree. Unreachable allocated pages are not audited
or reclaimed; there is no free-page inventory yet. Empty child leaves and
single-child internal nodes are accepted, and separator keys need to define valid
ranges rather than exactly equal a child's first key. Validation does not prove
crash durability or repair unrelated page corruption. Interrupted logged write
groups are handled by [recovery](recovery.md), which runs on `Open`.
