package canonical

import (
	"bytes"
	"io"
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

func TestDateDriverRepresentationsHaveSameCanonicalEncoding(t *testing.T) {
	column := []Column{{Name: "JYRQ", Kind: KindDate}}
	expected, err := EncodeRow(column, []any{"2020-06-19"})
	require.NoError(t, err)
	for _, value := range []any{
		[]byte("2020-06-19"),
		"2020-06-19T00:00:00+08:00",
		[]byte("2020-06-19T00:00:00+08:00"),
		"2020-06-19T00:00:00.000000000+14:00",
		"2020-06-19T23:00:00-12:00",
		"2020-06-19 00:00:00",
		"2020-06-19 00:00:00.000000",
		time.Date(2020, 6, 19, 0, 0, 0, 0, time.FixedZone("UTC+8", 8*3600)),
		time.Date(2020, 6, 19, 0, 0, 0, 0, time.FixedZone("UTC+14", 14*3600)),
	} {
		actual, err := EncodeRow(column, []any{value})
		require.NoError(t, err, "%v", value)
		require.Equal(t, expected, actual, "%v", value)
	}
	different, err := EncodeRow(column, []any{"2020-06-20T00:00:00+08:00"})
	require.NoError(t, err)
	require.NotEqual(t, expected, different)
}

func TestDateNormalizationRejectsInvalidDatesAndPreservesOtherTypes(t *testing.T) {
	for _, value := range []any{"2020-02-30", "2020-02-30T00:00:00+08:00", "2020-06-19garbage", "2020-06-19T99:00:00+08:00", "0000-00-00", 20200619} {
		_, err := EncodeRow([]Column{{Name: "JYRQ", Kind: KindDate}}, []any{value})
		require.ErrorContains(t, err, "CanonicalV1 column JYRQ")
	}
	// Timestamp values still represent instants and retain their existing UTC encoding.
	instant, err := EncodeRow([]Column{{Name: "t", Kind: KindTimestamp}}, []any{"2020-06-19T00:00:00+08:00"})
	require.NoError(t, err)
	utc, err := EncodeRow([]Column{{Name: "t", Kind: KindTimestamp}}, []any{"2020-06-18T16:00:00Z"})
	require.NoError(t, err)
	require.Equal(t, utc, instant)
	// Date-like strings in character columns remain character data.
	a, err := EncodeRow([]Column{{Name: "s", Kind: KindString}}, []any{"2020-06-19"})
	require.NoError(t, err)
	b, err := EncodeRow([]Column{{Name: "s", Kind: KindString}}, []any{"2020-06-19T00:00:00+08:00"})
	require.NoError(t, err)
	require.NotEqual(t, a, b)
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

func TestDigestRowsStreamMatchesBatch(t *testing.T) {
	columns := []Column{{Name: "id", Kind: KindInteger}, {Name: "name", Kind: KindString, TrimChar: true}}
	rows := [][]any{{int64(1), "a  "}, {int64(2), "中文"}, {nil, ""}}
	batch, err := DigestRows(columns, rows)
	require.NoError(t, err)
	i := 0
	stream, err := DigestRowsStream(columns, func() ([]any, error) {
		if i == len(rows) {
			return nil, io.EOF
		}
		row := rows[i]
		i++
		return row, nil
	})
	require.NoError(t, err)
	require.Equal(t, batch, stream)
}

func TestDigestRowsMultisetIsOrderIndependentAndDuplicateSensitive(t *testing.T) {
	columns := []Column{{Name: "id", Kind: KindInteger}, {Name: "text", Kind: KindString}, {Name: "binary", Kind: KindBinary}, {Name: "price", Kind: KindDecimal, Scale: 2}, {Name: "at", Kind: KindTimestamp}, {Name: "lob", Kind: KindLOB, MaxLOBBytes: 16}}
	rows := [][]any{{int64(1), "中文", []byte{0, 1}, "1.20", "2026-09-04 12:34:56", []byte("x")}, {int64(2), nil, []byte{2}, "2.00", "2026-09-04 12:34:57", []byte("y")}, {int64(1), "中文", []byte{0, 1}, "1.20", "2026-09-04 12:34:56", []byte("x")}}
	digest := func(values [][]any) ([32]byte, int64, error) {
		i := 0
		return DigestRowsMultisetStream(columns, func() ([]any, error) {
			if i == len(values) {
				return nil, io.EOF
			}
			v := values[i]
			i++
			return v, nil
		})
	}
	first, count, err := digest(rows)
	require.NoError(t, err)
	require.EqualValues(t, 3, count)
	reordered, _, err := digest([][]any{rows[2], rows[0], rows[1]})
	require.NoError(t, err)
	require.Equal(t, first, reordered)
	withoutDuplicate, _, err := digest(rows[:2])
	require.NoError(t, err)
	require.NotEqual(t, first, withoutDuplicate)
	changed, _, err := digest([][]any{rows[0], rows[1], {int64(3), "", []byte{3}, "3.00", "2026-09-04 12:34:58", []byte("z")}})
	require.NoError(t, err)
	require.NotEqual(t, first, changed)
	empty, emptyCount, err := digest(nil)
	require.NoError(t, err)
	require.Zero(t, emptyCount)
	require.NotEqual(t, first, empty)
}
