# DB2 migration CLI

`db2-migrate` is an independent Go module for invoking a DB2 comparison and
TiDB Lightning migration workflow. It does not import the repository's
`sync_diff_inspector`; `compare` invokes that program as a child process.

Build from this directory:

```sh
go build -o ./bin/db2-migrate ./cmd/db2-migrate
```

Copy `config.example.toml` to a local wrapper configuration if desired and pass
it with `--cli-config`. Its `[tools]` settings are never written into the TiDB
Lightning task TOML.

```sh
./bin/db2-migrate compare --binary ../bin/sync_diff_inspector --config ../sync_diff_inspector/config/config_db2.toml
./bin/db2-migrate lightning init --config ./tidb-lightning.toml --data-dir /data/db2-export --backend tidb
./bin/db2-migrate lightning run --config ./tidb-lightning.toml --toolkit-dir ../toolkit
```

`lightning run` locates Lightning in this order: `--binary`, `--toolkit-dir`,
`TIDB_TOOLKIT_DIR`, wrapper configuration, `PATH`, then `tiup tidb-lightning`.
Toolkit searches reject Windows `.exe` files, non-executable files, and
ambiguous multiple candidates. No toolkit is downloaded or installed.
