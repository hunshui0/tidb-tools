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

The selected driver is `github.com/ibmdb/go_ibm_db v0.5.4`. It requires the
Db2 CLI native client. Building without `-tags db2cli` remains supported for
offline tests, but starting a Db2 source returns an error explaining the
missing build tag and native environment.

Use [config_db2.toml](./config/config_db2.toml) as the minimum configuration.
Db2 unquoted identifiers are normalized to upper case. Quoted identifiers keep
their case. `database` and `schema` are mandatory for a Db2 source.

## Comparison protocol

Db2 and TiDB do not compare their server-side hashes. Both endpoints use
`CanonicalV1`, a Go-side SHA-256 digest with a versioned, typed,
length-delimited row encoding. The protocol distinguishes NULL and empty
values; preserves integer and decimal precision; normalizes timestamps to UTC;
trims trailing spaces for CHAR; preserves binary bytes; and caps LOB values at
16 MiB per value. A digest with a different algorithm version is never treated
as equal.

For V1, Db2 tables are scanned as a single stable chunk. A primary key or a
non-null unique key is preferred; set `index-fields` when no such key exists.
Legacy string `range` expressions are rejected for a Db2 source, because they
are MySQL syntax. The new structured range/Dialect package is checkpoint-safe,
but Db2 multi-chunk planning is intentionally deferred until it has real Db2
ordering and checkpoint-resume evidence.

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
canonical encoding, and error paths. They are not evidence of a working Db2
connection. This repository has no real Db2 LUW 11.5 or TiDB instance, so
native-client startup, catalog compatibility, authentication/TLS, and complete
end-to-end repair SQL output remain unverified.

Consistency is not a distributed snapshot guarantee. V1 requires the source
and target tables to be static for the duration of a comparison. Db2 target,
Db2 sharding, CDC, automatic repair execution, server-side Db2 checksums, and
Db2 multi-chunk splitting are out of scope.
