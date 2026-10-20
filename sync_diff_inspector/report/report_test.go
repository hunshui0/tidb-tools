// Copyright 2021 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/chunk"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/config"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/source/common"
	"github.com/stretchr/testify/require"
)

func testReport(t *testing.T, names ...string) *Report {
	t.Helper()
	r := NewReport(&config.TaskConfig{OutputDir: t.TempDir(), FixDir: "fix"})
	tables := make([]*common.TableDiff, 0, len(names))
	for _, name := range names {
		tables = append(tables, &common.TableDiff{Schema: "test", Table: name})
	}
	r.Init(tables, [][]byte{[]byte("host = source\n")}, []byte("host = target\n"))
	return r
}

func checked(r *Report, name string, structure, data bool, index int) {
	r.SetTableStructCheckResult("test", name, structure, false, common.AllTableExistFlag)
	r.SetTableDataCheckResult("test", name, data, 0, 0, 10, 10, &chunk.ChunkID{TableIndex: index})
}

func TestSummaryIndependentStatuses(t *testing.T) {
	r := testReport(t, "both_equal", "data_diff", "struct_diff", "both_diff", "skip_data", "missing_up", "missing_down", "error", "unchecked")
	checked(r, "both_equal", true, true, 0)
	checked(r, "data_diff", true, false, 1)
	checked(r, "struct_diff", false, true, 2)
	checked(r, "both_diff", false, false, 3)
	r.SetTableStructCheckResult("test", "skip_data", false, true, common.AllTableExistFlag)
	r.SetTableStructCheckResult("test", "missing_up", false, true, common.UpstreamTableLackFlag)
	r.SetTableStructCheckResult("test", "missing_down", false, true, common.DownstreamTableLackFlag)
	checked(r, "error", true, true, 7)
	r.SetTableMeetError("test", "error", errors.New("query timed out"))
	require.NoError(t, r.CommitSummary())
	require.EqualValues(t, 1, r.PassNum)
	require.EqualValues(t, 4, r.FailedNum)
	require.EqualValues(t, 2, r.SkippedNum)
	require.EqualValues(t, 1, r.ErrorNum)
	require.EqualValues(t, 1, r.UncheckedNum)
	require.Equal(t, Error, r.Result)

	expected := map[string][]string{
		"both_equal":   {Pass, "equal", "equal"},
		"data_diff":    {Fail, "equal", "different"},
		"struct_diff":  {Fail, "different", "equal"},
		"both_diff":    {Fail, "different", "different"},
		"skip_data":    {Fail, "different", "skipped"},
		"missing_up":   {"skipped", "missing", "skipped"},
		"missing_down": {"skipped", "missing", "skipped"},
		"error":        {Error, "equal", "error"},
		"unchecked":    {"not_checked", "not_checked", "not_checked"},
	}
	raw, err := os.ReadFile(filepath.Join(r.task.OutputDir, "summary.txt"))
	require.NoError(t, err)
	text := string(raw)
	for name, statuses := range expected {
		// Verify the actual generated file, independently of internal row helpers.
		var actual []string
		for _, line := range strings.Split(text, "\n") {
			cells := strings.Split(line, "|")
			if len(cells) == 9 && strings.TrimSpace(cells[1]) == "`test`.`"+name+"`" {
				actual = []string{strings.TrimSpace(cells[2]), strings.TrimSpace(cells[3]), strings.TrimSpace(cells[4])}
				break
			}
		}
		require.Equal(t, statuses, actual, name)
	}
	require.Contains(t, text, "Structure equal: 3; 数据一致 / Data equal: 2")
	require.Contains(t, text, "missing upstream")
	require.Contains(t, text, "missing downstream")
	require.Contains(t, text, "query timed out")
	require.NotContains(t, text, "succeed")
	rows := r.tableRows()
	for i := 1; i < len(rows); i++ {
		require.Less(t, rows[i-1][0], rows[i][0])
	}
}

func TestCheckModes(t *testing.T) {
	for _, mode := range []struct {
		name                 string
		structOnly, dataOnly bool
		structure, data      string
	}{
		{"structure", true, false, "equal", "skipped"},
		{"data", false, true, "skipped", "equal"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			r := testReport(t, "tbl")
			r.SetCheckMode(mode.structOnly, mode.dataOnly)
			if mode.structOnly {
				r.SetTableStructCheckResult("test", "tbl", true, false, 0)
			}
			if mode.dataOnly {
				r.SetTableDataCheckResult("test", "tbl", true, 0, 0, 2, 2, &chunk.ChunkID{})
			}
			require.NoError(t, r.CommitSummary())
			require.Equal(t, Pass, r.Result)
			require.Equal(t, []string{Pass, mode.structure, mode.data}, r.tableRows()[0][1:4])
		})
	}
}

func TestPrintResultsAndFixSQL(t *testing.T) {
	r := testReport(t, "tbl")
	checked(r, "tbl", true, true, 0)
	var buf bytes.Buffer
	require.NoError(t, r.Print(&buf))
	require.Contains(t, buf.String(), "1 passed, 0 failed")
	require.Contains(t, buf.String(), "summary.txt")
	r.SetTableMeetError("test", "tbl", errors.New("checksum failed"))
	buf.Reset()
	require.NoError(t, r.Print(&buf))
	require.Contains(t, buf.String(), "structure=equal, data=error (checksum failed)")
	require.Contains(t, buf.String(), "0 passed, 0 failed, 0 skipped, 1 errors")
	require.NotContains(t, buf.String(), "all are equal")
	require.Contains(t, buf.String(), "No patch file was generated.")
	r.MarkFixSQLGenerated()
	buf.Reset()
	require.NoError(t, r.Print(&buf))
	require.Contains(t, buf.String(), "The patch file has been generated")
	r.exportFixSQL = false
	buf.Reset()
	require.NoError(t, r.Print(&buf))
	require.Contains(t, buf.String(), "Fix SQL export is disabled")
	require.NotContains(t, buf.String(), "The patch file has been generated")
}

func TestChecksumOnlyMismatchReportsUnknownRows(t *testing.T) {
	r := testReport(t, "nokey")
	r.SetTableStructCheckResult("test", "nokey", true, false, 0)
	r.SetTableChecksumOnlyResult("test", "nokey", false, 7, 9, &chunk.ChunkID{})
	require.True(t, r.TableResults["test"]["nokey"].RowsUnknown)
	require.Contains(t, r.getDiffRows()[0], "unknown/unknown")
	require.NoError(t, r.CommitSummary())
	raw, err := os.ReadFile(filepath.Join(r.task.OutputDir, "summary.txt"))
	require.NoError(t, err)
	require.Contains(t, string(raw), "unknown/unknown")
	var buf bytes.Buffer
	require.NoError(t, r.Print(&buf))
	require.Contains(t, buf.String(), "No patch file was generated.")
	require.Contains(t, buf.String(), "Checksum-only differences have no row details and cannot generate fix SQL.")
}

func TestSnapshotPreservesCompletedTablesAndFiltersFutureChunks(t *testing.T) {
	// Input order deliberately differs from schema/table-name ordering.
	r := testReport(t, "z_first", "a_current", "m_future", "missing")
	checked(r, "z_first", true, false, 0)
	r.SetTableStructCheckResult("test", "a_current", true, false, 0)
	r.SetTableDataCheckResult("test", "a_current", true, 0, 0, 20, 20, &chunk.ChunkID{TableIndex: 1, ChunkIndex: 0})
	r.SetTableDataCheckResult("test", "a_current", false, 2, 3, 30, 31, &chunk.ChunkID{TableIndex: 1, ChunkIndex: 2})
	checked(r, "m_future", true, false, 2)
	r.SetTableStructCheckResult("test", "missing", false, true, common.UpstreamTableLackFlag)
	r.MarkFixSQLGenerated()
	snapshot, err := r.GetSnapshot(&chunk.ChunkID{TableIndex: 1, ChunkIndex: 0}, "test", "a_current")
	require.NoError(t, err)
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	var decoded Report
	require.NoError(t, json.Unmarshal(raw, &decoded))
	restored := testReport(t, "z_first", "a_current", "m_future", "missing")
	restored.LoadReport(&decoded)
	require.True(t, restored.FixSQLGenerated)
	require.Equal(t, []string{Fail, "equal", "different"}, restored.tableRows()[3][1:4])
	current := restored.TableResults["test"]["a_current"]
	require.True(t, current.DataEqual)
	require.True(t, current.DataChecked)
	require.EqualValues(t, 20, current.UpCount)
	require.EqualValues(t, 20, current.DownCount)
	require.Len(t, current.ChunkMap, 1)
	future := restored.TableResults["test"]["m_future"]
	require.False(t, future.DataChecked)
	require.Zero(t, future.UpCount)
	require.True(t, restored.TableResults["test"]["missing"].DataSkip)
	require.Equal(t, common.UpstreamTableLackFlag, restored.TableResults["test"]["missing"].TableLack)
	// Reprocessing excluded chunks restores the exact counts, without double counting.
	restored.SetTableDataCheckResult("test", "a_current", false, 2, 3, 30, 31, &chunk.ChunkID{TableIndex: 1, ChunkIndex: 2})
	require.EqualValues(t, 50, current.UpCount)
	require.EqualValues(t, 51, current.DownCount)
}

func TestSnapshotPersistsErrorsAndUnknownRows(t *testing.T) {
	r := testReport(t, "error", "nokey")
	r.SetTableStructCheckResult("test", "error", true, false, 0)
	r.SetTableMeetError("test", "error", errors.New("read failed"))
	r.SetTableDataCheckResult("test", "error", false, 0, 0, 0, 0, &chunk.ChunkID{})
	r.SetTableStructCheckResult("test", "nokey", true, false, 0)
	r.SetTableChecksumOnlyResult("test", "nokey", false, 4, 5, &chunk.ChunkID{TableIndex: 1})
	snapshot, err := r.GetSnapshot(&chunk.ChunkID{TableIndex: 1}, "test", "nokey")
	require.NoError(t, err)
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	var decoded Report
	require.NoError(t, json.Unmarshal(raw, &decoded))
	restored := testReport(t, "error", "nokey")
	restored.LoadReport(&decoded)
	require.EqualError(t, restored.TableResults["test"]["error"].MeetError, "read failed")
	require.True(t, restored.TableResults["test"]["nokey"].RowsUnknown)
	require.NoError(t, restored.CommitSummary())
	require.Equal(t, Error, restored.Result)
}

func TestSetTableMeetErrorBeforeInitialization(t *testing.T) {
	r := NewReport(&config.TaskConfig{OutputDir: t.TempDir()})
	r.SetTableMeetError("s", "first", errors.New("first error"))
	r.SetTableMeetError("s", "second", errors.New("second error"))
	require.NoError(t, r.CommitSummary())
	require.EqualValues(t, 2, r.ErrorNum)
	require.Equal(t, Error, r.Result)
}

func TestCalculateTotalSize(t *testing.T) {
	r := testReport(t, "tbl")
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery("select sum.*").WillReturnRows(sqlmock.NewRows([]string{"data"}).AddRow("123"))
	r.CalculateTotalSize(context.Background(), db)
	require.EqualValues(t, 123, r.TotalSize)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCommitSummaryWriteError(t *testing.T) {
	r := testReport(t, "tbl")
	r.task.OutputDir = filepath.Join(r.task.OutputDir, "missing")
	require.Error(t, r.CommitSummary())
}
