package utils

import (
	"context"
	"database/sql"
	"database/sql/driver"
	stderrors "errors"
	"io"
	"net"
	"time"

	"github.com/coreos/go-semver/semver"
	"github.com/go-sql-driver/mysql"
	"github.com/pingcap/errors"
	"github.com/pingcap/log"
	"github.com/pingcap/tidb-tools/pkg/dbutil"
	"github.com/pingcap/tidb/pkg/meta/model"
	"go.uber.org/zap"
)

const metadataAttempts = 3

// GetTableInfoWithRetry retries only read-only MySQL/TiDB metadata operations.
// Use the existing pool so replacement connections retain the configured DSN,
// session parameters and snapshot. Do not reuse this policy for row comparison
// or SQL writes, where replaying work would require separate guarantees.
func GetTableInfoWithRetry(ctx context.Context, db *sql.DB, schema, table string, version *semver.Version) (*model.TableInfo, error) {
	return getTableInfoWithRetry(ctx, db, schema, table, version, 500*time.Millisecond)
}

func getTableInfoWithRetry(ctx context.Context, db *sql.DB, schema, table string, version *semver.Version, delay time.Duration) (*model.TableInfo, error) {
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := dbutil.GetTableInfoWithVersion(ctx, db, schema, table, version)
		if err == nil {
			if attempt > 1 {
				log.Info("读取表元数据重试成功", zap.String("table", dbutil.TableName(schema, table)), zap.Int("attempt", attempt))
			}
			return info, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !isMetadataConnectionError(err) || attempt >= metadataAttempts {
			return nil, errors.Annotatef(err, "read metadata for %s failed after %d attempt(s)", dbutil.TableName(schema, table), attempt)
		}
		wait := time.Duration(attempt) * delay
		log.Warn("读取表元数据时连接中断，准备重试", zap.String("table", dbutil.TableName(schema, table)),
			zap.Int("attempt", attempt), zap.Int("max-attempts", metadataAttempts), zap.Duration("retry-in", wait), zap.Error(errors.Cause(err)))
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func isMetadataConnectionError(err error) bool {
	cause := errors.Cause(err)
	for _, connectionError := range []error{mysql.ErrInvalidConn, driver.ErrBadConn, sql.ErrConnDone, io.EOF, io.ErrUnexpectedEOF, net.ErrClosed} {
		if stderrors.Is(cause, connectionError) {
			return true
		}
	}
	var networkError net.Error
	return stderrors.As(cause, &networkError)
}
