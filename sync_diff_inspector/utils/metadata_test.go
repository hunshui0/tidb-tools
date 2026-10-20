package utils

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	perrors "github.com/pingcap/errors"
	"github.com/stretchr/testify/require"
)

func expectMetadataDDL(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SHOW CREATE TABLE").WillReturnRows(sqlmock.NewRows([]string{"Table", "Create Table"}).
		AddRow("t", "CREATE TABLE t (id INT PRIMARY KEY)"))
}

func expectMetadataMode(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SHOW VARIABLES LIKE 'sql_mode'").WillReturnRows(sqlmock.NewRows([]string{"Variable_name", "Value"}).
		AddRow("sql_mode", "ANSI_QUOTES"))
}

func TestMetadataRetryRecoversFromConnectionLossDuringSQLMode(t *testing.T) {
	for _, rowError := range []bool{false, true} {
		t.Run(fmt.Sprint("row-error=", rowError), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			expectMetadataDDL(mock)
			if rowError {
				mock.ExpectQuery("SHOW VARIABLES LIKE 'sql_mode'").WillReturnRows(
					sqlmock.NewRows([]string{"Variable_name", "Value"}).AddRow("sql_mode", "ANSI_QUOTES").RowError(0, io.ErrUnexpectedEOF))
			} else {
				mock.ExpectQuery("SHOW VARIABLES LIKE 'sql_mode'").WillReturnError(mysql.ErrInvalidConn)
			}
			// Retry the whole read-only operation; no partial TableInfo is kept.
			expectMetadataDDL(mock)
			expectMetadataMode(mock)
			info, err := getTableInfoWithRetry(context.Background(), db, "s", "t", nil, 0)
			require.NoError(t, err)
			require.Equal(t, "t", info.Name.O)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestMetadataPersistentConnectionFailureIsBounded(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	for i := 0; i < 3; i++ {
		mock.ExpectQuery("SHOW CREATE TABLE").WillReturnError(mysql.ErrInvalidConn)
	}
	info, err := getTableInfoWithRetry(context.Background(), db, "s", "t", nil, 0)
	require.Nil(t, info)
	require.ErrorContains(t, err, "failed after 3 attempt(s)")
	require.ErrorIs(t, err, mysql.ErrInvalidConn)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMetadataPermissionAndSQLFailuresAreNotRetried(t *testing.T) {
	for _, number := range []uint16{1045, 1064, 1142, 1146} {
		t.Run(fmt.Sprint(number), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			original := &mysql.MySQLError{Number: number, Message: "terminal failure"}
			mock.ExpectQuery("SHOW CREATE TABLE").WillReturnError(original)
			_, err = getTableInfoWithRetry(context.Background(), db, "s", "t", nil, 0)
			require.ErrorContains(t, err, "failed after 1 attempt(s)")
			require.ErrorIs(t, err, original)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestMetadataRetryHonorsCancellation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery("SHOW CREATE TABLE").WillReturnError(mysql.ErrInvalidConn)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = getTableInfoWithRetry(ctx, db, "s", "t", nil, time.Hour)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMetadataConnectionErrorsPreserveWrappedCauses(t *testing.T) {
	for _, err := range []error{mysql.ErrInvalidConn, driver.ErrBadConn, io.EOF, io.ErrUnexpectedEOF, net.ErrClosed,
		&net.OpError{Op: "read", Net: "tcp", Err: errors.New("reset")}} {
		require.True(t, isMetadataConnectionError(perrors.Trace(fmt.Errorf("metadata: %w", err))))
	}
	require.False(t, isMetadataConnectionError(errors.New("invalid SQL syntax")))
}
