package utils

import (
	"bytes"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/pingcap/errors"
	"github.com/pingcap/tidb-tools/pkg/dbutil"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser/mysql"
	"github.com/pingcap/tidb/pkg/types"
	"github.com/pingcap/tidb/pkg/util/collate"
)

// compareColumnData preserves integer and decimal precision. Float64 is used
// only for FLOAT/DOUBLE columns, never as a generic numeric representation.
func compareColumnData(left, right *dbutil.ColumnData, column *model.ColumnInfo, collationName string) (int, error) {
	if left.IsNull || right.IsNull {
		if left.IsNull && right.IsNull {
			return 0, nil
		}
		// Preserve the historical string-key behavior: the existing MySQL row
		// iterator keeps the raw bytes for NULL string columns.
		if NeedQuotes(column.FieldType.GetType()) {
			return bytes.Compare(left.Data, right.Data), nil
		}
		if left.IsNull {
			return -1, nil
		}
		return 1, nil
	}
	a, b := string(left.Data), string(right.Data)
	switch column.FieldType.GetType() {
	case mysql.TypeTiny, mysql.TypeShort, mysql.TypeLong, mysql.TypeInt24, mysql.TypeLonglong:
		return compareBigInt(a, b)
	case mysql.TypeNewDecimal:
		return compareDecimal(a, b)
	case mysql.TypeFloat, mysql.TypeDouble:
		return compareFloat(a, b)
	case mysql.TypeDate, mysql.TypeDuration, mysql.TypeDatetime, mysql.TypeTimestamp:
		return compareTemporal(a, b, column.FieldType.GetType())
	}
	if IsBinaryColumn(column) || column.FieldType.GetType() == mysql.TypeBlob || column.FieldType.GetType() == mysql.TypeTinyBlob || column.FieldType.GetType() == mysql.TypeMediumBlob || column.FieldType.GetType() == mysql.TypeLongBlob {
		return bytes.Compare(left.Data, right.Data), nil
	}
	if collationName == "" {
		collationName = "utf8mb4_bin"
	}
	leftDatum := types.NewCollationStringDatum(a, collationName)
	rightDatum := types.NewCollationStringDatum(b, collationName)
	cmp, err := leftDatum.Compare(types.Context{}, &rightDatum, collate.GetCollator(collationName))
	return cmp, errors.Trace(err)
}

func compareBigInt(a, b string) (int, error) {
	left, ok := new(big.Int).SetString(strings.TrimSpace(a), 10)
	if !ok {
		return 0, errors.Errorf("invalid integer %q", a)
	}
	right, ok := new(big.Int).SetString(strings.TrimSpace(b), 10)
	if !ok {
		return 0, errors.Errorf("invalid integer %q", b)
	}
	return left.Cmp(right), nil
}

func compareDecimal(a, b string) (int, error) {
	left, ok := new(big.Rat).SetString(strings.TrimSpace(a))
	if !ok {
		return 0, errors.Errorf("invalid decimal %q", a)
	}
	right, ok := new(big.Rat).SetString(strings.TrimSpace(b))
	if !ok {
		return 0, errors.Errorf("invalid decimal %q", b)
	}
	return left.Cmp(right), nil
}

func compareFloat(a, b string) (int, error) {
	left, err := strconv.ParseFloat(a, 64)
	if err != nil {
		return 0, errors.Trace(err)
	}
	right, err := strconv.ParseFloat(b, 64)
	if err != nil {
		return 0, errors.Trace(err)
	}
	if math.IsNaN(left) || math.IsNaN(right) {
		if math.IsNaN(left) && math.IsNaN(right) {
			return 0, nil
		}
		if math.IsNaN(left) {
			return -1, nil
		}
		return 1, nil
	}
	if math.Abs(left-right) <= 1e-6 {
		return 0, nil
	}
	if left < right {
		return -1, nil
	}
	return 1, nil
}

func compareTemporal(a, b string, typ byte) (int, error) {
	if a == b {
		return 0, nil
	}
	layouts := []string{"2006-01-02 15:04:05.999999999", time.RFC3339Nano}
	if typ == mysql.TypeDate {
		layouts = []string{"2006-01-02"}
	}
	if typ == mysql.TypeDuration {
		layouts = []string{"15:04:05.999999999", "15:04:05"}
	}
	for _, layout := range layouts {
		left, leftErr := time.Parse(layout, a)
		right, rightErr := time.Parse(layout, b)
		if leftErr == nil && rightErr == nil {
			if left.Before(right) {
				return -1, nil
			}
			if left.After(right) {
				return 1, nil
			}
			return 0, nil
		}
	}
	return 0, errors.Errorf("invalid temporal values %q and %q", a, b)
}
