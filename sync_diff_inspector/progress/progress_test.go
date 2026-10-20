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

package progress

import (
	"bytes"
	"container/list"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pingcap/tidb-tools/sync_diff_inspector/source/common"
	"github.com/stretchr/testify/require"
)

func TestProgressDynamicTotalsAndLateFinalization(t *testing.T) {
	buf := new(bytes.Buffer)
	p := newTableProgressPrinter(2, 0, buf)
	p.RegisterTable("dynamic", false, false, common.AllTableExistFlag)
	p.StartTable("dynamic", 0, false)
	p.UpdateTotal("dynamic", 1, false)
	p.Inc("dynamic")
	p.UpdateTotal("dynamic", 99, false)
	for i := 0; i < 99; i++ {
		p.Inc("dynamic")
	}
	// All consumers finish before the splitter reports EOF.
	p.UpdateTotal("dynamic", 0, true)
	p.RegisterTable("fixed", false, false, common.AllTableExistFlag)
	p.StartTable("fixed", 1, true)
	p.Inc("fixed")
	p.Close()
	require.Equal(t, 2, p.finishTableNums)
	require.Empty(t, p.tableMap)
	require.Equal(t, 101, p.progress)
	require.Equal(t, 101, p.total)
	require.Contains(t, buf.String(), "0% | tables 0/2")
	require.Contains(t, buf.String(), "50% | tables 1/2")
	require.Contains(t, buf.String(), "100% | tables 2/2 | chunks 101/101")
}

func TestProgressSkippedTablesAndDifferences(t *testing.T) {
	buf := new(bytes.Buffer)
	p := newTableProgressPrinter(5, 0, buf, false)
	p.RegisterTable("bad-structure", true, true, common.AllTableExistFlag)
	p.StartTable("bad-structure", 1, true)
	p.Inc("bad-structure")
	p.RegisterTable("missing-up", true, true, common.UpstreamTableLackFlag)
	p.StartTable("missing-up", 1, true)
	p.Inc("missing-up")
	p.RegisterTable("missing-down", true, true, common.DownstreamTableLackFlag)
	p.StartTable("missing-down", 1, true)
	p.Inc("missing-down")
	p.RegisterTable("bad-data", false, false, common.AllTableExistFlag)
	p.StartTable("bad-data", 1, true)
	p.FailTable("bad-data")
	p.Inc("bad-data")
	p.RegisterTable("both", true, false, common.AllTableExistFlag)
	p.StartTable("both", 1, true)
	p.FailTable("both")
	p.Inc("both")
	p.Close()
	require.Equal(t, 5, p.finishTableNums)
	require.Empty(t, p.tableMap)
	require.Equal(t, 2, p.progress)
	require.Equal(t, 2, p.total)
	require.Equal(t, 5, p.tableFailList.Len())
	p.PrintSummary()
	require.Contains(t, buf.String(), "The structure of both is not equal.")
	require.Contains(t, buf.String(), "The data of both is not equal.")
	require.Contains(t, buf.String(), "Fix SQL export is disabled")
}

func TestProgressResumeAndZeroChunks(t *testing.T) {
	buf := new(bytes.Buffer)
	p := newTableProgressPrinter(3, 2, buf)
	p.RegisterTable("resumed", false, false, common.AllTableExistFlag)
	p.StartTable("resumed", 0, false)
	p.UpdateTotal("resumed", 0, true)
	p.Close()
	require.Equal(t, 3, p.finishTableNums)
	require.Empty(t, p.tableMap)
	require.Contains(t, buf.String(), "66% | tables 2/3")
	require.Contains(t, buf.String(), "100% | tables 3/3")
}

func TestStructureOnlyProgress(t *testing.T) {
	buf := new(bytes.Buffer)
	p := newTableProgressPrinter(1, 0, buf)
	p.RegisterTable("structure-only", false, true, common.AllTableExistFlag)
	p.StartTable("structure-only", 0, true)
	p.Close()
	require.Equal(t, 1, p.finishTableNums)
	require.Contains(t, buf.String(), "100% | tables 1/1")
	require.Contains(t, buf.String(), "Comparing the table data of structure-only ... skipped")
	require.NotContains(t, buf.String(), "Comparing the table data of structure-only ... equivalent")
}

func TestProgressErrorDoesNotCompletePendingTable(t *testing.T) {
	buf := new(bytes.Buffer)
	p := newTableProgressPrinter(1, 0, buf)
	p.StartTable("error", 2, false)
	p.Inc("error")
	p.Error(errors.New("boundary query failed"))
	require.Zero(t, p.finishTableNums)
	require.Contains(t, buf.String(), "boundary query failed")
	require.NotContains(t, buf.String(), "100% | tables")
}

func TestProgressBarBounds(t *testing.T) {
	for _, finished := range []int{-1, 0, 1, 2} {
		buf := new(bytes.Buffer)
		p := &TableProgressPrinter{tableNums: 1, finishTableNums: finished, output: buf}
		require.NotPanics(t, func() { p.flush(false) })
	}
}

func TestTerminalProgressFitsWidthAndIdleTicksDoNotScroll(t *testing.T) {
	for _, width := range []int{30, 40, 60, 80, 120, 200} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			buf := new(bytes.Buffer)
			p := &TableProgressPrinter{output: buf, tableNums: 2868, finishTableNums: 1424,
				progress: 1430, total: 1458, terminalWidth: func() int { return width }}
			p.flush(false)
			first := buf.String()
			for i := 0; i < 100; i++ {
				p.flush(false)
			}
			require.Equal(t, first, buf.String(), "idle refreshes must emit no output")
			p.progress++
			p.finishTableNums++
			p.flush(false)
			// Each redraw starts at column zero, clears only that row and stays
			// strictly within its width. No newline or cursor-up is permitted.
			require.NotContains(t, buf.String(), "\n")
			frames := strings.Split(buf.String(), "\r\x1b[2K")[1:]
			require.Len(t, frames, 2)
			for _, frame := range frames {
				require.Less(t, len(frame), width)
				require.Contains(t, frame, "49%")
			}
			if width >= 60 {
				require.Contains(t, frames[1], "tables 1425/2868 | chunks 1431/1458")
			}
			p.endLine()
			require.True(t, strings.HasSuffix(buf.String(), "\r\n"))
		})
	}
}

func TestTerminalLifecycleMessagesAreNotRepeated(t *testing.T) {
	buf := new(bytes.Buffer)
	p := &TableProgressPrinter{tableList: list.New(), tableNums: 1, terminalWidth: func() int { return 80 }}
	p.init()
	p.output = buf
	p.tableList.PushBack(&TableProgress{name: "example", state: TABLE_STATE_PRESTART})
	p.flush(true)
	p.flush(true)
	p.flush(false)
	require.Equal(t, 1, strings.Count(buf.String(), "Comparing the table structure"))
	require.Equal(t, 1, strings.Count(buf.String(), "Comparing the table data"))
	require.NotContains(t, strings.ReplaceAll(buf.String(), "\r\n", ""), "\n")
	require.NotContains(t, buf.String(), "\x1b[1A")
}

func TestRedirectedProgressIsPlainTextAndNotRepeated(t *testing.T) {
	buf := new(bytes.Buffer)
	p := &TableProgressPrinter{output: buf, tableNums: 2868, finishTableNums: 1424, progress: 1430, total: 1458}
	p.flush(false)
	for i := 0; i < 100; i++ {
		p.flush(false)
	}
	require.Equal(t, "Progress 49% | tables 1424/2868 | chunks 1430/1458\n", buf.String())
	require.NotContains(t, buf.String(), "\x1b")
}

func TestTerminalResizeRedrawsWithinNewWidth(t *testing.T) {
	buf := new(bytes.Buffer)
	width := 120
	p := &TableProgressPrinter{output: buf, tableNums: 2868, finishTableNums: 1424, terminalWidth: func() int { return width }}
	p.flush(false)
	width = 40
	p.flush(false)
	frames := strings.Split(buf.String(), "\r\x1b[2K")[1:]
	require.Len(t, frames, 2)
	require.Less(t, len(frames[1]), width)
}
