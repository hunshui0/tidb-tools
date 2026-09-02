package utils

import (
	"testing"

	"github.com/pingcap/tidb-tools/pkg/dbutil"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser/mysql"
	"github.com/pingcap/tidb/pkg/types"
	"github.com/stretchr/testify/require"
)

func TestCompareColumnDataKeepsIntegerAndDecimalPrecision(t *testing.T) {
	bigint := &model.ColumnInfo{FieldType: *types.NewFieldType(mysql.TypeLonglong)}
	cmp, err := compareColumnData(&dbutil.ColumnData{Data: []byte("9223372036854775807")}, &dbutil.ColumnData{Data: []byte("9223372036854775806")}, bigint, "")
	require.NoError(t, err)
	require.Equal(t, 1, cmp)

	decimal := &model.ColumnInfo{FieldType: *types.NewFieldType(mysql.TypeNewDecimal)}
	cmp, err = compareColumnData(&dbutil.ColumnData{Data: []byte("1.20")}, &dbutil.ColumnData{Data: []byte("1.2")}, decimal, "")
	require.NoError(t, err)
	require.Equal(t, 0, cmp)
}
