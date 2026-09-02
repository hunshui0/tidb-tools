package canonical

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCanonicalV1CrossDriverValues(t *testing.T) {
	columns := []Column{{Name: "id", Kind: KindInteger}, {Name: "price", Kind: KindDecimal, Scale: 2}, {Name: "at", Kind: KindTimestamp}, {Name: "name", Kind: KindString, TrimChar: true}, {Name: "bin", Kind: KindBinary}}
	db2 := []any{int64(9223372036854775807), "1.2", time.Date(2024, 1, 2, 3, 4, 5, 6000, time.FixedZone("x", 3600)), "中文  ", []byte{0, 255}}
	tidb := []any{[]byte("9223372036854775807"), []byte("1.20"), "2024-01-02T02:04:05.000006Z", []byte("中文"), []byte{0, 255}}
	a, err := EncodeRow(columns, db2)
	require.NoError(t, err)
	b, err := EncodeRow(columns, tidb)
	require.NoError(t, err)
	require.Equal(t, a, b)
}

func TestCanonicalV1DistinguishesNullEmptyAndTypes(t *testing.T) {
	stringColumn := []Column{{Name: "x", Kind: KindString}}
	nullRow, err := EncodeRow(stringColumn, []any{nil})
	require.NoError(t, err)
	emptyRow, err := EncodeRow(stringColumn, []any{""})
	require.NoError(t, err)
	require.NotEqual(t, nullRow, emptyRow)
	intRow, err := EncodeRow([]Column{{Name: "x", Kind: KindInteger}}, []any{"1"})
	require.NoError(t, err)
	require.False(t, bytes.Equal(emptyRow, intRow))
}

func TestCanonicalV1LimitsLOBAndFloatBoundaries(t *testing.T) {
	_, err := EncodeRow([]Column{{Name: "lob", Kind: KindLOB, MaxLOBBytes: 2}}, []any{[]byte("123")})
	require.ErrorContains(t, err, "LOB exceeds")
	zeroA, err := EncodeRow([]Column{{Name: "f", Kind: KindFloat}}, []any{float64(0)})
	require.NoError(t, err)
	zeroB, err := EncodeRow([]Column{{Name: "f", Kind: KindFloat}}, []any{float64(-0)})
	require.NoError(t, err)
	require.Equal(t, zeroA, zeroB)
}
