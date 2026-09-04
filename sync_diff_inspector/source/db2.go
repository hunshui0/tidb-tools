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
	"github.com/pingcap/tidb/pkg/parser/mysql"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// DB2Source is deliberately single-instance. It never produces repair SQL;
// repair SQL remains a TiDB target responsibility.
type DB2Source struct {
	tableDiffs    []*common.TableDiff
	dbConn        *sql.DB
	schema        string
	sourceColumns map[int]map[string]string
	orderKeys     map[int][]*model.ColumnInfo
	textDecoder   *encoding.Decoder
}

type DB2TableAnalyzer struct{ source *DB2Source }

func (a DB2TableAnalyzer) AnalyzeSplitter(ctx context.Context, table *common.TableDiff, start *splitter.RangeInfo) (splitter.ChunkIterator, error) {
	keys, err := a.source.orderColumns(table)
	if err != nil {
		return nil, err
	}
	return newDB2KeysetIteratorWithContext(ctx, a.source, table, keys, start), nil
}

func (s *DB2Source) orderColumns(table *common.TableDiff) ([]*model.ColumnInfo, error) {
	var columns []*model.ColumnInfo
	if fields := strings.TrimSpace(table.Fields); fields != "" {
		seen := make(map[string]struct{})
		for _, field := range strings.Split(fields, ",") {
			field = strings.TrimSpace(field)
			if field == "" {
				continue
			}
			name := strings.Trim(field, "`\" ")
			key := strings.ToLower(name)
			if _, ok := seen[key]; ok {
				return nil, errors.Errorf("db2 chunk key column %q is duplicated", field)
			}
			seen[key] = struct{}{}
			column := dbutil.FindColumnByName(table.Info.Columns, name)
			if column == nil {
				return nil, errors.Errorf("db2 chunk key column %q does not exist in %s.%s", field, table.Schema, table.Table)
			}
			if !mysql.HasNotNullFlag(column.GetFlag()) {
				return nil, errors.Errorf("db2 chunk key column %q is nullable; configure a non-null primary or unique key", field)
			}
			columns = append(columns, column)
		}
		if len(columns) == 0 {
			return nil, errors.Errorf("db2 table %s.%s has empty index-fields", table.Schema, table.Table)
		}
		if !matchesUniqueIndex(table.Info, columns) {
			return nil, errors.Errorf("db2 index-fields for %s.%s are not a declared primary or unique key", table.Schema, table.Table)
		}
	} else {
		indices := append([]*model.IndexInfo(nil), table.Info.Indices...)
		for pass := 0; pass < 2 && len(columns) == 0; pass++ {
			for _, index := range indices {
				if (pass == 0 && !index.Primary) || (pass == 1 && (index.Primary || !index.Unique)) {
					continue
				}
				candidate := make([]*model.ColumnInfo, 0, len(index.Columns))
				stable := true
				for _, indexColumn := range index.Columns {
					if indexColumn.Offset < 0 || indexColumn.Offset >= len(table.Info.Columns) {
						stable = false
						break
					}
					column := table.Info.Columns[indexColumn.Offset]
					if !mysql.HasNotNullFlag(column.GetFlag()) {
						stable = false
						break
					}
					candidate = append(candidate, column)
				}
				if stable && len(candidate) > 0 {
					columns = candidate
					break
				}
			}
		}
	}
	if len(columns) == 0 {
		return nil, errors.Errorf("db2 table %s.%s has no stable non-null chunk key; configure index-fields", table.Schema, table.Table)
	}
	return columns, nil
}

func matchesUniqueIndex(info *model.TableInfo, columns []*model.ColumnInfo) bool {
	for _, index := range info.Indices {
		if !index.Primary && !index.Unique {
			continue
		}
		if len(index.Columns) != len(columns) {
			continue
		}
		matched := true
		for position, indexColumn := range index.Columns {
			if indexColumn.Offset < 0 || indexColumn.Offset >= len(info.Columns) ||
				!strings.EqualFold(info.Columns[indexColumn.Offset].Name.O, columns[position].Name.O) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

type db2KeysetIterator struct {
	ctx        context.Context
	cancel     context.CancelFunc
	source     *DB2Source
	table      *common.TableDiff
	keys       []*model.ColumnInfo
	lower      []db2util.Bound
	chunkSize  int
	chunkIndex int
	done       bool
	closed     bool
}

func newDB2KeysetIterator(source *DB2Source, table *common.TableDiff, keys []*model.ColumnInfo, start *splitter.RangeInfo) splitter.ChunkIterator {
	return newDB2KeysetIteratorWithContext(context.Background(), source, table, keys, start)
}

func newDB2KeysetIteratorWithContext(ctx context.Context, source *DB2Source, table *common.TableDiff, keys []*model.ColumnInfo, start *splitter.RangeInfo) splitter.ChunkIterator {
	queryCtx, cancel := context.WithCancel(ctx)
	iterator := &db2KeysetIterator{ctx: queryCtx, cancel: cancel, source: source, table: table, keys: keys, chunkSize: int(table.ChunkSize), chunkIndex: 0}
	if iterator.chunkSize <= 0 {
		iterator.chunkSize = 50000
	}
	if start != nil && start.ChunkRange != nil {
		iterator.chunkIndex = start.GetChunkIndex() + 1
		iterator.lower = make([]db2util.Bound, len(keys))
		for index, bound := range start.ChunkRange.Bounds {
			if index >= len(iterator.lower) || !bound.HasUpper {
				continue
			}
			value := bound.UpperValue
			if value == nil {
				value = bound.Upper
			}
			iterator.lower[index] = db2util.Bound{Set: true, Value: value}
		}
	}
	return iterator
}

func (i *db2KeysetIterator) Next() (*chunk.Range, error) {
	if i.done || i.closed {
		return nil, nil
	}
	sourceColumns := i.source.sourceColumns[i.tableIndex()]
	queryColumns := make([]string, 0, len(i.keys))
	keyNames := make([]string, 0, len(i.keys))
	for _, key := range i.keys {
		name := sourceColumns[key.Name.O]
		queryColumns = append(queryColumns, name)
		keyNames = append(keyNames, name)
	}
	rangeSpec := db2util.Range{Columns: keyNames, Lower: i.lower}
	query, args, err := (db2util.DB2Dialect{}).RenderSelect(i.source.schema, i.table.Table, queryColumns, rangeSpec, i.chunkSize+1)
	if err != nil {
		return nil, err
	}
	rows, err := i.source.dbConn.QueryContext(i.ctx, query, args...)
	if err != nil {
		return nil, errors.Annotate(db2util.ClassifyError(err), "fetch db2 chunk boundary")
	}
	defer rows.Close()
	values := make([][]any, 0, i.chunkSize+1)
	for rows.Next() {
		row := make([]any, len(i.keys))
		dest := make([]any, len(row))
		for index := range row {
			dest[index] = &row[index]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, errors.Annotate(err, "scan db2 chunk boundary")
		}
		values = append(values, row)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Annotate(err, "iterate db2 chunk boundary")
	}
	if len(values) == 0 {
		i.done = true
		return nil, nil
	}
	final := len(values) <= i.chunkSize
	if len(values) > i.chunkSize {
		values = values[:i.chunkSize]
	}
	upper := make([]db2util.Bound, len(i.keys))
	bounds := make([]*chunk.Bound, len(i.keys))
	for index, value := range values[len(values)-1] {
		upper[index] = db2util.Bound{Set: true, Value: value}
		text := db2ValueString(value)
		lowerBound := ""
		if index < len(i.lower) && i.lower[index].Set {
			lowerBound = db2ValueString(i.lower[index].Value)
		}
		bounds[index] = &chunk.Bound{Column: i.keys[index].Name.O, Lower: lowerBound, Upper: text, HasLower: index < len(i.lower) && i.lower[index].Set, HasUpper: true, LowerValue: boundValue(i.lower, index), UpperValue: value}
	}
	i.lower = upper
	if final {
		i.done = true
	}
	result := &chunk.Range{Index: &chunk.ChunkID{ChunkIndex: i.chunkIndex, ChunkCnt: 0}, Type: chunk.Limit, Bounds: bounds, IsFirst: i.chunkIndex == 0, IsLast: final, DB2UpperInclusive: true}
	i.chunkIndex++
	return result, nil
}

func (i *db2KeysetIterator) tableIndex() int {
	for index, table := range i.source.tableDiffs {
		if table == i.table {
			return index
		}
	}
	return 0
}
func (i *db2KeysetIterator) Close() {
	if !i.closed {
		i.closed = true
		if i.cancel != nil {
			i.cancel()
		}
	}
}

func boundValue(bounds []db2util.Bound, index int) any {
	if index < len(bounds) && bounds[index].Set {
		return bounds[index].Value
	}
	return nil
}
func db2ValueString(value any) string {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func NewDB2Source(ctx context.Context, tableDiffs []*common.TableDiff, ds *config.DataSource) (Source, error) {
	if ds == nil || ds.Conn == nil {
		return nil, errors.New("db2 source requires an initialized connection")
	}
	schema := ds.Schema
	if schema == "" {
		return nil, errors.New("db2 source requires schema")
	}
	textDecoder, err := newDB2TextDecoder(ds.SourceCharset)
	if err != nil {
		return nil, err
	}
	sourceColumns := make(map[int]map[string]string, len(tableDiffs))
	orderKeys := make(map[int][]*model.ColumnInfo, len(tableDiffs))
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
		if strings.TrimSpace(table.Fields) != "" {
			sourceKeyColumns, err := columnsByFields(info, table.Fields)
			if err != nil {
				return nil, err
			}
			if !matchesUniqueIndex(info, sourceKeyColumns) {
				return nil, errors.Errorf("db2 index-fields for %s.%s are not a declared primary or unique key in Db2", table.Schema, table.Table)
			}
		}
		keys, err := (&DB2Source{sourceColumns: sourceColumns}).orderColumns(table)
		if err != nil {
			return nil, err
		}
		// Automatic selection is based on the target TableInfo. Verify that the
		// corresponding Db2 columns form the same declared unique key; otherwise
		// chunk boundaries would be stable on one endpoint only.
		sourceKeys := make([]*model.ColumnInfo, 0, len(keys))
		for _, key := range keys {
			name := mapped[key.Name.O]
			column := dbutil.FindColumnByName(info.Columns, name)
			if column == nil || !mysql.HasNotNullFlag(column.GetFlag()) {
				return nil, errors.Errorf("db2 ordering key column %s is not non-null in source metadata for %s.%s", key.Name.O, schema, table.Table)
			}
			sourceKeys = append(sourceKeys, column)
		}
		if !matchesUniqueIndex(info, sourceKeys) {
			return nil, errors.Errorf("db2 ordering key for %s.%s is not a declared primary or unique key in Db2", schema, table.Table)
		}
		orderKeys[index] = keys
		names := make([]string, 0, len(keys))
		for _, key := range keys {
			names = append(names, key.Name.O)
		}
		table.OrderKeyColumns = names
	}
	return &DB2Source{tableDiffs: tableDiffs, dbConn: ds.Conn, schema: db2util.NormalizeIdentifier(schema), sourceColumns: sourceColumns, orderKeys: orderKeys, textDecoder: textDecoder}, nil
}

func columnsByFields(info *model.TableInfo, fields string) ([]*model.ColumnInfo, error) {
	columns := make([]*model.ColumnInfo, 0)
	seen := make(map[string]struct{})
	for _, field := range strings.Split(fields, ",") {
		name := strings.Trim(strings.TrimSpace(field), "`\" ")
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return nil, errors.Errorf("db2 chunk key column %q is duplicated", name)
		}
		seen[key] = struct{}{}
		column := dbutil.FindColumnByName(info.Columns, name)
		if column == nil {
			return nil, errors.Errorf("db2 chunk key column %q does not exist in Db2 metadata", name)
		}
		if !mysql.HasNotNullFlag(column.GetFlag()) {
			return nil, errors.Errorf("db2 chunk key column %q is nullable in Db2 metadata", name)
		}
		columns = append(columns, column)
	}
	return columns, nil
}

func (s *DB2Source) GetTableAnalyzer() TableAnalyzer { return DB2TableAnalyzer{source: s} }

// GetOrderKeyColumns exposes the validated key used by Db2 chunking. It is
// consumed by the row comparator through an optional interface so existing
// MySQL/TiDB sources retain their historical key selection.
func (s *DB2Source) GetOrderKeyColumns(tableIndex int) []*model.ColumnInfo {
	if keys := s.orderKeys[tableIndex]; len(keys) > 0 {
		return keys
	}
	return nil
}
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
	keys := s.orderKeys[r.GetTableIndex()]
	if len(keys) == 0 {
		var err error
		keys, err = s.orderColumns(table)
		if err != nil {
			return nil, err
		}
	}
	for _, column := range keys {
		order = append(order, db2util.QuoteIdentifier(s.sourceColumns[r.GetTableIndex()][column.Name.O]))
	}
	query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(columns, ", "), db2util.QualifiedTable(schema, sourceTable))
	structured := db2RangeFromChunk(r.GetChunk(), s.sourceColumns[r.GetTableIndex()])
	where, args, err := (db2util.DB2Dialect{}).RenderRange(structured)
	if err != nil {
		return nil, err
	}
	if where != "" {
		query += " WHERE " + where
	}
	if len(order) > 0 {
		query += " ORDER BY " + strings.Join(order, ", ")
	}
	rows, err := s.dbConn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.Annotate(db2util.ClassifyError(err), "scan db2 rows")
	}
	textColumns, err := db2CharacterColumns(rows)
	if err != nil {
		_ = rows.Close()
		return nil, errors.Annotate(err, "inspect db2 result columns")
	}
	return &db2RowsIterator{rows: rows, textDecoder: s.textDecoder, textColumns: textColumns}, nil
}

func db2RangeFromChunk(r *chunk.Range, sourceColumns map[string]string) db2util.Range {
	// Db2 keyset chunks use (previousUpper, currentUpper] for every chunk.
	// Keep this invariant independent of the legacy Range flag so a restored
	// checkpoint cannot accidentally reopen a gap at an intermediate bound.
	result := db2util.Range{Columns: make([]string, 0, len(r.Bounds)), Lower: make([]db2util.Bound, len(r.Bounds)), Upper: make([]db2util.Bound, len(r.Bounds)), UpperInclusive: true}
	for index, bound := range r.Bounds {
		column := sourceColumns[bound.Column]
		if column == "" {
			column = bound.Column
		}
		result.Columns = append(result.Columns, column)
		if bound.HasLower {
			value := bound.LowerValue
			if value == nil {
				value = bound.Lower
			}
			result.Lower[index] = db2util.Bound{Set: true, Value: value}
		}
		if bound.HasUpper {
			value := bound.UpperValue
			if value == nil {
				value = bound.Upper
			}
			result.Upper[index] = db2util.Bound{Set: true, Value: value}
		}
	}
	return result
}

type db2RowsIterator struct {
	rows        *sql.Rows
	textDecoder *encoding.Decoder
	textColumns map[string]struct{}
}

func (i *db2RowsIterator) Next() (map[string]*dbutil.ColumnData, error) {
	if i.rows.Next() {
		row, err := dbutil.ScanRow(i.rows)
		if err != nil {
			return nil, err
		}
		if err := decodeDB2TextRow(row, i.textColumns, i.textDecoder); err != nil {
			return nil, err
		}
		return row, nil
	}
	return nil, i.rows.Err()
}
func (i *db2RowsIterator) Close() { _ = i.rows.Close() }

func newDB2TextDecoder(charset string) (*encoding.Decoder, error) {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "":
		return nil, nil
	case "gbk", "cp936", "936":
		return simplifiedchinese.GBK.NewDecoder(), nil
	default:
		return nil, errors.Errorf("unsupported db2 source-charset %q; supported values: gbk", charset)
	}
}

func db2CharacterColumns(rows *sql.Rows) (map[string]struct{}, error) {
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	columns := make(map[string]struct{})
	for _, column := range types {
		switch strings.ToUpper(strings.TrimSpace(column.DatabaseTypeName())) {
		case "CHAR", "CHARACTER", "VARCHAR", "CHARACTER VARYING", "CLOB":
			columns[column.Name()] = struct{}{}
		}
	}
	return columns, nil
}

func decodeDB2TextRow(row map[string]*dbutil.ColumnData, textColumns map[string]struct{}, decoder *encoding.Decoder) error {
	if decoder == nil {
		return nil
	}
	for column := range textColumns {
		data := row[column]
		if data == nil || data.IsNull {
			continue
		}
		decoded, err := decoder.Bytes(data.Data)
		if err != nil {
			return errors.Annotatef(err, "decode db2 column %s", column)
		}
		data.Data = decoded
	}
	return nil
}
