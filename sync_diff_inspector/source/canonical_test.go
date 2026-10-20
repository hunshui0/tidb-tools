package source

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/canonical"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/chunk"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/source/common"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/splitter"
	"github.com/pingcap/tidb/pkg/meta/model"
	pmodel "github.com/pingcap/tidb/pkg/parser/model"
	"github.com/pingcap/tidb/pkg/parser/mysql"
	"github.com/pingcap/tidb/pkg/types"
	"github.com/stretchr/testify/require"
)

func TestCanonicalDB2DateDriverValueMatchesTiDBDate(t *testing.T) {
	for _, mode := range []common.TableDiffMode{common.TableDiffModeKeyset, common.TableDiffModeUnorderedChecksum} {
		t.Run(string(mode), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			column := &model.ColumnInfo{Name: pmodel.NewCIStr("JYRQ"), FieldType: *types.NewFieldType(mysql.TypeDate)}
			table := &common.TableDiff{Schema: "eimp", Table: "T", Mode: mode, Info: &model.TableInfo{Columns: []*model.ColumnInfo{column}}}
			s := &DB2Source{dbConn: db, schema: "EIMP", tableDiffs: []*common.TableDiff{table},
				sourceColumns: map[int]map[string]string{0: {"JYRQ": "JYRQ"}}, orderKeys: map[int][]*model.ColumnInfo{0: {column}}}
			// Simulate the native driver's time.Time. GetRowsIterator uses the
			// real database/sql -> ScanRow -> CanonicalSource pipeline, which
			// serializes this as 2020-06-19T00:00:00+08:00 before canonicalization.
			value := time.Date(2020, 6, 19, 0, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
			mock.ExpectQuery("SELECT.*FROM").WillReturnRows(sqlmock.NewRowsWithColumnDefinition(
				sqlmock.NewColumn("JYRQ").OfType("DATE", time.Time{})).AddRow(value))
			result := s.GetCountAndMd5(context.Background(), &splitter.RangeInfo{ChunkRange: &chunk.Range{
				Index: &chunk.ChunkID{TableIndex: 0}, Type: chunk.Limit}})
			require.NoError(t, result.Err)
			require.EqualValues(t, 1, result.Count)
			columns := []canonical.Column{{Name: "JYRQ", Kind: canonical.KindDate}}
			var expected [32]byte
			if mode == common.TableDiffModeUnorderedChecksum {
				read := false
				expected, _, err = canonical.DigestRowsMultisetStream(columns, func() ([]any, error) {
					if read {
						return nil, io.EOF
					}
					read = true
					return []any{[]byte("2020-06-19")}, nil
				})
				require.Equal(t, canonical.MultisetVersion, result.Algorithm)
			} else {
				expected, err = canonical.DigestRows(columns, [][]any{{[]byte("2020-06-19")}})
				require.Equal(t, canonical.Version, result.Algorithm)
			}
			require.NoError(t, err)
			require.Equal(t, expected, result.Digest)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
