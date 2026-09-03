package source

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/pingcap/tidb-tools/pkg/dbutil"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/chunk"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/config"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/db2util"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/source/common"
	"github.com/pingcap/tidb/pkg/meta/model"
	pmodel "github.com/pingcap/tidb/pkg/parser/model"
	"github.com/pingcap/tidb/pkg/parser/mysql"
	"github.com/pingcap/tidb/pkg/types"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestDB2ConnectionBranchExplainsNativeRequirement(t *testing.T) {
	_, err := connectDataSource(context.Background(), &config.DataSource{
		Type: config.DatabaseTypeDB2, Host: "db2.example", Port: 50000, Database: "SAMPLE", User: "diff",
	}, 1)
	require.ErrorContains(t, err, "build with -tags db2cli")
}

func TestDecodeDB2GBKTextRow(t *testing.T) {
	gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("中文"))
	require.NoError(t, err)
	row := map[string]*dbutil.ColumnData{
		"NAME":   {Data: gbk},
		"BINARY": {Data: []byte{0xD6, 0xD0}},
	}
	decoder, err := newDB2TextDecoder("gbk")
	require.NoError(t, err)
	require.NoError(t, decodeDB2TextRow(row, map[string]struct{}{"NAME": {}}, decoder))
	require.Equal(t, "中文", string(row["NAME"].Data))
	require.Equal(t, []byte{0xD6, 0xD0}, row["BINARY"].Data)

	_, err = newDB2TextDecoder("utf8")
	require.ErrorContains(t, err, "unsupported db2 source-charset")
}

func TestDB2OrderColumnsRejectsNullableConfiguredKey(t *testing.T) {
	info := &model.TableInfo{Columns: []*model.ColumnInfo{{Name: pmodel.NewCIStr("ID"), Offset: 0, FieldType: *types.NewFieldType(mysql.TypeLonglong)}}}
	table := &common.TableDiff{Schema: "APP", Table: "T", Fields: "ID", Info: info}
	source := &DB2Source{sourceColumns: map[int]map[string]string{0: {"ID": "ID"}}}
	_, err := source.orderColumns(table)
	require.ErrorContains(t, err, "nullable")
}

func TestDB2OrderColumnsAndStructuredChunkRange(t *testing.T) {
	info := &model.TableInfo{Columns: []*model.ColumnInfo{
		{Name: pmodel.NewCIStr("ID"), Offset: 0, FieldType: *types.NewFieldType(mysql.TypeLonglong)},
		{Name: pmodel.NewCIStr("NAME"), Offset: 1, FieldType: *types.NewFieldType(mysql.TypeVarString)},
	}}
	info.Indices = []*model.IndexInfo{{Name: pmodel.NewCIStr("PRIMARY"), Primary: true, Unique: true, Columns: []*model.IndexColumn{{Name: pmodel.NewCIStr("ID"), Offset: 0}}}}
	table := &common.TableDiff{Schema: "APP", Table: "T", Info: info}
	source := &DB2Source{tableDiffs: []*common.TableDiff{table}, sourceColumns: map[int]map[string]string{0: {"ID": "ID", "NAME": "NAME"}}}
	keys, err := source.orderColumns(table)
	require.NoError(t, err)
	require.Equal(t, []string{"ID"}, []string{keys[0].Name.O})

	r := &chunk.Range{Bounds: []*chunk.Bound{{Column: "ID", Lower: "10", Upper: "20", HasLower: true, HasUpper: true, LowerValue: int64(10), UpperValue: int64(20)}, {Column: "NAME", Lower: "a", Upper: "z", HasLower: true, HasUpper: true, LowerValue: "a", UpperValue: "z"}}, DB2UpperInclusive: true}
	structured := db2RangeFromChunk(r, source.sourceColumns[0])
	sql, args, err := (db2util.DB2Dialect{}).RenderRange(structured)
	require.NoError(t, err)
	require.Equal(t, `(("ID" > ?) OR ("ID" = ? AND "NAME" > ?)) AND (("ID" < ?) OR ("ID" = ? AND "NAME" <= ?))`, sql)
	require.Equal(t, []any{int64(10), int64(10), "a", int64(20), int64(20), "z"}, args)
}

func TestDB2KeysetIteratorUsesDialectAndChunkSize(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	info := &model.TableInfo{Columns: []*model.ColumnInfo{{Name: pmodel.NewCIStr("ID"), Offset: 0, FieldType: *types.NewFieldType(mysql.TypeLonglong)}}}
	info.Indices = []*model.IndexInfo{{Name: pmodel.NewCIStr("PRIMARY"), Primary: true, Unique: true, Columns: []*model.IndexColumn{{Name: pmodel.NewCIStr("ID"), Offset: 0}}}}
	table := &common.TableDiff{Schema: "APP", Table: "T", Info: info, ChunkSize: 2}
	source := &DB2Source{tableDiffs: []*common.TableDiff{table}, dbConn: db, schema: "APP", sourceColumns: map[int]map[string]string{0: {"ID": "ID"}}}
	mock.ExpectQuery(`SELECT "ID" FROM "APP"\."T" ORDER BY "ID" FETCH FIRST 3 ROWS ONLY`).WillReturnRows(sqlmock.NewRows([]string{"ID"}).AddRow([]byte("1")).AddRow([]byte("2")).AddRow([]byte("3")))
	iter := newDB2KeysetIterator(source, table, []*model.ColumnInfo{info.Columns[0]}, nil)
	rangeInfo, err := iter.Next()
	require.NoError(t, err)
	require.NotNil(t, rangeInfo)
	require.False(t, rangeInfo.IsLast)
	require.Equal(t, "2", rangeInfo.Bounds[0].Upper)
	require.NoError(t, mock.ExpectationsWereMet())
}
