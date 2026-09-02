package source

import (
	"context"
	"encoding/binary"
	"time"

	"github.com/pingcap/tidb-tools/pkg/dbutil"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/canonical"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/splitter"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser/mysql"
)

// CanonicalSource replaces database-specific checksum SQL with CanonicalV1.
// Both endpoints are wrapped together for Db2 -> TiDB comparisons.
type CanonicalSource struct{ Source }

func (s CanonicalSource) GetCountAndMd5(ctx context.Context, tableRange *splitter.RangeInfo) *ChecksumInfo {
	started := time.Now()
	iterator, err := s.Source.GetRowsIterator(ctx, tableRange)
	if err != nil {
		return &ChecksumInfo{Err: err, Cost: time.Since(started)}
	}
	defer iterator.Close()
	table := s.GetTables()[tableRange.GetTableIndex()]
	columns := canonicalColumns(table.Info.Columns)
	rows := make([][]any, 0)
	for {
		row, err := iterator.Next()
		if err != nil {
			return &ChecksumInfo{Err: err, Cost: time.Since(started)}
		}
		if row == nil {
			break
		}
		rows = append(rows, canonicalValues(table.Info.Columns, row))
	}
	digest, err := canonical.DigestRows(columns, rows)
	if err != nil {
		return &ChecksumInfo{Err: err, Cost: time.Since(started)}
	}
	return &ChecksumInfo{Checksum: binary.BigEndian.Uint64(digest[:8]), Digest: digest, Algorithm: canonical.Version, Count: int64(len(rows)), Cost: time.Since(started)}
}

func canonicalValues(columns []*model.ColumnInfo, row map[string]*dbutil.ColumnData) []any {
	values := make([]any, len(columns))
	for i, column := range columns {
		data := row[column.Name.O]
		if data == nil || data.IsNull {
			continue
		}
		values[i] = data.Data
	}
	return values
}

func canonicalColumns(columns []*model.ColumnInfo) []canonical.Column {
	result := make([]canonical.Column, 0, len(columns))
	for _, column := range columns {
		kind := canonical.KindString
		switch column.FieldType.GetType() {
		case mysql.TypeTiny, mysql.TypeShort, mysql.TypeLong, mysql.TypeInt24, mysql.TypeLonglong:
			kind = canonical.KindInteger
		case mysql.TypeNewDecimal:
			kind = canonical.KindDecimal
		case mysql.TypeFloat, mysql.TypeDouble:
			kind = canonical.KindFloat
		case mysql.TypeDate:
			kind = canonical.KindDate
		case mysql.TypeDuration:
			kind = canonical.KindTime
		case mysql.TypeTimestamp, mysql.TypeDatetime:
			kind = canonical.KindTimestamp
		case mysql.TypeBit:
			kind = canonical.KindBoolean
		case mysql.TypeBlob, mysql.TypeTinyBlob, mysql.TypeMediumBlob, mysql.TypeLongBlob:
			kind = canonical.KindLOB
		case mysql.TypeString:
			kind = canonical.KindString
		}
		result = append(result, canonical.Column{Name: column.Name.O, Kind: kind, Scale: column.FieldType.GetDecimal(), TrimChar: column.FieldType.GetType() == mysql.TypeString, MaxLOBBytes: 16 << 20})
	}
	return result
}
