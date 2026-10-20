package dbutil

import (
	"context"
	"io"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestGetSessionVariablePropagatesRowError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery("SHOW VARIABLES LIKE 'sql_mode'").WillReturnRows(
		sqlmock.NewRows([]string{"Variable_name", "Value"}).AddRow("sql_mode", "ANSI_QUOTES").RowError(0, io.ErrUnexpectedEOF))
	mode, err := GetSessionVariable(context.Background(), db, "sql_mode")
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Empty(t, mode)
	require.NoError(t, mock.ExpectationsWereMet())
}
