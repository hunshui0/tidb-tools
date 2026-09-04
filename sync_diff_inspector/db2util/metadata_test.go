package db2util

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
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

func TestReadTableInfoMissingObjectIsTyped(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(objectsSQL)).WithArgs("APP", "MISSING").WillReturnRows(sqlmock.NewRows([]string{"TYPE"}))
	mock.ExpectQuery(regexp.QuoteMeta(schemaSQL)).WithArgs("APP").WillReturnRows(sqlmock.NewRows([]string{"SCHEMANAME"}).AddRow("APP"))
	mock.ExpectQuery(regexp.QuoteMeta(objectCaseSQL)).WithArgs("APP", "MISSING").WillReturnRows(sqlmock.NewRows([]string{"TABNAME"}))

	_, err = ReadTableInfo(context.Background(), db, "app", "missing")
	var missing *TableNotFoundError
	require.ErrorAs(t, err, &missing)
	require.Equal(t, "APP", missing.Schema)
	require.Equal(t, "MISSING", missing.Table)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReadTableInfoMissingSchemaIsTypedAndNotTableMissing(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(objectsSQL)).WithArgs("NO_SCHEMA", "T").WillReturnRows(sqlmock.NewRows([]string{"TYPE"}))
	mock.ExpectQuery(regexp.QuoteMeta(schemaSQL)).WithArgs("NO_SCHEMA").WillReturnRows(sqlmock.NewRows([]string{"SCHEMANAME"}))
	mock.ExpectQuery(regexp.QuoteMeta(schemaCaseSQL)).WithArgs("NO_SCHEMA").WillReturnRows(sqlmock.NewRows([]string{"SCHEMANAME"}))

	_, err = ReadTableInfo(context.Background(), db, "NO_SCHEMA", "T")
	var missing *TableNotFoundError
	require.Error(t, err)
	require.False(t, errors.As(err, &missing))
	var schemaMissing *SchemaNotFoundError
	require.ErrorAs(t, err, &schemaMissing)
	require.Equal(t, "NO_SCHEMA", schemaMissing.Schema)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReadTableInfoRejectsSchemaCaseMismatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(objectsSQL)).WithArgs("MIXED", "T").WillReturnRows(sqlmock.NewRows([]string{"TYPE"}))
	mock.ExpectQuery(regexp.QuoteMeta(schemaSQL)).WithArgs("MIXED").WillReturnRows(sqlmock.NewRows([]string{"SCHEMANAME"}))
	mock.ExpectQuery(regexp.QuoteMeta(schemaCaseSQL)).WithArgs("MIXED").WillReturnRows(sqlmock.NewRows([]string{"SCHEMANAME"}).AddRow("MiXeD"))

	_, err = ReadTableInfo(context.Background(), db, "MIXED", "T")
	var caseErr *IdentifierCaseError
	require.ErrorAs(t, err, &caseErr)
	require.Equal(t, "schema", caseErr.Kind)
	require.Equal(t, []string{"MiXeD"}, caseErr.Candidates)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReadTableInfoRejectsTableCaseMismatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(objectsSQL)).WithArgs("APP", "MIXED").WillReturnRows(sqlmock.NewRows([]string{"TYPE"}))
	mock.ExpectQuery(regexp.QuoteMeta(schemaSQL)).WithArgs("APP").WillReturnRows(sqlmock.NewRows([]string{"SCHEMANAME"}).AddRow("APP"))
	mock.ExpectQuery(regexp.QuoteMeta(objectCaseSQL)).WithArgs("APP", "MIXED").WillReturnRows(sqlmock.NewRows([]string{"TABNAME"}).AddRow("MiXeD"))

	_, err = ReadTableInfo(context.Background(), db, "APP", "MIXED")
	var caseErr *IdentifierCaseError
	require.ErrorAs(t, err, &caseErr)
	require.Equal(t, "table", caseErr.Kind)
	require.Equal(t, "APP.MIXED", caseErr.Identifier)
	require.Equal(t, []string{"MiXeD"}, caseErr.Candidates)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReadTableInfoFollowupCatalogFailureIsNotMissing(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	catalogErr := errors.New("permission denied reading schema catalog")
	mock.ExpectQuery(regexp.QuoteMeta(objectsSQL)).WithArgs("APP", "T").WillReturnRows(sqlmock.NewRows([]string{"TYPE"}))
	mock.ExpectQuery(regexp.QuoteMeta(schemaSQL)).WithArgs("APP").WillReturnError(catalogErr)

	_, err = ReadTableInfo(context.Background(), db, "APP", "T")
	var missing *TableNotFoundError
	require.Error(t, err)
	require.False(t, errors.As(err, &missing))
	var queryErr *CatalogError
	require.ErrorAs(t, err, &queryErr)
	require.ErrorContains(t, err, catalogErr.Error())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReadTableInfoCatalogFailureIsNotMissing(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	catalogErr := errors.New("permission denied by catalog")
	mock.ExpectQuery(regexp.QuoteMeta(objectsSQL)).WithArgs("APP", "T").WillReturnError(catalogErr)

	_, err = ReadTableInfo(context.Background(), db, "APP", "T")
	var missing *TableNotFoundError
	require.Error(t, err)
	require.ErrorContains(t, err, catalogErr.Error())
	require.False(t, errors.As(err, &missing))
	var queryErr *CatalogError
	require.ErrorAs(t, err, &queryErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReadTableInfoPreservesQuotedIdentifierCase(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(objectsSQL)).WithArgs("MiXeD", "TaBle").WillReturnRows(sqlmock.NewRows([]string{"TYPE"}).AddRow("V"))
	mock.ExpectQuery(regexp.QuoteMeta(columnsSQL)).WithArgs("MiXeD", "TaBle").WillReturnRows(
		sqlmock.NewRows([]string{"COLNAME", "COLNO", "TYPENAME", "LENGTH", "SCALE", "NULLS"}).AddRow("Value", 0, "VARCHAR", 20, 0, "Y"),
	)
	mock.ExpectQuery(regexp.QuoteMeta(constraintsSQL)).WithArgs("MiXeD", "TaBle").WillReturnRows(sqlmock.NewRows([]string{"CONSTNAME", "TYPE", "COLNAME", "COLSEQ"}))
	mock.ExpectQuery(regexp.QuoteMeta(indexesSQL)).WithArgs("MiXeD", "TaBle").WillReturnRows(sqlmock.NewRows([]string{"INDNAME", "UNIQUERULE", "COLNAME", "COLSEQ"}))

	info, err := ReadTableInfo(context.Background(), db, `"MiXeD"`, `"TaBle"`)
	require.NoError(t, err)
	require.Equal(t, "TaBle", info.Name.O)
	require.Equal(t, "Value", info.Columns[0].Name.O)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReadTableInfoRejectsSpecialObjectType(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	for _, objectType := range []string{"A", "N"} {
		mock.ExpectQuery(regexp.QuoteMeta(objectsSQL)).WithArgs("APP", objectType).WillReturnRows(sqlmock.NewRows([]string{"TYPE"}).AddRow(objectType))
		_, err = ReadTableInfo(context.Background(), db, "APP", objectType)
		var unsupported *UnsupportedObjectError
		require.ErrorAs(t, err, &unsupported)
		require.Contains(t, err.Error(), objectTypeName(objectType))
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReadTableInfoExistingObjectWithoutReadableColumnsIsNotMissing(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(objectsSQL)).WithArgs("APP", "EMPTY").WillReturnRows(sqlmock.NewRows([]string{"TYPE"}).AddRow("T"))
	mock.ExpectQuery(regexp.QuoteMeta(columnsSQL)).WithArgs("APP", "EMPTY").WillReturnRows(sqlmock.NewRows([]string{"COLNAME", "COLNO", "TYPENAME", "LENGTH", "SCALE", "NULLS"}))

	_, err = ReadTableInfo(context.Background(), db, "APP", "EMPTY")
	var missing *TableNotFoundError
	require.Error(t, err)
	require.ErrorContains(t, err, "no readable columns")
	require.False(t, errors.As(err, &missing))
	require.NoError(t, mock.ExpectationsWereMet())
}
