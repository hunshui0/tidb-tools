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

## Real database evidence

No Db2 CLI client, Db2 LUW 11.5 server, or TiDB server is available in this
environment. Therefore no claim is made for a real Db2 connection, catalog
read, cross-database end-to-end comparison, or repair-SQL acceptance. These
remain mandatory release-gate validations.
