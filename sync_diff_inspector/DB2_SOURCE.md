# Db2 LUW source to TiDB target

This V1 path supports one Db2 LUW 11.5 source instance and one TiDB target.
Db2 is read-only in this tool: generated repair SQL is TiDB SQL and is only
exported, never executed.

Build the Db2 connector with the IBM CLI driver:

```sh
export IBM_DB_HOME=/opt/ibm/clidriver
export CGO_CFLAGS="-I$IBM_DB_HOME/include"
export CGO_LDFLAGS="-L$IBM_DB_HOME/lib"
export LD_LIBRARY_PATH="$IBM_DB_HOME/lib:$LD_LIBRARY_PATH"
go build -tags db2cli ./sync_diff_inspector
```

On Windows, use `scripts/run-db2-local.ps1 -Action Build`. The Windows driver
loads `db2cli64.dll` dynamically and builds with `CGO_ENABLED=0`, so GCC is not
required. The portable Db2 CLI remains under `.local-tools/clidriver`.

The selected driver is `github.com/ibmdb/go_ibm_db v0.5.4`. It requires the
Db2 CLI native client. Building without `-tags db2cli` remains supported for
offline tests, but starting a Db2 source returns an error explaining the
missing build tag and native environment.

Use [config_db2.toml](./config/config_db2.toml) as the minimum configuration.
Db2 unquoted identifiers are normalized to upper case. Quoted identifiers keep
their case. `database` and `schema` are mandatory for a Db2 source.

## Missing Db2 source objects

`skip-non-existing-table` is a root-level option, independent of the Db2 data
source. Its default `false` is strict mode: initialization stops when a table
selected from TiDB is not present in the exact normalized Db2 schema/table
pair. The error includes the normalized name and asks the operator to check the
source object, schema, quoted-identifier case, and target filter.

Set `skip-non-existing-table = true` to report only a genuinely absent Db2
source object as missing upstream and continue with the remaining tables. A
genuine absence means the exact schema exists and the catalog has neither the
exact table nor a differently cased table candidate. Such an object is not
treated as an empty table, is not checksummed, is not scanned, and never
produces repair SQL. A missing schema returns `SchemaNotFoundError`; a
differently cased schema or table returns `IdentifierCaseError` with the
catalog candidates. Neither is skipped. This setting also does not correct
catalog permissions or a catalog/connection failure. Use `schema =
"\"MixedCase\""` and matching quoted table configuration when the Db2 object was
created with quoted mixed case.

The Db2 catalog lookup is exact. Ordinary `TABLE` and `VIEW` objects are
supported when their columns can be read; a VIEW without a common key can use
`no-unique-key-mode = "checksum-only"`. Catalog-visible `ALIAS`, `NICKNAME`,
materialized-query tables, and other special object types are rejected with
their catalog type rather than guessed or selected by a case-insensitive
fallback. Session-declared temporary objects that are not exposed by the
catalog cannot be distinguished from any other absent object, so they are never
selected by name fallback. `no-unique-key-mode` controls only present objects
without a common unique key; it cannot make a missing object checksum-only.

On Windows, set `client-code-page = "1208"` under the Db2 data source when
Chinese or other non-ASCII characters must be compared. The tool validates this
as a numeric Db2 code page and sets `DB2CODEPAGE` before the first Db2
connection. This process-level setting applies to the single Db2 source in V1;
it is not a `connection-params`/DSN keyword.

For Db2 databases whose ordinary `CHAR`, `VARCHAR`, or `CLOB` result bytes are
GBK, set `source-charset = "gbk"`. The Db2 row iterator converts only those
text columns to UTF-8 before comparison; binary columns and `GRAPHIC`/
`VARGRAPHIC` values are left untouched. Do not use this setting for a Db2
source that already returns UTF-8 bytes.

## Comparison protocol

Db2 and TiDB do not compare their server-side hashes. Both endpoints use
`CanonicalV1`, a Go-side SHA-256 digest with a versioned, typed,
length-delimited row encoding. The protocol distinguishes NULL and empty
values; preserves integer and decimal precision; normalizes timestamps to UTC;
trims trailing spaces for CHAR; preserves binary bytes; and caps LOB values at
16 MiB per value. A digest with a different algorithm version is never treated
as equal.

For V1, Db2 tables use keyset chunks ordered by `index-fields`, then primary
key, then a non-null unique index. `chunk-size` controls the number of rows in
each chunk. A nullable configured key is rejected because Db2 NULL ordering
cannot provide a portable, non-overlapping keyset. Tables without a stable key
must be configured explicitly; the analyzer fails instead of silently using
unstable OFFSET pagination. Legacy string `range` expressions are rejected for
a Db2 source because they are MySQL syntax. Bounds are stored as structured,
typed values and rendered by `DB2Dialect`; the final keyset chunk includes its
upper endpoint.

The keyset planner and row iterator are implemented and covered by offline
sqlmock tests. Keyset chunks use the progression `(previousUpper, currentUpper]`
with typed structured bounds. The user has also run the current single-column
keyset path against Db2 LUW and TiDB with `chunk-size = 2`, processing 108
consecutive chunks and locating one real differing field. Real composite-key
execution, checkpoint interruption/resume, and performance still require
separate validation.

Column mapping is currently same-name (case-insensitive) only. Explicit
source-to-target column rename configuration is not implemented in this V1;
tables requiring different names must be renamed or excluded before running.

## Tables without a common unique key

The default `no-unique-key-mode` is `error`: a Db2 table must have a non-null
PRIMARY KEY or UNIQUE KEY with the same ordered columns on TiDB. Automatic
selection checks all TiDB candidates (primary keys first, then unique keys),
and uses the first candidate declared unique and non-null on both endpoints.
Explicit `index-fields` remains strict: a missing, nullable, or non-common
unique key is an error even when fallback is enabled.

Set `no-unique-key-mode = "checksum-only"` only for a table without a common
unique key. Such a table is compared as one full-table, streaming chunk with
no `ORDER BY` on either endpoint. It uses `CanonicalMultisetV1`: each row uses
the existing typed `CanonicalV1` encoding, then two domain-separated SHA-256
row hashes are accumulated modulo 2^256 with the row count. The final SHA-256
includes the version, count, and both sums. This is O(1) extra memory,
order-independent, and duplicate-sensitive. It is a low-collision-probability
multiset checksum, not a mathematical proof of no collision.

When an unordered checksum differs, the report says the row change counts are
unknown and logs that row details are unavailable. The tool does not run
binary search or row comparison and does not generate TiDB repair SQL for that
table, even if `export-fix-sql` is enabled. An interrupted checksum-only scan
restarts the whole table; it has no row-level checkpoint resume. Large tables
therefore require a complete scan on both endpoints on every attempt.

## Type matrix

| Db2 LUW type | V1 behavior |
| --- | --- |
| SMALLINT, INTEGER, BIGINT | Supported, exact integer comparison |
| DECIMAL, NUMERIC | Supported, scale-aware canonical encoding |
| REAL, DOUBLE | Supported; floating tolerance applies only to float types |
| CHAR, VARCHAR, GRAPHIC, VARGRAPHIC | Supported |
| DATE, TIME, TIMESTAMP | Supported |
| BOOLEAN | Supported |
| BINARY, VARBINARY | Supported, byte-preserving |
| BLOB, CLOB, DBCLOB | Supported up to the 16 MiB V1 per-value limit |
| DECFLOAT, XML, ROWID, time-zone types | Rejected before data scanning |

## Evidence levels and limitations

Unit and sqlmock-style tests validate DSNs, catalog SQL, dialect SQL, types,
canonical encoding, and error paths. The user has separately verified a real
Db2 CLI connection, structure comparison, GBK Chinese conversion, and field
difference localization, including the current single-column multi-chunk run
described above.

Consistency is not a distributed snapshot guarantee. V1 requires the source
and target tables to be static for the duration of a comparison. Db2 target,
Db2 sharding, CDC, automatic repair execution, and server-side Db2 checksums
are out of scope. Keyset multi-chunk execution is implemented and has one
user-verified single-column run; checkpoint recovery, repair-SQL execution
acceptance, composite-key real execution, performance, and production readiness
remain unverified.

`checksum-only` behavior has offline unit/sqlmock coverage only. A real Db2
and TiDB run for no-key tables, real restart behavior, performance, and
production readiness remain unverified and must be performed by the user.
