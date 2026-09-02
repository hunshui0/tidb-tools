# sync-diff standalone branch

This branch is a focused development base for `sync_diff_inspector`, retained
from [`tidb-tools`](https://github.com/pingcap/tidb-tools). It preserves the
existing MySQL/TiDB comparison behavior and its required internal packages so
that a DB2 source can be added incrementally.

The Go module path deliberately remains `github.com/pingcap/tidb-tools` on
this branch. Existing internal imports and version metadata therefore continue
to resolve locally without a broad, unverified import-path rewrite.

## Build and test

```text
make build
make test
```

The binary is written to `bin/sync_diff_inspector`. See
[`sync_diff_inspector/README.md`](sync_diff_inspector/README.md) for usage and
configuration examples.

## Retained internal packages

- `pkg/dbutil`
- `pkg/filter`
- `pkg/table-filter`
- `pkg/table-rule-selector`
- `pkg/column-mapping`
- `pkg/utils`
- `pkg/schemacmp` (test support for `pkg/dbutil`)

## License

Apache 2.0. See [LICENSE](LICENSE).
