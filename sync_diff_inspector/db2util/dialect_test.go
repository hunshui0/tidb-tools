package db2util

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDialectRendersStructuredRange(t *testing.T) {
	r := Range{Columns: []string{"id", "name"}, Lower: []Bound{{Value: int64(10), Set: true}, {Value: "a", Set: true}}, Upper: []Bound{{Value: int64(20), Set: true}, {Value: "z", Set: true}}}
	db2SQL, db2Args, err := (DB2Dialect{}).RenderSelect("app", "order", []string{"id", "name"}, r, 100)
	require.NoError(t, err)
	require.Equal(t, `SELECT "ID", "NAME" FROM "APP"."ORDER" WHERE (("ID" > ?) OR ("ID" = ? AND "NAME" > ?)) AND (("ID" < ?) OR ("ID" = ? AND "NAME" < ?)) ORDER BY "ID", "NAME" FETCH FIRST 100 ROWS ONLY`, db2SQL)
	require.Equal(t, []any{int64(10), int64(10), "a", int64(20), int64(20), "z"}, db2Args)

	mySQL, _, err := (MySQLDialect{}).RenderSelect("app", "order", []string{"id", "name"}, r, 100)
	require.NoError(t, err)
	require.Contains(t, mySQL, "FROM `app`.`order`")
	require.Contains(t, mySQL, "LIMIT 100")
}

func TestRangeValidationAndNullBoundary(t *testing.T) {
	_, _, err := (DB2Dialect{}).RenderRange(Range{})
	require.ErrorContains(t, err, "at least one")
	r := Range{Columns: []string{"id"}, Lower: []Bound{{Value: nil, Set: true}}}
	where, args, err := (DB2Dialect{}).RenderRange(r)
	require.NoError(t, err)
	require.Equal(t, `(("ID" IS NOT NULL))`, where)
	require.Empty(t, args)
}
