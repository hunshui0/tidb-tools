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
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/pingcap/errors"
	"github.com/pingcap/log"
	"github.com/pingcap/tidb-tools/pkg/dbutil"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/chunk"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/config"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/source/common"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/utils"
	"go.uber.org/zap"
)

const (
	// Pass means all requested checks passed (unrequested checks are skipped).
	Pass = "pass"
	// Fail means not all data or struct of tables are equal
	Fail  = "fail"
	Error = "error"
)

// ReportConfig stores the config information for the user
type ReportConfig struct {
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	User     string `toml:"user"`
	Snapshot string `toml:"snapshot,omitempty"`
	SqlMode  string `toml:"sql-mode,omitempty"`
}

// TableResult saves the check result for every table.
type TableResult struct {
	Schema        string                  `json:"schema"`
	Table         string                  `json:"table"`
	StructEqual   bool                    `json:"struct-equal"`
	StructChecked bool                    `json:"struct-checked,omitempty"`
	DataChecked   bool                    `json:"data-checked,omitempty"`
	DataSkip      bool                    `json:"data-skip"`
	DataEqual     bool                    `json:"data-equal"`
	MeetError     error                   `json:"-"`
	ChunkMap      map[string]*ChunkResult `json:"chunk-result"` // `ChunkMap` stores the `ChunkResult` of each chunk of the table
	UpCount       int64                   `json:"up-count"`     // `UpCount` is the number of rows in the table from upstream
	DownCount     int64                   `json:"down-count"`   // `DownCount` is the number of rows in the table from downstream
	TableLack     int                     `json:"table-lack"`
	RowsUnknown   bool                    `json:"rows-unknown,omitempty"`
}

// ChunkResult save the necessarily information to provide summary information
type ChunkResult struct {
	RowsAdd    int    `json:"rows-add"`    // `RowsAdd` is the number of rows needed to add
	RowsDelete int    `json:"rows-delete"` // `RowsDelete` is the number of rows needed to delete
	Equal      bool   `json:"equal"`
	UpCount    int64  `json:"up-count"`
	DownCount  int64  `json:"down-count"`
	Error      string `json:"error,omitempty"`
}

// Report saves the check results.
type Report struct {
	sync.RWMutex
	Result       string                             `json:"-"` // Result is pass or fail
	PassNum      int32                              `json:"-"` // The pass number of tables
	FailedNum    int32                              `json:"-"` // The failed number of tables
	SkippedNum   int32                              `json:"-"` // The skipped number of tables
	ErrorNum     int32                              `json:"-"`
	UncheckedNum int32                              `json:"-"`
	TableResults map[string]map[string]*TableResult `json:"table-results"` // TableResult saved the map of  `schema` => `table` => `tableResult`
	StartTime    time.Time                          `json:"start-time"`
	Duration     time.Duration                      `json:"time-duration"`
	TotalSize    int64                              `json:"-"` // Total size of the checked tables
	SourceConfig [][]byte                           `json:"-"`
	TargetConfig []byte                             `json:"-"`
	// FixSQLGenerated records whether at least one repair SQL file was actually
	// written. Export being enabled alone does not imply that a patch exists.
	FixSQLGenerated bool `json:"fix-sql-generated,omitempty"`

	task            *config.TaskConfig `json:"-"`
	exportFixSQL    bool               `json:"-"`
	checkStructOnly bool
	checkDataOnly   bool
}

// SetCheckMode records which comparisons were requested. A skipped check must
// never be reported as equal just because its initial boolean value was true.
func (r *Report) SetCheckMode(structOnly, dataOnly bool) {
	r.checkStructOnly, r.checkDataOnly = structOnly, dataOnly
}

func (r *Report) tableStatus(result *TableResult) (overall, structure, data string) {
	structure, data = "not_checked", "not_checked"
	if !common.AllTableExist(result.TableLack) {
		return "skipped", "missing", "skipped"
	}
	if r.checkDataOnly {
		structure = "skipped"
	} else if result.StructChecked {
		structure = "equal"
		if !result.StructEqual {
			structure = "different"
		}
	}
	if r.checkStructOnly || result.DataSkip {
		data = "skipped"
	} else if result.MeetError != nil {
		data = "error"
	} else if result.DataChecked {
		data = "equal"
		if !result.DataEqual {
			data = "different"
		}
	}
	switch {
	case result.MeetError != nil:
		overall = Error
	case structure == "different" || data == "different":
		overall = Fail
	case structure == "not_checked" || data == "not_checked":
		overall = "not_checked"
	case data == "skipped" && !r.checkStructOnly:
		overall = "skipped"
	default:
		overall = Pass
	}
	return
}

// LoadReport loads the report from the checkpoint
func (r *Report) LoadReport(reportInfo *Report) {
	r.StartTime = time.Now()
	r.Duration = reportInfo.Duration
	r.TotalSize = reportInfo.TotalSize
	r.FixSQLGenerated = reportInfo.FixSQLGenerated
	for schema, tableMap := range reportInfo.TableResults {
		if _, ok := r.TableResults[schema]; !ok {
			r.TableResults[schema] = make(map[string]*TableResult)
		}
		for table, result := range tableMap {
			// Errors are serialized per chunk; the error interface itself is not.
			for _, c := range result.ChunkMap {
				if c.Error != "" {
					result.MeetError = errors.New(c.Error)
				}
			}
			r.TableResults[schema][table] = result
		}
	}
}

func (r *Report) getDiffRows() [][]string {
	diffRows := make([][]string, 0)
	for schema, tableMap := range r.TableResults {
		for table, result := range tableMap {
			overall, structure, data := r.tableStatus(result)
			if overall == Pass {
				continue
			}
			diffRow := make([]string, 0)
			diffRow = append(diffRow, dbutil.TableName(schema, table))
			diffRow = append(diffRow, overall, structure, data)
			rowsAdd, rowsDelete := 0, 0
			for _, chunkResult := range result.ChunkMap {
				rowsAdd += chunkResult.RowsAdd
				rowsDelete += chunkResult.RowsDelete
			}
			change := fmt.Sprintf("+%d/-%d", rowsAdd, rowsDelete)
			if result.RowsUnknown {
				change = "unknown/unknown"
			}
			if data != "equal" && data != "different" {
				change = "N/A"
			}
			diffRow = append(diffRow, change, strconv.FormatInt(result.UpCount, 10), strconv.FormatInt(result.DownCount, 10))
			diffRows = append(diffRows, diffRow)
		}
	}
	sort.Slice(diffRows, func(i, j int) bool { return diffRows[i][0] < diffRows[j][0] })
	return diffRows
}

func (r *Report) summarize() {
	r.PassNum, r.FailedNum, r.SkippedNum, r.ErrorNum, r.UncheckedNum = 0, 0, 0, 0, 0
	for _, tables := range r.TableResults {
		for _, result := range tables {
			overall, _, _ := r.tableStatus(result)
			switch overall {
			case Pass:
				r.PassNum++
			case Fail:
				r.FailedNum++
			case Error:
				r.ErrorNum++
			case "skipped":
				r.SkippedNum++
			default:
				r.UncheckedNum++
			}
		}
	}
	r.Result = Pass
	if r.FailedNum > 0 {
		r.Result = Fail
	}
	if r.ErrorNum > 0 || r.UncheckedNum > 0 {
		r.Result = Error
	}
}

// tableRows includes every table and keeps the two check results independent.
func (r *Report) tableRows() [][]string {
	rows := make([][]string, 0)
	for schema, tables := range r.TableResults {
		for name, result := range tables {
			overall, structure, data := r.tableStatus(result)
			detail := ""
			switch result.TableLack {
			case common.UpstreamTableLackFlag:
				detail = "missing upstream"
			case common.DownstreamTableLackFlag:
				detail = "missing downstream"
			}
			if result.MeetError != nil {
				detail = result.MeetError.Error()
			}
			rows = append(rows, []string{dbutil.TableName(schema, name), overall, structure, data,
				strconv.FormatInt(result.UpCount, 10), strconv.FormatInt(result.DownCount, 10), detail})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
	return rows
}

// CalculateTotalSize calculate the total size of all the checked tables
// Notice, user should run the analyze table first, when some of tables' size are zero.
func (r *Report) CalculateTotalSize(ctx context.Context, db *sql.DB) {
	for schema, tableMap := range r.TableResults {
		for table := range tableMap {
			size, err := utils.GetTableSize(ctx, db, schema, table)
			if size == 0 || err != nil {
				log.Warn("fail to get the correct size of table, if you want to get the correct size, please analyze the corresponding tables", zap.String("table", dbutil.TableName(schema, table)), zap.Error(err))
			} else {
				r.TotalSize += size
			}
		}
	}
}

// CommitSummary commit summary info
func (r *Report) CommitSummary() error {
	r.summarize()
	var summary strings.Builder
	summary.WriteString("Summary\n\nSource Database\n\n")
	for _, source := range r.SourceConfig {
		summary.Write(source)
		summary.WriteString("\n")
	}
	summary.WriteString("Target Database\n\n")
	summary.Write(r.TargetConfig)
	summary.WriteString("\nComparison Result / 逐表比较结果\n\n")
	summary.WriteString(fmt.Sprintf("Tables: %d | pass: %d | fail: %d | skipped: %d | error: %d | not_checked: %d\n\n",
		r.PassNum+r.FailedNum+r.SkippedNum+r.ErrorNum+r.UncheckedNum,
		r.PassNum, r.FailedNum, r.SkippedNum, r.ErrorNum, r.UncheckedNum))
	summary.WriteString("Structure 和 Data 独立表示结构、数据的比较结论。\n")
	summary.WriteString("equal=一致; different=不一致; skipped=跳过; missing=缺表; error=出错; not_checked=未完成检查。\n")
	summary.WriteString("pass 表示本次要求的检查均通过；只检查结构或数据时，另一项标记为 skipped。\n")
	summary.WriteString("UpCount/DownCount 为已比较分块的累计行数；跳过、未完成或出错时不代表完整表行数。\n\n")
	table := tablewriter.NewWriter(&summary)
	table.SetHeader([]string{"Table", "Result", "Structure", "Data", "UpCount", "DownCount", "Details"})
	table.SetAutoWrapText(false)
	table.AppendBulk(r.tableRows())
	table.Render()

	structureEqual, dataEqual := 0, 0
	for _, tables := range r.TableResults {
		for _, result := range tables {
			_, structure, data := r.tableStatus(result)
			if structure == "equal" {
				structureEqual++
			}
			if data == "equal" {
				dataEqual++
			}
		}
	}
	summary.WriteString(fmt.Sprintf("\n结构一致 / Structure equal: %d; 数据一致 / Data equal: %d\n", structureEqual, dataEqual))
	if diffRows := r.getDiffRows(); len(diffRows) > 0 {
		summary.WriteString("\nDifferences, skipped and incomplete checks / 差异及未完成检查\n\n")
		table := tablewriter.NewWriter(&summary)
		table.SetHeader([]string{"Table", "Result", "Structure", "Data", "Data diff rows", "UpCount", "DownCount"})
		table.AppendBulk(diffRows)
		table.Render()
		summary.WriteString("unknown/unknown: checksum-only 校验不一致，无法定位差异行或生成修复 SQL。N/A: 未得出数据比较结论。\n")
	}
	duration := r.Duration + time.Since(r.StartTime)
	summary.WriteString(fmt.Sprintf("\nTime Cost: %s\n", duration))
	if duration > 0 {
		summary.WriteString(fmt.Sprintf("Average Speed: %fMB/s\n", float64(r.TotalSize)/(1024.0*1024.0*duration.Seconds())))
	}
	return errors.Trace(os.WriteFile(filepath.Join(r.task.OutputDir, "summary.txt"), []byte(summary.String()), 0o644))
}

func (r *Report) Print(w io.Writer) error {
	r.summarize()
	var summary strings.Builder
	if r.ErrorNum > 0 || r.UncheckedNum > 0 {
		summary.WriteString("Error or incomplete checks in comparison process:\n")
	}
	for _, row := range r.tableRows() {
		if row[1] == Pass {
			continue
		}
		summary.WriteString(fmt.Sprintf("%s: result=%s, structure=%s, data=%s", row[0], row[1], row[2], row[3]))
		if row[6] != "" {
			summary.WriteString(" (" + row[6] + ")")
		}
		summary.WriteString("\n")
	}
	summary.WriteString(fmt.Sprintf("A total of %d tables: %d passed, %d failed, %d skipped, %d errors, %d not checked.\n",
		r.PassNum+r.FailedNum+r.SkippedNum+r.ErrorNum+r.UncheckedNum,
		r.PassNum, r.FailedNum, r.SkippedNum, r.ErrorNum, r.UncheckedNum))
	if r.FailedNum > 0 || r.ErrorNum > 0 || r.SkippedNum > 0 || r.UncheckedNum > 0 {
		if !r.exportFixSQL {
			summary.WriteString("Fix SQL export is disabled; differences were recorded without exporting SQL.\n")
		} else if r.FixSQLGenerated {
			summary.WriteString(fmt.Sprintf("The patch file has been generated in \n\t'%s/'\n", r.task.FixDir))
		} else {
			summary.WriteString("No patch file was generated.\n")
		}
		if r.hasChecksumOnlyMismatch() {
			summary.WriteString("Checksum-only differences have no row details and cannot generate fix SQL.\n")
		}
	}
	summary.WriteString(fmt.Sprintf("Per-table structure and data results: '%s'\n", filepath.Join(r.task.OutputDir, "summary.txt")))
	summary.WriteString(fmt.Sprintf("You can view the comparison details through '%s/%s'\n", r.task.OutputDir, config.LogFileName))
	_, err := fmt.Fprint(w, summary.String())
	return err
}

// MarkFixSQLGenerated records that a repair SQL file was successfully written.
func (r *Report) MarkFixSQLGenerated() {
	r.Lock()
	defer r.Unlock()
	r.FixSQLGenerated = true
}

func (r *Report) hasChecksumOnlyMismatch() bool {
	for _, tableMap := range r.TableResults {
		for _, result := range tableMap {
			if result.RowsUnknown && !result.DataEqual {
				return true
			}
		}
	}
	return false
}

// NewReport returns a new Report.
func NewReport(task *config.TaskConfig, exportFixSQL ...bool) *Report {
	return &Report{
		TableResults: make(map[string]map[string]*TableResult),
		Result:       Pass,
		task:         task,
		exportFixSQL: len(exportFixSQL) == 0 || exportFixSQL[0],
	}
}

func (r *Report) Init(tableDiffs []*common.TableDiff, sourceConfig [][]byte, targetConfig []byte) {
	r.StartTime = time.Now()
	r.SourceConfig = sourceConfig
	r.TargetConfig = targetConfig
	for _, tableDiff := range tableDiffs {
		schema, table := tableDiff.Schema, tableDiff.Table
		if _, ok := r.TableResults[schema]; !ok {
			r.TableResults[schema] = make(map[string]*TableResult)
		}
		r.TableResults[schema][table] = &TableResult{
			Schema:      schema,
			Table:       table,
			StructEqual: true,
			DataSkip:    tableDiff.IgnoreDataCheck,
			TableLack:   tableDiff.TableLack,
			DataEqual:   true,
			MeetError:   nil,
			ChunkMap:    make(map[string]*ChunkResult),
		}
	}
}

// SetTableStructCheckResult sets the struct check result for table.
func (r *Report) SetTableStructCheckResult(schema, table string, equal bool, skip bool, exist int) {
	r.Lock()
	defer r.Unlock()
	tableResult := r.TableResults[schema][table]
	tableResult.StructChecked = true
	tableResult.StructEqual = equal
	tableResult.DataSkip = skip
	tableResult.TableLack = exist
	if !equal && common.AllTableExist(tableResult.TableLack) && r.Result != Error {
		r.Result = Fail
	}
}

// SetTableDataCheckResult sets the data check result for table.
func (r *Report) SetTableDataCheckResult(schema, table string, equal bool, rowsAdd, rowsDelete int, upCount, downCount int64, id *chunk.ChunkID) {
	r.Lock()
	defer r.Unlock()
	result := r.TableResults[schema][table]
	result.DataChecked = true
	result.UpCount += upCount
	result.DownCount += downCount
	if result.ChunkMap == nil {
		result.ChunkMap = make(map[string]*ChunkResult)
	}
	chunkResult, ok := result.ChunkMap[id.ToString()]
	if !ok {
		chunkResult = &ChunkResult{Equal: true}
		result.ChunkMap[id.ToString()] = chunkResult
	}
	chunkResult.Equal = chunkResult.Equal && equal
	chunkResult.UpCount += upCount
	chunkResult.DownCount += downCount
	if result.MeetError != nil {
		chunkResult.Error = result.MeetError.Error()
	}
	if !equal {
		result.DataEqual = equal
		chunkResult.RowsAdd += rowsAdd
		chunkResult.RowsDelete += rowsDelete
		if r.Result != Error && common.AllTableExist(result.TableLack) {
			r.Result = Fail
		}
	}
	if !equal && common.AllTableExist(result.TableLack) && r.Result != Error {
		r.Result = Fail
	}
}

// SetTableChecksumOnlyResult records an unordered checksum mismatch. Row-level
// additions/deletions are intentionally unknown and no fix SQL is available.
func (r *Report) SetTableChecksumOnlyResult(schema, table string, equal bool, upCount, downCount int64, id *chunk.ChunkID) {
	r.SetTableDataCheckResult(schema, table, equal, 0, 0, upCount, downCount, id)
	r.Lock()
	defer r.Unlock()
	if result := r.TableResults[schema][table]; result != nil {
		result.RowsUnknown = !equal
	}
}

// SetTableMeetError sets meet error when check the table.
func (r *Report) SetTableMeetError(schema, table string, err error) {
	r.Lock()
	defer r.Unlock()
	if _, ok := r.TableResults[schema]; !ok {
		r.TableResults[schema] = make(map[string]*TableResult)
	}
	if r.TableResults[schema][table] == nil {
		r.TableResults[schema][table] = &TableResult{Schema: schema, Table: table, ChunkMap: make(map[string]*ChunkResult)}
	}
	r.TableResults[schema][table].MeetError = err
	r.Result = Error
}

// GetSnapshot get the snapshot of the current state of the report, then we can restart the
// sync-diff and get the correct report state.
func (r *Report) GetSnapshot(chunkID *chunk.ChunkID, schema, table string) (*Report, error) {
	r.RLock()
	defer r.RUnlock()
	// Keep structure metadata for every table. Data totals must only include
	// chunks at or before the checkpoint, regardless of table-name ordering.
	reserveMap := make(map[string]map[string]*TableResult)
	for schema, tableMap := range r.TableResults {
		reserveMap[schema] = make(map[string]*TableResult)
		for table, result := range tableMap {
			saved := &TableResult{
				Schema: result.Schema, Table: result.Table,
				StructEqual: result.StructEqual, StructChecked: result.StructChecked,
				DataSkip: result.DataSkip, TableLack: result.TableLack,
				DataEqual: true, ChunkMap: make(map[string]*ChunkResult),
			}
			for id, chunkResult := range result.ChunkMap {
				sid := new(chunk.ChunkID)
				if err := sid.FromString(id); err != nil {
					return nil, errors.Trace(err)
				}
				if sid.Compare(chunkID) > 0 {
					continue
				}
				copied := *chunkResult
				saved.ChunkMap[id] = &copied
				saved.DataChecked = true
				saved.DataEqual = saved.DataEqual && copied.Equal
				saved.UpCount += copied.UpCount
				saved.DownCount += copied.DownCount
				if copied.Error != "" {
					saved.MeetError = errors.New(copied.Error)
				}
			}
			saved.RowsUnknown = result.RowsUnknown && saved.DataChecked && !saved.DataEqual
			reserveMap[schema][table] = saved
		}
	}

	result := r.Result
	totalSize := r.TotalSize
	duration := time.Since(r.StartTime)
	task := r.task
	return &Report{
		PassNum:         0,
		FailedNum:       0,
		Result:          result,
		TableResults:    reserveMap,
		StartTime:       r.StartTime,
		Duration:        duration,
		TotalSize:       totalSize,
		FixSQLGenerated: r.FixSQLGenerated,

		task: task,
	}, nil
}
