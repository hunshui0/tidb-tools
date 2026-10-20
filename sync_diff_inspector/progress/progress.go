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
	"container/list"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pingcap/tidb-tools/sync_diff_inspector/source/common"
	"golang.org/x/term"
)

type TableProgressPrinter struct {
	tableList     *list.List
	tableFailList *list.List
	tableMap      map[string]*list.Element
	output        io.Writer
	lastProgress  string
	lineVisible   bool
	terminalWidth func() int // optional override for terminal rendering tests

	finishTableNums int
	tableNums       int

	progress     int
	total        int
	exportFixSQL bool

	optCh    chan Operator
	finishCh chan struct{}
}

type table_state_t int

const (
	TABLE_STATE_REGISTER                       table_state_t = 0x1
	TABLE_STATE_PRESTART                       table_state_t = 0x2
	TABLE_STATE_COMPARING                      table_state_t = 0x4
	TABLE_STATE_FINISH                         table_state_t = 0x8
	TABLE_STATE_RESULT_OK                      table_state_t = 0x00
	TABLE_STATE_RESULT_FAIL_STRUCTURE_DONE     table_state_t = 0x10
	TABLE_STATE_RESULT_FAIL_STRUCTURE_CONTINUE table_state_t = 0x20
	TABLE_STATE_RESULT_FAIL_STRUCTURE_PASS     table_state_t = 0x40
	TABLE_STATE_RESULT_DIFFERENT               table_state_t = 0x80
	TABLE_STATE_HEAD                           table_state_t = 0xff
	TABLE_STATE_RESULT_MASK                    table_state_t = 0xff0
	TABLE_STATE_NOT_EXSIT_UPSTREAM             table_state_t = 0x100
	TABLE_STATE_NOT_EXSIT_DOWNSTREAM           table_state_t = 0x200
	TABLE_STATE_RESULT_SKIP_DATA               table_state_t = 0x400
)

type TableProgress struct {
	name            string
	progress        int
	total           int
	state           table_state_t
	totalStopUpdate bool
}

type progress_opt_t int

const (
	PROGRESS_OPT_INC progress_opt_t = iota
	PROGRESS_OPT_UPDATE
	PROGRESS_OPT_REGISTER
	PROGRESS_OPT_START
	PROGRESS_OPT_FAIL
	PROGRESS_OPT_CLOSE
	PROGRESS_OPT_ERROR
)

type Operator struct {
	optType         progress_opt_t
	name            string
	total           int
	state           table_state_t
	totalStopUpdate bool
}

func NewTableProgressPrinter(tableNums int, finishTableNums int, exportFixSQL ...bool) *TableProgressPrinter {
	return newTableProgressPrinter(tableNums, finishTableNums, os.Stdout, exportFixSQL...)
}

func newTableProgressPrinter(tableNums, finishTableNums int, output io.Writer, exportFixSQL ...bool) *TableProgressPrinter {
	tpp := &TableProgressPrinter{
		tableList:     list.New(),
		tableFailList: list.New(),
		tableMap:      make(map[string]*list.Element),

		finishTableNums: finishTableNums,
		tableNums:       tableNums,

		progress:     0,
		total:        0,
		exportFixSQL: len(exportFixSQL) == 0 || exportFixSQL[0],

		optCh:    make(chan Operator, 16),
		finishCh: make(chan struct{}),
	}
	tpp.init()
	tpp.output = output
	fmt.Fprintf(tpp.output, "A total of %d tables need to be compared\n", tableNums)
	go tpp.serve()
	return tpp
}

func (tpp *TableProgressPrinter) SetOutput(output io.Writer) {
	tpp.output = output
}

func (tpp *TableProgressPrinter) enqueue(opt Operator) {
	select {
	case <-tpp.finishCh:
		return
	default:
	}
	select {
	case <-tpp.finishCh:
	case tpp.optCh <- opt:
	}
}

func (tpp *TableProgressPrinter) Inc(name string) {
	tpp.enqueue(Operator{
		optType: PROGRESS_OPT_INC,
		name:    name,
	})
}

func (tpp *TableProgressPrinter) UpdateTotal(name string, total int, stopUpdate bool) {
	tpp.enqueue(Operator{
		optType:         PROGRESS_OPT_UPDATE,
		name:            name,
		total:           total,
		totalStopUpdate: stopUpdate,
	})
}

func (tpp *TableProgressPrinter) RegisterTable(name string, isFailed bool, isDone bool, isExist int) {
	var state table_state_t
	if isFailed {
		if isDone {
			switch isExist {
			case common.UpstreamTableLackFlag:
				state = TABLE_STATE_NOT_EXSIT_UPSTREAM | TABLE_STATE_REGISTER
			case common.DownstreamTableLackFlag:
				state = TABLE_STATE_NOT_EXSIT_DOWNSTREAM | TABLE_STATE_REGISTER
			default:
				state = TABLE_STATE_RESULT_FAIL_STRUCTURE_DONE | TABLE_STATE_REGISTER
			}
		} else {
			state = TABLE_STATE_RESULT_FAIL_STRUCTURE_CONTINUE | TABLE_STATE_REGISTER
		}
	} else if isDone {
		state = TABLE_STATE_RESULT_SKIP_DATA | TABLE_STATE_REGISTER
	} else {
		state = TABLE_STATE_REGISTER
	}
	tpp.enqueue(Operator{
		optType: PROGRESS_OPT_REGISTER,
		name:    name,
		state:   state,
	})
}

func (tpp *TableProgressPrinter) StartTable(name string, total int, stopUpdate bool) {
	tpp.enqueue(Operator{
		optType:         PROGRESS_OPT_START,
		name:            name,
		total:           total,
		state:           TABLE_STATE_PRESTART,
		totalStopUpdate: stopUpdate,
	})
}

func (tpp *TableProgressPrinter) FailTable(name string) {
	tpp.enqueue(Operator{
		optType: PROGRESS_OPT_FAIL,
		name:    name,
		state:   TABLE_STATE_RESULT_DIFFERENT,
	})
}

func (tpp *TableProgressPrinter) Close() {
	tpp.enqueue(Operator{
		optType: PROGRESS_OPT_CLOSE,
	})
	<-tpp.finishCh
}

func (tpp *TableProgressPrinter) PrintSummary() {
	fixStr := "\nSummary:\n\n"
	if tpp.tableFailList.Len() == 0 {
		fixStr = fmt.Sprintf(
			"%sA total of %d tables have been compared and all are equal.\nYou can view the comparison details through './output_dir/sync_diff_inspector.log'\n",
			fixStr,
			tpp.tableNums,
		)
	} else {
		SkippedNum := 0
		for p := tpp.tableFailList.Front(); p != nil; p = p.Next() {
			tp := p.Value.(*TableProgress)
			if tp.state&(TABLE_STATE_RESULT_FAIL_STRUCTURE_DONE|TABLE_STATE_RESULT_FAIL_STRUCTURE_CONTINUE) != 0 {
				fixStr = fmt.Sprintf("%sThe structure of %s is not equal.\n", fixStr, tp.name)
			}
			if tp.state&(TABLE_STATE_RESULT_DIFFERENT) != 0 {
				fixStr = fmt.Sprintf("%sThe data of %s is not equal.\n", fixStr, tp.name)
			}
			if tp.state&(TABLE_STATE_NOT_EXSIT_DOWNSTREAM) != 0 {
				fixStr = fmt.Sprintf("%sThe data of %s does not exist in downstream database.\n", fixStr, tp.name)
				SkippedNum++
			}
			if tp.state&(TABLE_STATE_NOT_EXSIT_UPSTREAM) != 0 {
				fixStr = fmt.Sprintf("%sThe data of %s does not exist in upstream database.\n", fixStr, tp.name)
				SkippedNum++
			}
		}
		fixStr = fmt.Sprintf("%s\nThe rest of the tables are all equal.\nA total of %d tables have been compared, %d tables finished, %d tables failed, %d tables skipped.\n", fixStr, tpp.tableNums, tpp.tableNums-tpp.tableFailList.Len(), tpp.tableFailList.Len()-SkippedNum, SkippedNum)
		if tpp.exportFixSQL {
			fixStr += "The patch file has been generated to './output_dir/patch.sql'\n"
		} else {
			fixStr += "Fix SQL export is disabled; differences were recorded without exporting SQL.\n"
		}
		fixStr += "You can view the comparison details through './output_dir/sync_diff_inspector.log'\n"
	}

	fmt.Fprintf(tpp.output, "%s\n", fixStr)

}

func (tpp *TableProgressPrinter) Error(err error) {
	tpp.enqueue(Operator{
		optType: PROGRESS_OPT_ERROR,
	})
	<-tpp.finishCh
	fmt.Fprintf(tpp.output, "\nError in comparison process:\n%v\n\nYou can view the comparison details through './output_dir/sync_diff_inspector.log'\n", err)
}

func (tpp *TableProgressPrinter) init() {
	tpp.tableList.PushBack(&TableProgress{
		state: TABLE_STATE_HEAD,
	})

	tpp.output = os.Stdout
}

func (tpp *TableProgressPrinter) serve() {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-tick.C:
			tpp.flush(false)
		case opt := <-tpp.optCh:
			switch opt.optType {
			case PROGRESS_OPT_CLOSE:
				tpp.flush(false)
				tpp.endLine()
				close(tpp.finishCh)
				return
			case PROGRESS_OPT_ERROR:
				tpp.endLine()
				close(tpp.finishCh)
				return
			case PROGRESS_OPT_INC:
				if e, ok := tpp.tableMap[opt.name]; ok {
					tp := e.Value.(*TableProgress)
					tp.progress++
					tpp.progress++
					tpp.finishTable(tp)
				}
			case PROGRESS_OPT_REGISTER:
				if _, ok := tpp.tableMap[opt.name]; !ok {
					e := tpp.tableList.PushBack(&TableProgress{
						name:            opt.name,
						progress:        0,
						total:           opt.total,
						state:           opt.state,
						totalStopUpdate: opt.totalStopUpdate,
					})
					tpp.tableMap[opt.name] = e
				}
			case PROGRESS_OPT_START:
				e, ok := tpp.tableMap[opt.name]
				if !ok {
					e = tpp.tableList.PushBack(&TableProgress{
						name:            opt.name,
						progress:        0,
						total:           opt.total,
						state:           opt.state | TABLE_STATE_RESULT_FAIL_STRUCTURE_PASS,
						totalStopUpdate: opt.totalStopUpdate,
					})
					tpp.tableMap[opt.name] = e
				} else {
					tp := e.Value.(*TableProgress)
					tp.state ^= TABLE_STATE_REGISTER | opt.state
					tp.progress = 0
					tp.total = opt.total
					tp.totalStopUpdate = opt.totalStopUpdate
				}
				tp := e.Value.(*TableProgress)
				if tp.state&(TABLE_STATE_RESULT_FAIL_STRUCTURE_DONE|TABLE_STATE_NOT_EXSIT_UPSTREAM|TABLE_STATE_NOT_EXSIT_DOWNSTREAM|TABLE_STATE_RESULT_SKIP_DATA) == 0 {
					tpp.total += opt.total
				} else {
					delete(tpp.tableMap, opt.name)
				}
				tpp.flush(true)
				if _, ok := tpp.tableMap[opt.name]; ok {
					tpp.finishTable(tp)
				}
			case PROGRESS_OPT_UPDATE:
				if e, ok := tpp.tableMap[opt.name]; ok {
					tp := e.Value.(*TableProgress)
					tpp.total += opt.total
					tp.total += opt.total
					tp.totalStopUpdate = opt.totalStopUpdate
					tpp.finishTable(tp)
				}
			case PROGRESS_OPT_FAIL:
				if e, ok := tpp.tableMap[opt.name]; ok {
					tp := e.Value.(*TableProgress)
					tp.state |= opt.state
					// continue to increment chunk
				}
			}
		}
	}
}

// The producer may finish splitting after the consumer has processed its last
// chunk. Both Inc and UpdateTotal must therefore check for completion.
func (tpp *TableProgressPrinter) finishTable(tp *TableProgress) {
	if tp.totalStopUpdate && tp.progress >= tp.total {
		tp.state = (tp.state & TABLE_STATE_RESULT_MASK) | TABLE_STATE_FINISH
		delete(tpp.tableMap, tp.name)
		tpp.flush(true)
	}
}

// flush prints lifecycle messages once and keeps only the progress line live.
// Moving up a fixed number of lines breaks as soon as the terminal wraps text.
func (tpp *TableProgressPrinter) flush(stateIsChanged bool) {
	var messages string
	if stateIsChanged {
		var fixStr, dynStr string
		/* PRESTART/COMPARING/FINISH OK/DIFFERENT */
		for p := tpp.tableList.Front(); p != nil; p = p.Next() {
			tp := p.Value.(*TableProgress)
			// There are 5 situations:
			// 1. structure is same and data is same
			// 2. structure is same and data is different
			// 3. structure is different and we won't compare data
			// 4. structure is different and data is same
			// 5. structure is different and data is different
			switch tp.state & 0xf {
			case TABLE_STATE_PRESTART:
				switch tp.state & TABLE_STATE_RESULT_MASK {
				case TABLE_STATE_RESULT_SKIP_DATA:
					fixStr += fmt.Sprintf("Comparing the table structure of %s ... equivalent\nComparing the table data of %s ... skipped\n", tp.name, tp.name)
					preNode := p.Prev()
					tpp.tableList.Remove(p)
					p = preNode
					tpp.finishTableNums++
				case TABLE_STATE_RESULT_OK:
					fixStr = fmt.Sprintf("%sComparing the table structure of %s ... equivalent\n", fixStr, tp.name)
					dynStr = fmt.Sprintf("%sComparing the table data of %s ...\n", dynStr, tp.name)
					tp.state = TABLE_STATE_COMPARING
				case TABLE_STATE_NOT_EXSIT_UPSTREAM, TABLE_STATE_NOT_EXSIT_DOWNSTREAM:
					dynStr = fmt.Sprintf("%sComparing the table data of %s ...skipped\n", dynStr, tp.name)
					tpp.tableFailList.PushBack(tp)
					preNode := p.Prev()
					tpp.tableList.Remove(p)
					p = preNode
					tpp.finishTableNums++
				case TABLE_STATE_RESULT_FAIL_STRUCTURE_DONE:
					fixStr = fmt.Sprintf("%sComparing the table structure of %s ... failure\n", fixStr, tp.name)
					tpp.tableFailList.PushBack(tp)
					// we have empty node as list head, so p is not nil
					preNode := p.Prev()
					tpp.tableList.Remove(p)
					p = preNode
					tpp.finishTableNums++
				case TABLE_STATE_RESULT_FAIL_STRUCTURE_CONTINUE:
					fixStr = fmt.Sprintf("%sComparing the table structure of %s ... failure\n", fixStr, tp.name)
					dynStr = fmt.Sprintf("%sComparing the table data of %s ...\n", dynStr, tp.name)
					tp.state ^= TABLE_STATE_COMPARING | TABLE_STATE_PRESTART
				case TABLE_STATE_RESULT_FAIL_STRUCTURE_PASS:
					fixStr = fmt.Sprintf("%sComparing the table structure of %s ... skip\n", fixStr, tp.name)
					dynStr = fmt.Sprintf("%sComparing the table data of %s ...\n", dynStr, tp.name)
					tp.state ^= TABLE_STATE_COMPARING | TABLE_STATE_PRESTART
				}
			case TABLE_STATE_COMPARING:
				// Already announced when the table started.
			case TABLE_STATE_FINISH:
				if tp.state&TABLE_STATE_RESULT_DIFFERENT == 0 {
					fixStr = fmt.Sprintf("%sComparing the table data of %s ... equivalent\n", fixStr, tp.name)
				} else {
					fixStr = fmt.Sprintf("%sComparing the table data of %s ... failure\n", fixStr, tp.name)
				}
				if tp.state&TABLE_STATE_RESULT_MASK != 0 {
					tpp.tableFailList.PushBack(tp)
				}
				// we have empty node as list head, so p is not nil
				preNode := p.Prev()
				tpp.tableList.Remove(p)
				p = preNode
				tpp.finishTableNums++
			}
		}

		messages = fixStr + dynStr
	}
	tpp.render(messages)
}

func (tpp *TableProgressPrinter) width() int {
	if tpp.terminalWidth != nil {
		return tpp.terminalWidth()
	}
	if file, ok := tpp.output.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		width, _, err := term.GetSize(int(file.Fd()))
		if err == nil && width > 1 {
			return width
		}
	}
	return 0
}

func (tpp *TableProgressPrinter) progressLine(width int) string {
	var fraction float64
	if tpp.tableNums > 0 {
		fraction = float64(tpp.finishTableNums) / float64(tpp.tableNums)
	}
	fraction = min(1, max(0, fraction))
	// Chunk totals are discovered lazily; only completed tables determine the
	// percentage. Chunk activity remains visible even while a large table runs.
	line := fmt.Sprintf("Progress %d%% | tables %d/%d | chunks %d/%d",
		int(100*fraction), tpp.finishTableNums, tpp.tableNums, tpp.progress, tpp.total)
	if width > 1 {
		// Reserve the final column to avoid the terminal's automatic line wrap.
		available := width - 1
		if len(line) > available {
			line = fmt.Sprintf("Progress %d%% | tables %d/%d", int(100*fraction), tpp.finishTableNums, tpp.tableNums)
		}
		if len(line) > available {
			line = strings.TrimPrefix(line, "Progress ")
		}
		barWidth := min(24, available-len(line)-3)
		if barWidth >= 8 {
			filled := int(float64(barWidth) * fraction)
			bar := strings.Repeat("=", filled) + strings.Repeat("-", barWidth-filled)
			line = strings.Replace(line, "Progress ", "Progress ["+bar+"] ", 1)
		}
		if len(line) > available {
			// Keep the table count ahead of the chunk counter in narrow windows.
			line = line[:available]
		}
	}
	return line
}

func (tpp *TableProgressPrinter) render(messages string) {
	width := tpp.width()
	line := tpp.progressLine(width)
	if messages == "" && line == tpp.lastProgress {
		return // idle ticks must not scroll the terminal or fill redirected logs
	}
	if width > 1 {
		// Explicitly return to column zero; cursor-up alone retains the column.
		fmt.Fprint(tpp.output, "\r\x1b[2K")
		if messages != "" {
			fmt.Fprint(tpp.output, strings.ReplaceAll(messages, "\n", "\r\n"))
		}
		fmt.Fprint(tpp.output, line) // no newline: redraw this same physical row
		tpp.lineVisible = true
	} else {
		// Files, pipes and non-terminal consoles get ordinary append-only text.
		fmt.Fprint(tpp.output, messages)
		fmt.Fprintln(tpp.output, line)
	}
	tpp.lastProgress = line
}

func (tpp *TableProgressPrinter) endLine() {
	if tpp.lineVisible {
		fmt.Fprint(tpp.output, "\r\n")
		tpp.lineVisible = false
	}
}

var progress_ *TableProgressPrinter = nil

func Init(tableNums, finishTableNums int, exportFixSQL ...bool) {
	progress_ = NewTableProgressPrinter(tableNums, finishTableNums, exportFixSQL...)
}

func Inc(name string) {
	if progress_ != nil {
		progress_.Inc(name)
	}
}

func UpdateTotal(name string, total int, stopUpdate bool) {
	if progress_ != nil {
		progress_.UpdateTotal(name, total, stopUpdate)
	}
}

func RegisterTable(name string, isFailed bool, isDone bool, isExist int) {
	if progress_ != nil {
		progress_.RegisterTable(name, isFailed, isDone, isExist)
	}
}

func StartTable(name string, total int, stopUpdate bool) {
	if progress_ != nil {
		progress_.StartTable(name, total, stopUpdate)
	}
}

func FailTable(name string) {
	if progress_ != nil {
		progress_.FailTable(name)
	}
}

func Close() {
	if progress_ != nil {
		progress_.Close()
	}
}

func PrintSummary() {
	if progress_ != nil {
		progress_.PrintSummary()
	}
}

func Error(err error) {
	if progress_ != nil {
		progress_.Error(err)
	}
}

func SetOutput(output io.Writer) {
	if progress_ != nil {
		progress_.SetOutput(output)
	}
}
