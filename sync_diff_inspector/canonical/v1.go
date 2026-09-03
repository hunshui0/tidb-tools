// Package canonical defines the database-independent V1 row digest protocol.
package canonical

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

const Version = "CanonicalV1"

type Kind byte

const (
	KindString Kind = iota + 1
	KindBinary
	KindInteger
	KindDecimal
	KindFloat
	KindDate
	KindTime
	KindTimestamp
	KindBoolean
	KindLOB
)

type Column struct {
	Name        string
	Kind        Kind
	Scale       int
	TrimChar    bool
	MaxLOBBytes int64
}

// EncodeRow is length-delimited and includes each value's logical type and
// NULL marker, making NULL, empty string, and the text "NULL" distinct.
func EncodeRow(columns []Column, values []any) ([]byte, error) {
	if len(columns) != len(values) {
		return nil, fmt.Errorf("CanonicalV1 has %d columns and %d values", len(columns), len(values))
	}
	var out bytes.Buffer
	out.WriteString(Version)
	out.WriteByte(0)
	for i, column := range columns {
		value, isNull, err := normalize(column, values[i])
		if err != nil {
			return nil, fmt.Errorf("CanonicalV1 column %s: %w", column.Name, err)
		}
		out.WriteByte(byte(column.Kind))
		if isNull {
			out.WriteByte(1)
			writeLength(&out, 0)
			continue
		}
		out.WriteByte(0)
		writeLength(&out, uint64(len(value)))
		out.Write(value)
	}
	return out.Bytes(), nil
}

func DigestRows(columns []Column, rows [][]any) ([32]byte, error) {
	index := 0
	return DigestRowsStream(columns, func() ([]any, error) {
		if index >= len(rows) {
			return nil, io.EOF
		}
		row := rows[index]
		index++
		return row, nil
	})
}

// DigestRowsStream computes the same digest as DigestRows while retaining only
// the current row. The callback must return io.EOF when no rows remain.
func DigestRowsStream(columns []Column, next func() ([]any, error)) ([32]byte, error) {
	h := sha256.New()
	h.Write([]byte(Version))
	h.Write([]byte{0})
	for {
		row, err := next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return [32]byte{}, err
		}
		encoded, err := EncodeRow(columns, row)
		if err != nil {
			return [32]byte{}, err
		}
		h.Write(encoded)
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

func writeLength(out *bytes.Buffer, length uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], length)
	out.Write(b[:])
}

func normalize(column Column, value any) ([]byte, bool, error) {
	if value == nil {
		return nil, true, nil
	}
	switch column.Kind {
	case KindString:
		text, err := textValue(value)
		if err != nil {
			return nil, false, err
		}
		if column.TrimChar {
			text = strings.TrimRight(text, " ")
		}
		return []byte(text), false, nil
	case KindBinary:
		return binaryValue(value)
	case KindInteger:
		return integerValue(value)
	case KindDecimal:
		return decimalValue(value, column.Scale)
	case KindFloat:
		return floatValue(value)
	case KindDate:
		return temporalValue(value, false)
	case KindTime:
		return timeOnlyValue(value)
	case KindTimestamp:
		return temporalValue(value, true)
	case KindBoolean:
		return boolValue(value)
	case KindLOB:
		return lobValue(value, column.MaxLOBBytes)
	default:
		return nil, false, fmt.Errorf("unsupported logical kind %d", column.Kind)
	}
}

func textValue(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	default:
		return "", fmt.Errorf("expected string, got %T", value)
	}
}

func binaryValue(value any) ([]byte, bool, error) {
	switch v := value.(type) {
	case []byte:
		return append([]byte(nil), v...), false, nil
	case string:
		return []byte(v), false, nil
	default:
		return nil, false, fmt.Errorf("expected bytes, got %T", value)
	}
}

func integerValue(value any) ([]byte, bool, error) {
	text := fmt.Sprint(value)
	if bytesValue, ok := value.([]byte); ok {
		text = string(bytesValue)
	}
	i, ok := new(big.Int).SetString(strings.TrimSpace(text), 10)
	if !ok {
		return nil, false, fmt.Errorf("invalid integer %q", text)
	}
	return []byte(i.String()), false, nil
}

func decimalValue(value any, scale int) ([]byte, bool, error) {
	text := fmt.Sprint(value)
	if bytesValue, ok := value.([]byte); ok {
		text = string(bytesValue)
	}
	r, ok := new(big.Rat).SetString(strings.TrimSpace(text))
	if !ok || scale < 0 {
		return nil, false, fmt.Errorf("invalid decimal %q", text)
	}
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	unscaled := new(big.Rat).Mul(r, new(big.Rat).SetInt(factor))
	if !unscaled.IsInt() {
		return nil, false, fmt.Errorf("decimal %q exceeds scale %d", text, scale)
	}
	return []byte(unscaled.Num().String() + ":" + strconv.Itoa(scale)), false, nil
}

func floatValue(value any) ([]byte, bool, error) {
	var f float64
	switch v := value.(type) {
	case float32:
		f = float64(v)
	case float64:
		f = v
	case []byte:
		parsed, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return nil, false, err
		}
		f = parsed
	case string:
		parsed, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, false, err
		}
		f = parsed
	default:
		return nil, false, fmt.Errorf("expected float, got %T", value)
	}
	if math.IsNaN(f) {
		return []byte("NaN"), false, nil
	}
	if math.IsInf(f, 1) {
		return []byte("+Inf"), false, nil
	}
	if math.IsInf(f, -1) {
		return []byte("-Inf"), false, nil
	}
	if f == 0 {
		f = 0 // CanonicalV1 treats +0 and -0 as equal.
	}
	return []byte(strconv.FormatFloat(f, 'g', -1, 64)), false, nil
}

func temporalValue(value any, timestamp bool) ([]byte, bool, error) {
	if t, ok := value.(time.Time); ok {
		if timestamp {
			return []byte(t.UTC().Format(time.RFC3339Nano)), false, nil
		}
		return []byte(t.Format("2006-01-02")), false, nil
	}
	text, err := textValue(value)
	if err != nil {
		return nil, false, err
	}
	if !timestamp {
		if _, err := time.Parse("2006-01-02", text); err != nil {
			return nil, false, err
		}
		return []byte(text), false, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999"} {
		if t, err := time.Parse(layout, text); err == nil {
			return []byte(t.UTC().Format(time.RFC3339Nano)), false, nil
		}
	}
	return nil, false, fmt.Errorf("invalid timestamp %q", text)
}

func timeOnlyValue(value any) ([]byte, bool, error) {
	text, err := textValue(value)
	if err != nil {
		return nil, false, err
	}
	for _, layout := range []string{"15:04:05.999999999", "15:04:05"} {
		if t, err := time.Parse(layout, text); err == nil {
			return []byte(t.Format("15:04:05.999999999")), false, nil
		}
	}
	return nil, false, fmt.Errorf("invalid time %q", text)
}

func boolValue(value any) ([]byte, bool, error) {
	switch v := value.(type) {
	case bool:
		if v {
			return []byte{1}, false, nil
		}
		return []byte{0}, false, nil
	case int64:
		if v == 0 || v == 1 {
			return []byte{byte(v)}, false, nil
		}
	case []byte:
		return boolValue(string(v))
	case string:
		switch strings.ToLower(v) {
		case "1", "true":
			return []byte{1}, false, nil
		case "0", "false":
			return []byte{0}, false, nil
		}
	}
	return nil, false, fmt.Errorf("invalid boolean %v", value)
}

func lobValue(value any, max int64) ([]byte, bool, error) {
	if max <= 0 {
		max = 16 << 20
	}
	if reader, ok := value.(io.Reader); ok {
		data, err := io.ReadAll(io.LimitReader(reader, max+1))
		if err != nil {
			return nil, false, err
		}
		if int64(len(data)) > max {
			return nil, false, fmt.Errorf("LOB exceeds %d byte limit", max)
		}
		return data, false, nil
	}
	data, _, err := binaryValue(value)
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > max {
		return nil, false, fmt.Errorf("LOB exceeds %d byte limit", max)
	}
	return data, false, nil
}
