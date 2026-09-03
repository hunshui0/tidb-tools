package db2util

import (
	"fmt"
	"strings"
)

// Bound preserves range semantics independently of database SQL syntax.
// Nil represents SQL NULL, not an absent bound.
type Bound struct {
	Value any  `json:"value"`
	Set   bool `json:"set"`
}

// Range is the V1 checkpoint-safe range representation. A range is ordered
// lexicographically by Columns; bounds use strict inequalities so adjacent
// chunks never overlap.
type Range struct {
	Columns        []string `json:"columns"`
	Lower          []Bound  `json:"lower,omitempty"`
	Upper          []Bound  `json:"upper,omitempty"`
	LowerInclusive bool     `json:"lower-inclusive"`
	UpperInclusive bool     `json:"upper-inclusive"`
}

func (r Range) Validate() error {
	if len(r.Columns) == 0 {
		return fmt.Errorf("range requires at least one ordering column")
	}
	if len(r.Lower) > 0 && len(r.Lower) != len(r.Columns) {
		return fmt.Errorf("lower bound has %d values for %d columns", len(r.Lower), len(r.Columns))
	}
	if len(r.Upper) > 0 && len(r.Upper) != len(r.Columns) {
		return fmt.Errorf("upper bound has %d values for %d columns", len(r.Upper), len(r.Columns))
	}
	return nil
}

// Dialect renders the same structured range for each supported SQL family.
type Dialect interface {
	QuoteIdentifier(string) string
	Placeholder(int) string
	RenderRange(Range) (string, []any, error)
	RenderSelect(schema, table string, columns []string, r Range, limit int) (string, []any, error)
}

type DB2Dialect struct{}

func (DB2Dialect) QuoteIdentifier(name string) string { return QuoteIdentifier(name) }
func (DB2Dialect) Placeholder(_ int) string           { return "?" }

func (d DB2Dialect) RenderRange(r Range) (string, []any, error) {
	return renderRange(d, r)
}

func (d DB2Dialect) RenderSelect(schema, table string, columns []string, r Range, limit int) (string, []any, error) {
	where, args, err := d.RenderRange(r)
	if err != nil {
		return "", nil, err
	}
	selectColumns := quoteColumns(d, columns)
	query := "SELECT " + strings.Join(selectColumns, ", ") + " FROM " + QualifiedTable(schema, table)
	if where != "" {
		query += " WHERE " + where
	}
	query += " ORDER BY " + strings.Join(quoteColumns(d, r.Columns), ", ")
	if limit > 0 {
		query += fmt.Sprintf(" FETCH FIRST %d ROWS ONLY", limit)
	}
	return query, args, nil
}

// MySQLDialect is intentionally small and emits existing MySQL/TiDB syntax.
// It is used by CanonicalV1 paths so a shared range never becomes DB2 SQL.
type MySQLDialect struct{}

func (MySQLDialect) QuoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
func (MySQLDialect) Placeholder(_ int) string { return "?" }
func (d MySQLDialect) RenderRange(r Range) (string, []any, error) {
	return renderRange(d, r)
}
func (d MySQLDialect) RenderSelect(schema, table string, columns []string, r Range, limit int) (string, []any, error) {
	where, args, err := d.RenderRange(r)
	if err != nil {
		return "", nil, err
	}
	query := "SELECT " + strings.Join(quoteColumns(d, columns), ", ") + " FROM " + d.QuoteIdentifier(schema) + "." + d.QuoteIdentifier(table)
	if where != "" {
		query += " WHERE " + where
	}
	query += " ORDER BY " + strings.Join(quoteColumns(d, r.Columns), ", ")
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	return query, args, nil
}

func renderRange(d Dialect, r Range) (string, []any, error) {
	if err := r.Validate(); err != nil {
		return "", nil, err
	}
	clauses := make([]string, 0, 2)
	args := make([]any, 0, 2*len(r.Columns)*len(r.Columns))
	if len(r.Lower) > 0 {
		clause, values := tuplePredicate(d, r.Columns, r.Lower, r.LowerInclusive, true)
		clauses, args = append(clauses, clause), append(args, values...)
	}
	if len(r.Upper) > 0 {
		clause, values := tuplePredicate(d, r.Columns, r.Upper, r.UpperInclusive, false)
		clauses, args = append(clauses, clause), append(args, values...)
	}
	return strings.Join(clauses, " AND "), args, nil
}

func tuplePredicate(d Dialect, columns []string, bounds []Bound, inclusive, lower bool) (string, []any) {
	parts := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns)*(len(columns)+1)/2)
	for i := range columns {
		operator := "<"
		if lower {
			operator = ">"
		}
		if inclusive && i == len(columns)-1 {
			operator += "="
		}
		prefix := make([]string, 0, i+1)
		valid := true
		for j := 0; j < i; j++ {
			if !bounds[j].Set {
				valid = false
				break
			}
			if bounds[j].Value == nil {
				prefix = append(prefix, d.QuoteIdentifier(columns[j])+" IS NULL")
			} else {
				prefix = append(prefix, d.QuoteIdentifier(columns[j])+" = "+d.Placeholder(len(args)+1))
				args = append(args, bounds[j].Value)
			}
		}
		if !valid || !bounds[i].Set {
			continue
		}
		if bounds[i].Value == nil {
			// NULL ordering differs by database and collation. V1 admits a NULL
			// endpoint only as the explicit first lower boundary.
			if !lower || i != 0 {
				continue
			}
			prefix = append(prefix, d.QuoteIdentifier(columns[i])+" IS NOT NULL")
		} else {
			prefix = append(prefix, d.QuoteIdentifier(columns[i])+" "+operator+" "+d.Placeholder(len(args)+1))
			args = append(args, bounds[i].Value)
		}
		parts = append(parts, "("+strings.Join(prefix, " AND ")+")")
	}
	if len(parts) == 0 {
		return "", nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

func quoteColumns(d Dialect, columns []string) []string {
	result := make([]string, 0, len(columns))
	for _, column := range columns {
		result = append(result, d.QuoteIdentifier(column))
	}
	return result
}
