# Db2 Source Implementation Evidence

## Stage 0: Baseline

- Branch: `feature/db2-source`
- Base branch: `feature/sync-diff-standalone`
- Base commit: `79f010dcd074dd4fdf380804b57ad7f9c8feb466`
- Recorded on: 2026-09-02

The pre-existing working-tree changes affect 103 tracked files, but
`git diff --ignore-space-at-eol --quiet` exits successfully. They are CRLF/LF
line-ending changes and are deliberately excluded from this implementation.

### Baseline verification

| Command | Result |
| --- | --- |
| `git rev-parse HEAD` | Passed: `79f010dcd074dd4fdf380804b57ad7f9c8feb466` |
| `go version` | Blocked: `/bin/bash: go: command not found` |
| `go test ./sync_diff_inspector/...` | Not started because the Go toolchain is unavailable in this WSL environment. |

The subsequent work uses Db2 LUW 11.5, `github.com/ibmdb/go_ibm_db`, and a
static-source-data consistency requirement for V1. The driver requires the
Db2 CLI native client; real connection and end-to-end results remain separate
from offline test evidence.

## Offline implementation evidence

| Layer | Command | Result |
| --- | --- | --- |
| Db2 config, DSN, catalog, dialect | `go test ./sync_diff_inspector/config ./sync_diff_inspector/db2util` | Passed |
| Canonical protocol | `go test ./sync_diff_inspector/canonical` | Passed |
| Source routing and canonical checksum branch | `go test ./sync_diff_inspector/source` | Passed |
| Typed row comparison | `go test ./sync_diff_inspector/utils` | Passed |
| Full inspector package command | `go test ./sync_diff_inspector/...` | Not clean: `source/common.TestConnect` requires a MySQL/TiDB listener at `127.0.0.1:4000`; parallel package execution also exposed existing shared-output/sqlmock test coupling. Re-running `config` and `source` individually passed. |

`git diff --check` against the complete working tree remains noisy because the
pre-existing CRLF conversion is intentionally preserved. Every implementation
commit is staged from a normalized blob and passed `git diff --cached --check`.

Final targeted verification passed:

```text
go test ./sync_diff_inspector/config ./sync_diff_inspector/db2util \
  ./sync_diff_inspector/canonical ./sync_diff_inspector/source \
  ./sync_diff_inspector/utils
go vet ./sync_diff_inspector/db2util ./sync_diff_inspector/canonical \
  ./sync_diff_inspector/source ./sync_diff_inspector/utils
```

The native-driver probe behaved as expected for this host:

```text
go test -tags db2cli ./sync_diff_inspector/db2util
fatal error: sqlcli1.h: No such file or directory
```

This confirms that the Db2 CLI headers are absent; it is not a successful
native-driver build.

## Real database evidence

No Db2 CLI client, Db2 LUW 11.5 server, or TiDB server is available in this
environment. Therefore no claim is made for a real Db2 connection, catalog
read, cross-database end-to-end comparison, or repair-SQL acceptance. These
remain mandatory release-gate validations.

## Continuation verification (feature/db2-source)

This continuation adds streaming CanonicalV1 hashing, Db2 keyset chunking, and
Windows-safe checkpoint writes. It does not connect to Db2, TiDB, PD, or etcd.

| Command | Result |
| --- | --- |
| `/tmp/go123/bin/go test ./sync_diff_inspector/canonical ./sync_diff_inspector/db2util ./sync_diff_inspector/chunk ./sync_diff_inspector/checkpoints ./sync_diff_inspector/utils` | Passed |
| `/tmp/go123/bin/go test ./sync_diff_inspector/source -run 'TestDB2\\|TestDecodeDB2'` | Passed |
| `/tmp/go123/bin/go test ./sync_diff_inspector/checkpoints -run 'TestSaveChunkWindows\\|TestLoadChunkCorrupt'` | Passed |
| `/tmp/go123/bin/go test ./sync_diff_inspector/source` | Passed (including DB2-targeted tests) |
| `/tmp/go123/bin/go test -tags db2cli ./sync_diff_inspector/db2util` | Not verified successfully: this WSL host has no Db2 CLI `sqlcli.h` headers |
| `/tmp/go123/bin/go test ./sync_diff_inspector/...` | Not clean: existing `source/common.TestConnect` expects a local listener at `127.0.0.1:4000`; no DB2/TiDB service was started |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 /tmp/go123/bin/go build ./sync_diff_inspector` | Passed (ELF binary) |
| `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 /tmp/go123/bin/go build ./sync_diff_inspector` | Passed (PE32+ binary; execution not attempted in WSL) |
| `git diff --check c26c52c^..HEAD` | Passed |

The streaming API retains only one row while producing the same digest as the
batch API. Db2 chunks require a non-null stable key and use `DB2Dialect` for
all range SQL; no MySQL WHERE string is sent to Db2. Different source/target
column names remain unsupported and are rejected by the existing same-name
mapping contract. Real connection, end-to-end comparison, performance, and
production checkpoint evidence remain unverified.
