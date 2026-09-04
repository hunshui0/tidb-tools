package source

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/pingcap/tidb-tools/pkg/dbutil"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/chunk"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/config"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/db2util"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/source/common"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/splitter"
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

func TestIsDB2SourceRecognizesCanonicalWrapper(t *testing.T) {
	db2Source := &DB2Source{}
	require.True(t, IsDB2Source(db2Source))
	require.True(t, IsDB2Source(CanonicalSource{Source: db2Source}))
	require.True(t, IsDB2Source(&CanonicalSource{Source: db2Source}))
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

func TestDB2DecodedRowGeneratesTypedTiDBRepairSQL(t *testing.T) {
	gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("中文"))
	require.NoError(t, err)
	columns := []*model.ColumnInfo{
		{Name: pmodel.NewCIStr("NAME"), FieldType: *types.NewFieldType(mysql.TypeVarString)},
		{Name: pmodel.NewCIStr("OPTIONAL"), FieldType: *types.NewFieldType(mysql.TypeVarString)},
		{Name: pmodel.NewCIStr("PAYLOAD"), FieldType: *types.NewFieldType(mysql.TypeBlob)},
		{Name: pmodel.NewCIStr("AMOUNT"), FieldType: *types.NewFieldType(mysql.TypeNewDecimal)},
		{Name: pmodel.NewCIStr("BIRTHDAY"), FieldType: *types.NewFieldType(mysql.TypeDate)},
		{Name: pmodel.NewCIStr("AT"), FieldType: *types.NewFieldType(mysql.TypeDuration)},
		{Name: pmodel.NewCIStr("UPDATED"), FieldType: *types.NewFieldType(mysql.TypeTimestamp)},
	}
	row := map[string]*dbutil.ColumnData{
		"NAME":     {Data: gbk},
		"OPTIONAL": {IsNull: true},
		"PAYLOAD":  {Data: []byte{0x00, 0xFF}},
		"AMOUNT":   {Data: []byte("123.4500")},
		"BIRTHDAY": {Data: []byte("2026-09-04")},
		"AT":       {Data: []byte("12:34:56")},
		"UPDATED":  {Data: []byte("2026-09-04 12:34:56")},
	}
	decoder, err := newDB2TextDecoder("gbk")
	require.NoError(t, err)
	require.NoError(t, decodeDB2TextRow(row, map[string]struct{}{"NAME": {}}, decoder))
	tidb := &TiDBSource{tableDiffs: []*common.TableDiff{{Schema: "APP", Table: "T", Info: &model.TableInfo{Name: pmodel.NewCIStr("T"), Columns: columns}}}}
	sql := tidb.GenerateFixSQL(Insert, row, nil, 0)
	require.Equal(t, "REPLACE INTO `APP`.`T`(`NAME`,`OPTIONAL`,`PAYLOAD`,`AMOUNT`,`BIRTHDAY`,`AT`,`UPDATED`) VALUES ('中文',NULL,x'00ff',123.4500,'2026-09-04','12:34:56','2026-09-04 12:34:56');", sql)
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
	info.Columns[0].FieldType.SetFlag(mysql.NotNullFlag)
	info.Indices = []*model.IndexInfo{{Name: pmodel.NewCIStr("PRIMARY"), Primary: true, Unique: true, Columns: []*model.IndexColumn{{Name: pmodel.NewCIStr("ID"), Offset: 0}}}}
	table := &common.TableDiff{Schema: "APP", Table: "T", Info: info}
	source := &DB2Source{tableDiffs: []*common.TableDiff{table}, sourceColumns: map[int]map[string]string{0: {"ID": "ID", "NAME": "NAME"}}}
	keys, err := source.orderColumns(table)
	require.NoError(t, err)
	require.Equal(t, []string{"ID"}, []string{keys[0].Name.O})

	r := &chunk.Range{Bounds: []*chunk.Bound{{Column: "ID", Lower: "10", Upper: "20", HasLower: true, HasUpper: true, LowerValue: int64(10), UpperValue: int64(20)}, {Column: "NAME", Lower: "a", Upper: "z", HasLower: true, HasUpper: true, LowerValue: "a", UpperValue: "z"}}, DB2UpperInclusive: false}
	structured := db2RangeFromChunk(r, source.sourceColumns[0])
	sql, args, err := (db2util.DB2Dialect{}).RenderRange(structured)
	require.NoError(t, err)
	require.Equal(t, `(("ID" > ?) OR ("ID" = ? AND "NAME" > ?)) AND (("ID" < ?) OR ("ID" = ? AND "NAME" <= ?))`, sql)
	require.Equal(t, []any{int64(10), int64(10), "a", int64(20), int64(20), "z"}, args)
}

func TestDB2ConfiguredIndexFieldsMustBeUnique(t *testing.T) {
	id := &model.ColumnInfo{Name: pmodel.NewCIStr("ID"), Offset: 0, FieldType: *types.NewFieldType(mysql.TypeLonglong)}
	id.FieldType.SetFlag(mysql.NotNullFlag)
	name := &model.ColumnInfo{Name: pmodel.NewCIStr("NAME"), Offset: 1, FieldType: *types.NewFieldType(mysql.TypeVarString)}
	name.FieldType.SetFlag(mysql.NotNullFlag)
	info := &model.TableInfo{Columns: []*model.ColumnInfo{id, name}, Indices: []*model.IndexInfo{{Name: pmodel.NewCIStr("PRIMARY"), Primary: true, Unique: true, Columns: []*model.IndexColumn{{Name: id.Name, Offset: 0}}}}}
	source := &DB2Source{}
	_, err := source.orderColumns(&common.TableDiff{Schema: "APP", Table: "T", Fields: "NAME", Info: info})
	require.ErrorContains(t, err, "not a declared primary or unique key")
	keys, err := source.orderColumns(&common.TableDiff{Schema: "APP", Table: "T", Fields: "ID", Info: info})
	require.NoError(t, err)
	require.Equal(t, "ID", keys[0].Name.O)
}

func TestDB2AutomaticOrderPrefersPrimaryKey(t *testing.T) {
	unique := db2TestKeyColumn("U", 1, mysql.TypeLonglong)
	primary := db2TestKeyColumn("ID", 0, mysql.TypeLonglong)
	info := &model.TableInfo{
		Columns: []*model.ColumnInfo{primary, unique},
		// Deliberately put the unique index first to exercise the priority rule.
		Indices: []*model.IndexInfo{
			{Name: pmodel.NewCIStr("UK_U"), Unique: true, Columns: []*model.IndexColumn{{Name: unique.Name, Offset: unique.Offset}}},
			{Name: pmodel.NewCIStr("PRIMARY"), Primary: true, Unique: true, Columns: []*model.IndexColumn{{Name: primary.Name, Offset: primary.Offset}}},
		},
	}
	keys, err := (&DB2Source{}).orderColumns(&common.TableDiff{Schema: "APP", Table: "T", Info: info})
	require.NoError(t, err)
	require.Equal(t, []string{"ID"}, []string{keys[0].Name.O})
}

func TestDB2CheckpointResumePreservesTypedBoundary(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	id := db2TestKeyColumn("ID", 0, mysql.TypeLonglong)
	table := db2TestTable(id, 1)
	source := db2TestSource(db, table, id)
	original := &chunk.Range{Index: &chunk.ChunkID{ChunkIndex: 0}, Bounds: []*chunk.Bound{{Column: "ID", Upper: "1", UpperValue: int64(1), HasUpper: true}}}
	data, err := json.Marshal(original)
	require.NoError(t, err)
	var restored chunk.Range
	require.NoError(t, json.Unmarshal(data, &restored))
	mock.ExpectQuery(`SELECT "ID" FROM "APP"\."T" WHERE \(\("ID" > \?\)\) ORDER BY "ID" FETCH FIRST 2 ROWS ONLY`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"ID"}).AddRow(int64(2)))
	iter := newDB2KeysetIterator(source, table, []*model.ColumnInfo{id}, &splitter.RangeInfo{ChunkRange: &restored})
	next, err := iter.Next()
	require.NoError(t, err)
	require.True(t, next.IsLast)
	require.Equal(t, int64(2), next.Bounds[0].UpperValue)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDB2CheckpointResumeAfterMultipleKeysetChunks(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	id := db2TestKeyColumn("ID", 0, mysql.TypeLonglong)
	table := db2TestTable(id, 2)
	source := db2TestSource(db, table, id)
	mock.ExpectQuery(`SELECT "ID" FROM "APP"\."T" ORDER BY "ID" FETCH FIRST 3 ROWS ONLY`).WillReturnRows(sqlmock.NewRows([]string{"ID"}).AddRow(int64(1)).AddRow(int64(2)).AddRow(int64(3)))
	mock.ExpectQuery(`SELECT "ID" FROM "APP"\."T" WHERE \(\("ID" > \?\)\) ORDER BY "ID" FETCH FIRST 3 ROWS ONLY`).WithArgs(int64(2)).WillReturnRows(sqlmock.NewRows([]string{"ID"}).AddRow(int64(3)).AddRow(int64(4)))
	iter := newDB2KeysetIterator(source, table, []*model.ColumnInfo{id}, nil)
	first, err := iter.Next()
	require.NoError(t, err)
	second, err := iter.Next()
	require.NoError(t, err)
	require.False(t, first.IsLast)
	require.True(t, second.IsLast)
	require.Equal(t, int64(4), second.Bounds[0].UpperValue)

	data, err := json.Marshal(second)
	require.NoError(t, err)
	var restored chunk.Range
	require.NoError(t, json.Unmarshal(data, &restored))
	require.IsType(t, int64(0), restored.Bounds[0].UpperValue)

	mock.ExpectQuery(`SELECT "ID" FROM "APP"\."T" WHERE \(\("ID" > \?\)\) ORDER BY "ID" FETCH FIRST 3 ROWS ONLY`).WithArgs(int64(4)).WillReturnRows(sqlmock.NewRows([]string{"ID"}).AddRow(int64(5)))
	resumed := newDB2KeysetIterator(source, table, []*model.ColumnInfo{id}, &splitter.RangeInfo{ChunkRange: &restored})
	next, err := resumed.Next()
	require.NoError(t, err)
	require.True(t, next.IsLast)
	require.Equal(t, int64(5), next.Bounds[0].UpperValue)
	where, args, err := (db2util.DB2Dialect{}).RenderRange(db2RangeFromChunk(next, source.sourceColumns[0]))
	require.NoError(t, err)
	require.Equal(t, `(("ID" > ?)) AND (("ID" <= ?))`, where)
	require.Equal(t, []any{int64(4), int64(5)}, args)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDB2KeysetIteratorCloseCancelsContext(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	id := db2TestKeyColumn("ID", 0, mysql.TypeLonglong)
	table := db2TestTable(id, 1)
	source := db2TestSource(db, table, id)
	ctx, cancel := context.WithCancel(context.Background())
	iter := newDB2KeysetIteratorWithContext(ctx, source, table, []*model.ColumnInfo{id}, nil)
	cancel()
	_, err = iter.Next()
	require.ErrorContains(t, err, "context canceled")
	iter.Close()
	chunkRange, err := iter.Next()
	require.NoError(t, err)
	require.Nil(t, chunkRange)
}

func TestDB2KeysetChunksAreContiguousAndNonOverlapping(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	id := &model.ColumnInfo{Name: pmodel.NewCIStr("ID"), Offset: 0, FieldType: *types.NewFieldType(mysql.TypeLonglong)}
	id.FieldType.SetFlag(mysql.NotNullFlag)
	info := &model.TableInfo{Columns: []*model.ColumnInfo{id}, Indices: []*model.IndexInfo{{Name: pmodel.NewCIStr("PRIMARY"), Primary: true, Unique: true, Columns: []*model.IndexColumn{{Name: id.Name, Offset: 0}}}}}
	table := &common.TableDiff{Schema: "APP", Table: "T", Info: info, ChunkSize: 2}
	source := &DB2Source{tableDiffs: []*common.TableDiff{table}, dbConn: db, schema: "APP", sourceColumns: map[int]map[string]string{0: {"ID": "ID"}}, orderKeys: map[int][]*model.ColumnInfo{0: {id}}}
	mock.ExpectQuery(`SELECT "ID" FROM "APP"\."T" ORDER BY "ID" FETCH FIRST 3 ROWS ONLY`).WillReturnRows(sqlmock.NewRows([]string{"ID"}).AddRow(int64(1)).AddRow(int64(2)).AddRow(int64(3)))
	mock.ExpectQuery(`SELECT "ID" FROM "APP"\."T" WHERE \(\(\"ID\" > \?\)\) ORDER BY "ID" FETCH FIRST 3 ROWS ONLY`).WithArgs(int64(2)).WillReturnRows(sqlmock.NewRows([]string{"ID"}).AddRow(int64(3)))
	iter := newDB2KeysetIterator(source, table, []*model.ColumnInfo{id}, nil)
	first, err := iter.Next()
	require.NoError(t, err)
	second, err := iter.Next()
	require.NoError(t, err)
	require.True(t, second.IsLast)
	third, err := iter.Next()
	require.NoError(t, err)
	require.Nil(t, third)

	firstRange := db2RangeFromChunk(first, source.sourceColumns[0])
	where1, args1, err := (db2util.DB2Dialect{}).RenderRange(firstRange)
	require.NoError(t, err)
	require.Equal(t, `(("ID" <= ?))`, where1)
	require.Equal(t, []any{int64(2)}, args1)
	secondRange := db2RangeFromChunk(second, source.sourceColumns[0])
	where2, args2, err := (db2util.DB2Dialect{}).RenderRange(secondRange)
	require.NoError(t, err)
	require.Equal(t, `(("ID" > ?)) AND (("ID" <= ?))`, where2)
	require.Equal(t, []any{int64(2), int64(3)}, args2)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDB2KeysetChunkSizeOneAndCheckpointResume(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	id := &model.ColumnInfo{Name: pmodel.NewCIStr("ID"), Offset: 0, FieldType: *types.NewFieldType(mysql.TypeLonglong)}
	id.FieldType.SetFlag(mysql.NotNullFlag)
	info := &model.TableInfo{Columns: []*model.ColumnInfo{id}, Indices: []*model.IndexInfo{{Name: pmodel.NewCIStr("PRIMARY"), Primary: true, Unique: true, Columns: []*model.IndexColumn{{Name: id.Name, Offset: 0}}}}}
	table := &common.TableDiff{Schema: "APP", Table: "T", Info: info, ChunkSize: 1}
	source := &DB2Source{tableDiffs: []*common.TableDiff{table}, dbConn: db, schema: "APP", sourceColumns: map[int]map[string]string{0: {"ID": "ID"}}, orderKeys: map[int][]*model.ColumnInfo{0: {id}}}
	mock.ExpectQuery(`SELECT "ID" FROM "APP"\."T" WHERE \(\(\"ID\" > \?\)\) ORDER BY "ID" FETCH FIRST 2 ROWS ONLY`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"ID"}).AddRow(int64(2)))
	start := &splitter.RangeInfo{ChunkRange: &chunk.Range{Index: &chunk.ChunkID{ChunkIndex: 0}, Bounds: []*chunk.Bound{{Column: "ID", Upper: "1", UpperValue: int64(1), HasUpper: true}}}}
	iter := newDB2KeysetIterator(source, table, []*model.ColumnInfo{id}, start)
	rangeInfo, err := iter.Next()
	require.NoError(t, err)
	require.True(t, rangeInfo.IsLast)
	require.Equal(t, "2", rangeInfo.Bounds[0].Upper)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDB2KeysetChunkCoverageIncludesEveryBoundaryOnce(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	id := db2TestKeyColumn("ID", 0, mysql.TypeLonglong)
	table := db2TestTable(id, 2)
	source := db2TestSource(db, table, id)
	mock.ExpectQuery(`SELECT "ID" FROM "APP"\."T" ORDER BY "ID" FETCH FIRST 3 ROWS ONLY`).WillReturnRows(sqlmock.NewRows([]string{"ID"}).AddRow(int64(1)).AddRow(int64(2)).AddRow(int64(3)))
	mock.ExpectQuery(`SELECT "ID" FROM "APP"\."T" WHERE \(\(\"ID\" > \?\)\) ORDER BY "ID" FETCH FIRST 3 ROWS ONLY`).WithArgs(int64(2)).WillReturnRows(sqlmock.NewRows([]string{"ID"}).AddRow(int64(3)))
	iter := newDB2KeysetIterator(source, table, []*model.ColumnInfo{id}, nil)
	first, err := iter.Next()
	require.NoError(t, err)
	second, err := iter.Next()
	require.NoError(t, err)
	require.True(t, second.IsLast)

	seen := make(map[int64]int)
	for _, value := range []int64{1, 2, 3} {
		for _, r := range []*chunk.Range{first, second} {
			if db2IntRangeContains(r, value) {
				seen[value]++
			}
		}
	}
	require.Equal(t, map[int64]int{1: 1, 2: 1, 3: 1}, seen)
	require.True(t, first.DB2UpperInclusive)
	require.True(t, second.DB2UpperInclusive)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDB2KeysetCompositeAndStringRanges(t *testing.T) {
	id := db2TestKeyColumn("ID", 0, mysql.TypeLonglong)
	code := db2TestKeyColumn("CODE", 1, mysql.TypeVarString)
	composite := &chunk.Range{Bounds: []*chunk.Bound{
		{Column: "ID", LowerValue: int64(1), UpperValue: int64(2), HasLower: true, HasUpper: true},
		{Column: "CODE", LowerValue: "b", UpperValue: "a", HasLower: true, HasUpper: true},
	}}
	where, args, err := (db2util.DB2Dialect{}).RenderRange(db2RangeFromChunk(composite, map[string]string{"ID": "ID", "CODE": "CODE"}))
	require.NoError(t, err)
	require.Equal(t, `(("ID" > ?) OR ("ID" = ? AND "CODE" > ?)) AND (("ID" < ?) OR ("ID" = ? AND "CODE" <= ?))`, where)
	require.Equal(t, []any{int64(1), int64(1), "b", int64(2), int64(2), "a"}, args)

	// (1,"b") is in the first interval only and (2,"a") is in the next;
	// the lexicographic boundary itself is never lost or duplicated.
	first := &chunk.Range{Bounds: []*chunk.Bound{{Column: "ID", UpperValue: int64(1), HasUpper: true}, {Column: "CODE", UpperValue: "b", HasUpper: true}}}
	second := composite
	require.True(t, db2TupleRangeContains(first, []any{int64(1), "b"}))
	require.False(t, db2TupleRangeContains(second, []any{int64(1), "b"}))
	require.False(t, db2TupleRangeContains(first, []any{int64(2), "a"}))
	require.True(t, db2TupleRangeContains(second, []any{int64(2), "a"}))
	_ = id
	_ = code
}

func TestDB2RowsIteratorUsesSelectedKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	id := db2TestKeyColumn("ID", 0, mysql.TypeLonglong)
	name := db2TestKeyColumn("NAME", 1, mysql.TypeVarString)
	table := db2TestTable(id, 2)
	table.Info.Columns = []*model.ColumnInfo{id, name}
	table.Fields = "NAME"
	table.OrderKeyColumns = []string{"NAME"}
	source := &DB2Source{tableDiffs: []*common.TableDiff{table}, dbConn: db, schema: "APP", sourceColumns: map[int]map[string]string{0: {"ID": "ID", "NAME": "NAME"}}, orderKeys: map[int][]*model.ColumnInfo{0: {name}}}
	rangeInfo := &splitter.RangeInfo{ChunkRange: &chunk.Range{Index: &chunk.ChunkID{TableIndex: 0}, Bounds: []*chunk.Bound{{Column: "NAME", UpperValue: "m", HasUpper: true}}}}
	mock.ExpectQuery(`SELECT "ID" AS "ID", "NAME" AS "NAME" FROM "APP"\."T" WHERE \(\(\"NAME\" <= \?\)\) ORDER BY "NAME"`).WithArgs("m").WillReturnRows(sqlmock.NewRows([]string{"ID", "NAME"}).AddRow(int64(1), "a"))
	rows, err := source.GetRowsIterator(context.Background(), rangeInfo)
	require.NoError(t, err)
	defer rows.Close()
	row, err := rows.Next()
	require.NoError(t, err)
	require.Equal(t, []byte("a"), row["NAME"].Data)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDB2RowsIteratorAcceptsUnboundedFullTableChunk(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	id := db2TestKeyColumn("ID", 0, mysql.TypeLonglong)
	table := db2TestTable(id, 2)
	source := db2TestSource(db, table, id)
	rangeInfo := &splitter.RangeInfo{ChunkRange: &chunk.Range{Index: &chunk.ChunkID{TableIndex: 0}, Where: "((TRUE) AND (TRUE))"}}
	mock.ExpectQuery(`SELECT "ID" AS "ID" FROM "APP"\."T" ORDER BY "ID"`).WillReturnRows(sqlmock.NewRows([]string{"ID"}).AddRow(int64(1)))
	rows, err := source.GetRowsIterator(context.Background(), rangeInfo)
	require.NoError(t, err)
	rows.Close()
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTiDBRangeFromDB2KeysetChunkUsesTypedBounds(t *testing.T) {
	r := &chunk.Range{Bounds: []*chunk.Bound{
		{Column: "ID", Lower: "10", Upper: "20", LowerValue: int64(10), UpperValue: int64(20), HasLower: true, HasUpper: true},
		{Column: "CODE", Lower: "a", Upper: "z", LowerValue: "a", UpperValue: "z", HasLower: true, HasUpper: true},
	}}
	where, args, err := tiDBRangeFromChunk(r)
	require.NoError(t, err)
	require.Equal(t, "((`ID` > ?) OR (`ID` = ? AND `CODE` > ?)) AND ((`ID` < ?) OR (`ID` = ? AND `CODE` <= ?))", where)
	require.Equal(t, []any{int64(10), int64(10), "a", int64(20), int64(20), "z"}, args)
}

func db2TestKeyColumn(name string, offset int, typ byte) *model.ColumnInfo {
	column := &model.ColumnInfo{Name: pmodel.NewCIStr(name), Offset: offset, FieldType: *types.NewFieldType(typ)}
	column.FieldType.SetFlag(mysql.NotNullFlag)
	return column
}

func db2TestTable(key *model.ColumnInfo, chunkSize int64) *common.TableDiff {
	info := &model.TableInfo{Columns: []*model.ColumnInfo{key}, Indices: []*model.IndexInfo{{Name: pmodel.NewCIStr("PRIMARY"), Primary: true, Unique: true, Columns: []*model.IndexColumn{{Name: key.Name, Offset: key.Offset}}}}}
	return &common.TableDiff{Schema: "APP", Table: "T", Info: info, ChunkSize: chunkSize}
}

func db2TestSource(db *sql.DB, table *common.TableDiff, key *model.ColumnInfo) *DB2Source {
	return &DB2Source{tableDiffs: []*common.TableDiff{table}, dbConn: db, schema: "APP", sourceColumns: map[int]map[string]string{0: {key.Name.O: key.Name.O}}, orderKeys: map[int][]*model.ColumnInfo{0: {key}}}
}

func db2IntRangeContains(r *chunk.Range, value int64) bool {
	return db2TupleRangeContains(r, []any{value})
}

func db2TupleRangeContains(r *chunk.Range, value []any) bool {
	lower, upper := make([]any, len(r.Bounds)), make([]any, len(r.Bounds))
	for i, bound := range r.Bounds {
		lower[i], upper[i] = bound.LowerValue, bound.UpperValue
	}
	if r.Bounds[0].HasLower && db2TupleCompare(value, lower) <= 0 {
		return false
	}
	return !r.Bounds[0].HasUpper || db2TupleCompare(value, upper) <= 0
}

func db2TupleCompare(left, right []any) int {
	for i := range left {
		if fmt.Sprint(left[i]) == fmt.Sprint(right[i]) {
			continue
		}
		if fmt.Sprint(left[i]) < fmt.Sprint(right[i]) {
			return -1
		}
		return 1
	}
	return 0
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
