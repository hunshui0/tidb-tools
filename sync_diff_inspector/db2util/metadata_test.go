package db2util

import (
	"testing"

	"github.com/pingcap/tidb/pkg/parser/mysql"
	"github.com/stretchr/testify/require"
)

func TestMapType(t *testing.T) {
	cases := map[string]byte{
		"SMALLINT": mysql.TypeShort, "INTEGER": mysql.TypeLong, "BIGINT": mysql.TypeLonglong,
		"DECIMAL": mysql.TypeNewDecimal, "NUMERIC": mysql.TypeNewDecimal, "REAL": mysql.TypeFloat,
		"DOUBLE": mysql.TypeDouble, "CHAR": mysql.TypeString, "VARCHAR": mysql.TypeVarString,
		"GRAPHIC": mysql.TypeString, "VARGRAPHIC": mysql.TypeVarString, "DATE": mysql.TypeDate,
		"TIME": mysql.TypeDuration, "TIMESTAMP": mysql.TypeDatetime, "BOOLEAN": mysql.TypeTiny,
		"BINARY": mysql.TypeBlob, "VARBINARY": mysql.TypeBlob, "BLOB": mysql.TypeLongBlob,
		"CLOB": mysql.TypeLongBlob, "DBCLOB": mysql.TypeLongBlob,
	}
	for name, expected := range cases {
		ft, err := MapType(name, 20, 6)
		require.NoError(t, err, name)
		require.Equal(t, expected, ft.GetType(), name)
	}
	for _, name := range []string{"DECFLOAT", "XML", "ROWID", "TIMESTAMP WITH TIME ZONE"} {
		_, err := MapType(name, 0, 0)
		require.ErrorContains(t, err, "not supported by V1")
	}
}

func TestCatalogQueriesAreParameterized(t *testing.T) {
	for name, query := range CatalogQueries() {
		require.Contains(t, query, "?")
		require.NotContains(t, query, "%s", name)
	}
}
