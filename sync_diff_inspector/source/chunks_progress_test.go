package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/pingcap/tidb-tools/pkg/dbutil"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/chunk"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/progress"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/source/common"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/splitter"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser/mysql"
	"github.com/stretchr/testify/require"
)

func TestDB2ChunksReportProgressForAllModes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	key := db2TestKeyColumn("ID", 0, mysql.TypeLonglong)
	keyset := db2TestTable(key, 1)
	keyset.Table = "KEYSET"
	empty := db2TestTable(key, 1)
	empty.Table = "EMPTY"
	tables := []*common.TableDiff{keyset,
		{Schema: "APP", Table: "NOKEY", Mode: common.TableDiffModeUnorderedChecksum},
		empty, {Schema: "APP", Table: "MISSING", TableLack: common.UpstreamTableLackFlag}}
	s := &DB2Source{tableDiffs: tables, dbConn: db, schema: "APP",
		sourceColumns: map[int]map[string]string{0: {"ID": "ID"}, 2: {"ID": "ID"}},
		orderKeys:     map[int][]*model.ColumnInfo{0: {key}, 2: {key}}}
	mock.ExpectQuery(`SELECT "ID" FROM "APP"\."KEYSET" ORDER BY "ID" FETCH FIRST 2 ROWS ONLY`).
		WillReturnRows(sqlmock.NewRows([]string{"ID"}).AddRow(1).AddRow(2))
	mock.ExpectQuery(`SELECT "ID" FROM "APP"\."KEYSET" WHERE .* ORDER BY "ID" FETCH FIRST 2 ROWS ONLY`).
		WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"ID"}).AddRow(2))
	mock.ExpectQuery(`SELECT "ID" FROM "APP"\."EMPTY" ORDER BY "ID" FETCH FIRST 2 ROWS ONLY`).
		WillReturnRows(sqlmock.NewRows([]string{"ID"}))

	// Capture the real global printer used by ChunksIterator, without sleeps.
	outputPath := filepath.Join(t.TempDir(), "progress.txt")
	output, err := os.Create(outputPath)
	require.NoError(t, err)
	stdout := os.Stdout
	os.Stdout = output
	defer func() { progress.Close(); os.Stdout = stdout; output.Close() }()
	progress.Init(len(tables), 0)
	for _, table := range tables {
		missing := !common.AllTableExist(table.TableLack)
		progress.RegisterTable(dbutil.TableName(table.Schema, table.Table), missing, missing, table.TableLack)
	}
	iter, err := NewChunksIterator(ctx, DB2TableAnalyzer{source: s}, tables, nil, 1)
	require.NoError(t, err)
	defer iter.Close()
	counts := make(map[int]int)
	for {
		r, err := iter.Next(ctx)
		require.NoError(t, err)
		if r == nil {
			break
		}
		counts[r.GetTableIndex()]++
		if r.GetTableIndex() == 2 {
			// Empty source tables must still be compared against the target.
			require.NotEqual(t, chunk.Empty, r.ChunkRange.Type)
			require.Empty(t, r.ChunkRange.Bounds)
		}
		progress.Inc(r.ProgressID)
	}
	progress.Close()
	require.NoError(t, ctx.Err())
	require.Equal(t, map[int]int{0: 2, 1: 1, 2: 1, 3: 1}, counts)
	require.NoError(t, mock.ExpectationsWereMet())
	raw, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	require.Contains(t, string(raw), "100% | tables 4/4 | chunks 4/4")
}

func TestChunksIteratorDoesNotLosePendingErrorAtEOF(t *testing.T) {
	chunksCh := make(chan *splitter.RangeInfo)
	close(chunksCh)
	errCh := make(chan error, 1)
	errCh <- errors.New("split failed")
	iter := &ChunksIterator{chunksCh: chunksCh, errCh: errCh}
	r, err := iter.Next(context.Background())
	require.Nil(t, r)
	require.EqualError(t, err, "split failed")
}
