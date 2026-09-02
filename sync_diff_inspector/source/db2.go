package source

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/pingcap/errors"
	"github.com/pingcap/tidb-tools/pkg/dbutil"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/chunk"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/config"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/db2util"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/source/common"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/splitter"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/utils"
	"github.com/pingcap/tidb/pkg/meta/model"
)

// DB2Source is deliberately single-instance. It never produces repair SQL;
// repair SQL remains a TiDB target responsibility.
type DB2Source struct {
	tableDiffs    []*common.TableDiff
	dbConn        *sql.DB
	schema        string
	sourceColumns map[int]map[string]string
}

type DB2TableAnalyzer struct{}

func (DB2TableAnalyzer) AnalyzeSplitter(_ context.Context, table *common.TableDiff, _ *splitter.RangeInfo) (splitter.ChunkIterator, error) {
	if strings.TrimSpace(table.Fields) == "" && len(dbutil.SelectUniqueOrderKey(table.Info)) == 0 {
		// V1 must not use OFFSET without a stable unique key. A single ordered
		// block remains correct but is intentionally visible as the fallback.
		return &db2SingleChunkIterator{table: table}, nil
	}
	return &db2SingleChunkIterator{table: table}, nil
}

type db2SingleChunkIterator struct {
	table *common.TableDiff
	done  bool
}

func (i *db2SingleChunkIterator) Next() (*chunk.Range, error) {
	if i.done {
		return nil, nil
	}
	i.done = true
	return &chunk.Range{Index: &chunk.ChunkID{ChunkIndex: 0, ChunkCnt: 1}, Type: chunk.Others, IsFirst: true, IsLast: true, Where: "TRUE"}, nil
}
func (*db2SingleChunkIterator) Close() {}

func NewDB2Source(ctx context.Context, tableDiffs []*common.TableDiff, ds *config.DataSource) (Source, error) {
	if ds == nil || ds.Conn == nil {
		return nil, errors.New("db2 source requires an initialized connection")
	}
	schema := ds.Schema
	if schema == "" {
		return nil, errors.New("db2 source requires schema")
	}
	sourceColumns := make(map[int]map[string]string, len(tableDiffs))
	for index, table := range tableDiffs {
		if !common.AllTableExist(table.TableLack) {
			continue
		}
		if !utils.IsRangeTrivial(table.Range) {
			return nil, errors.Errorf("db2 source does not accept legacy SQL range strings for %s.%s; use the structured range checkpoint format", table.Schema, table.Table)
		}
		info, err := db2util.ReadTableInfo(ctx, ds.Conn, schema, table.Table)
		if err != nil {
			return nil, errors.Trace(err)
		}
		mapped := make(map[string]string, len(table.Info.Columns))
		for _, targetColumn := range table.Info.Columns {
			for _, sourceColumn := range info.Columns {
				if strings.EqualFold(targetColumn.Name.O, sourceColumn.Name.O) {
					mapped[targetColumn.Name.O] = sourceColumn.Name.O
					break
				}
			}
			if mapped[targetColumn.Name.O] == "" {
				return nil, errors.Errorf("db2 table %s.%s has no column compatible with target column %s", schema, table.Table, targetColumn.Name.O)
			}
		}
		sourceColumns[index] = mapped
	}
	return &DB2Source{tableDiffs: tableDiffs, dbConn: ds.Conn, schema: db2util.NormalizeIdentifier(schema), sourceColumns: sourceColumns}, nil
}

func (s *DB2Source) GetTableAnalyzer() TableAnalyzer { return DB2TableAnalyzer{} }
func (s *DB2Source) GetRangeIterator(ctx context.Context, r *splitter.RangeInfo, analyzer TableAnalyzer, splitThreadCount int) (RangeIterator, error) {
	return NewChunksIterator(ctx, analyzer, s.tableDiffs, r, splitThreadCount)
}
func (s *DB2Source) Close() { _ = s.dbConn.Close() }
func (s *DB2Source) GetCountAndMd5(ctx context.Context, r *splitter.RangeInfo) *ChecksumInfo {
	return CanonicalSource{Source: s}.GetCountAndMd5(ctx, r)
}
func (s *DB2Source) GetCountForLackTable(ctx context.Context, r *splitter.RangeInfo) int64 {
	schema, table := s.GetSourceTable(r)
	var count int64
	if err := s.dbConn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+db2util.QualifiedTable(schema, table)).Scan(&count); err != nil {
		return 0
	}
	return count
}
func (s *DB2Source) GetTables() []*common.TableDiff { return s.tableDiffs }
func (s *DB2Source) GetSourceTable(r *splitter.RangeInfo) (string, string) {
	table := s.tableDiffs[r.GetTableIndex()]
	return s.schema, db2util.NormalizeIdentifier(table.Table)
}
func (s *DB2Source) GetSourceStructInfo(ctx context.Context, index int) ([]*model.TableInfo, error) {
	table := s.tableDiffs[index]
	info, err := db2util.ReadTableInfo(ctx, s.dbConn, s.schema, table.Table)
	if err != nil {
		return nil, err
	}
	info, _ = utils.ResetColumns(info, table.IgnoreColumns)
	return []*model.TableInfo{info}, nil
}
func (*DB2Source) GenerateFixSQL(DMLType, map[string]*dbutil.ColumnData, map[string]*dbutil.ColumnData, int) string {
	return ""
}
func (s *DB2Source) GetDB() *sql.DB    { return s.dbConn }
func (*DB2Source) GetSnapshot() string { return "" }
func (s *DB2Source) GetRowsIterator(ctx context.Context, r *splitter.RangeInfo) (RowDataIterator, error) {
	table := s.tableDiffs[r.GetTableIndex()]
	schema, sourceTable := s.GetSourceTable(r)
	columns := make([]string, 0, len(table.Info.Columns))
	for _, column := range table.Info.Columns {
		// DB2 catalog names default to uppercase; the quoted alias preserves the
		// target column key expected by the existing comparison layer.
		columns = append(columns, db2util.QuoteIdentifier(s.sourceColumns[r.GetTableIndex()][column.Name.O])+" AS "+db2util.QuoteIdentifier(column.Name.O))
	}
	order := make([]string, 0)
	for _, column := range dbutil.SelectUniqueOrderKey(table.Info) {
		order = append(order, db2util.QuoteIdentifier(s.sourceColumns[r.GetTableIndex()][column.Name.O]))
	}
	query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(columns, ", "), db2util.QualifiedTable(schema, sourceTable))
	if len(order) > 0 {
		query += " ORDER BY " + strings.Join(order, ", ")
	}
	rows, err := s.dbConn.QueryContext(ctx, query)
	if err != nil {
		return nil, errors.Annotate(db2util.ClassifyError(err), "scan db2 rows")
	}
	return &db2RowsIterator{rows: rows}, nil
}

type db2RowsIterator struct{ rows *sql.Rows }

func (i *db2RowsIterator) Next() (map[string]*dbutil.ColumnData, error) {
	if i.rows.Next() {
		return dbutil.ScanRow(i.rows)
	}
	return nil, i.rows.Err()
}
func (i *db2RowsIterator) Close() { _ = i.rows.Close() }
