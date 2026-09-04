// Package db2util contains Db2 LUW-specific connection and SQL support.
package db2util

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pingcap/errors"
	"github.com/pingcap/tidb-tools/sync_diff_inspector/config"
)

const driverName = "go_ibm_db"

var reservedDSNKeys = map[string]struct{}{
	"DATABASE": {}, "HOSTNAME": {}, "PORT": {}, "PWD": {}, "UID": {},
}

// BuildDSN returns a Db2 CLI connection string. It deliberately does not log
// the result because it contains the password.
func BuildDSN(cfg *config.DataSource) (string, error) {
	if cfg == nil {
		return "", errors.New("nil db2 data source")
	}
	if err := cfg.ValidateDatabaseType(); err != nil {
		return "", errors.Trace(err)
	}
	if cfg.DatabaseType() != config.DatabaseTypeDB2 {
		return "", errors.Errorf("expected db2 data source, got %s", cfg.DatabaseType())
	}
	if cfg.Host == "" || cfg.Port <= 0 || cfg.User == "" {
		return "", errors.New("db2 data source requires host, port, database, and user")
	}

	parts := []string{
		"HOSTNAME=" + cfg.Host,
		"PORT=" + strconv.Itoa(cfg.Port),
		"DATABASE=" + cfg.Database,
		"UID=" + cfg.User,
		"PWD=" + cfg.Password.Plain(),
	}
	if cfg.Schema != "" {
		parts = append(parts, "CURRENTSCHEMA="+NormalizeIdentifier(cfg.Schema))
	}
	params := make(map[string]string, len(cfg.ConnectionParams))
	keys := make([]string, 0, len(cfg.ConnectionParams))
	for key, value := range cfg.ConnectionParams {
		key = strings.ToUpper(strings.TrimSpace(key))
		if _, reserved := reservedDSNKeys[key]; reserved {
			return "", errors.Errorf("db2 connection-params must not override %s", key)
		}
		if _, exists := params[key]; exists {
			return "", errors.Errorf("duplicate db2 connection parameter %s", key)
		}
		params[key] = value
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := params[key]
		if strings.ContainsAny(value, ";\x00") {
			return "", errors.Errorf("db2 connection parameter %s contains an invalid character", key)
		}
		parts = append(parts, key+"="+value)
	}
	return strings.Join(parts, ";") + ";", nil
}

// Connect opens and verifies a Db2 connection. Builds without the db2cli tag
// return an actionable error before attempting to expose a driver failure.
func Connect(ctx context.Context, cfg *config.DataSource, maxOpen int) (*sql.DB, error) {
	if !driverAvailable() {
		return nil, errors.New("db2 support requires a build with -tags db2cli and the Db2 CLI client; set IBM_DB_HOME, CGO_CFLAGS, CGO_LDFLAGS, and LD_LIBRARY_PATH")
	}
	dsn, err := BuildDSN(cfg)
	if err != nil {
		return nil, errors.Trace(err)
	}
	clientCodePage, err := validateClientCodePage(cfg.ClientCodePage)
	if err != nil {
		return nil, err
	}
	if clientCodePage != "" {
		// Db2 CLI reads this process-level setting when it establishes the
		// connection. It must be configured before sql.Open/Ping.
		if err := os.Setenv("DB2CODEPAGE", clientCodePage); err != nil {
			return nil, errors.Annotate(err, "set db2 client code page")
		}
	}
	if maxOpen < 1 {
		maxOpen = 1
	}
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, ClassifyError(err)
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxOpen)
	db.SetConnMaxLifetime(30 * time.Minute)

	timeout := 15 * time.Second
	if cfg.ConnectTimeout != "" {
		timeout, err = time.ParseDuration(cfg.ConnectTimeout)
		if err != nil || timeout <= 0 {
			db.Close()
			return nil, errors.Errorf("invalid db2 connect-timeout %q", cfg.ConnectTimeout)
		}
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, ClassifyError(err)
	}
	if _, err := db.ExecContext(pingCtx, "SET CURRENT ISOLATION UR"); err != nil {
		db.Close()
		return nil, errors.Annotate(ClassifyError(err), "initialize db2 read-only isolation")
	}
	return db, nil
}

func validateClientCodePage(raw string) (string, error) {
	codePage := strings.TrimSpace(raw)
	if codePage == "" {
		return "", nil
	}
	value, err := strconv.ParseUint(codePage, 10, 32)
	if err != nil || value == 0 {
		return "", errors.Errorf("invalid db2 client-code-page %q; use a positive numeric Db2 code page such as 1208", raw)
	}
	return strconv.FormatUint(value, 10), nil
}

// NormalizeIdentifier applies Db2's unquoted-identifier rule. Quoted names
// retain their exact case and embedded quotes are validated by QuoteIdentifier.
func NormalizeIdentifier(name string) string {
	if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
		return name[1 : len(name)-1]
	}
	return strings.ToUpper(name)
}

func QuoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(NormalizeIdentifier(name), `"`, `""`) + `"`
}

func QualifiedTable(schema, table string) string {
	return QuoteIdentifier(schema) + "." + QuoteIdentifier(table)
}

// ClassifyError adds a stable category without assuming a specific driver error
// type. Db2 SQLSTATE is preserved in the original error string.
func ClassifyError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToUpper(err.Error())
	category := "query failed"
	switch {
	case strings.Contains(message, "SQL30082"), strings.Contains(message, "AUTHENTICATION"):
		category = "authentication failed"
	case strings.Contains(message, "SQL0204"), strings.Contains(message, "SQLSTATE=42704"), strings.Contains(message, "SQLSTATE 42704"):
		category = "object not found"
	case strings.Contains(message, "SQL0551"), strings.Contains(message, "SQL0552"), strings.Contains(message, "SQLSTATE=42501"):
		category = "permission denied"
	case strings.Contains(message, "CANCEL"), strings.Contains(message, "SQL0952"):
		category = "query cancelled"
	case strings.Contains(message, "SQL30081"), strings.Contains(message, "ECONN"), strings.Contains(message, "NETWORK"):
		category = "network failed"
	}
	return errors.Annotatef(err, "db2 %s", category)
}

func Address(cfg *config.DataSource) string {
	return net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
}

func RedactedDSN(cfg *config.DataSource) string {
	return fmt.Sprintf("HOSTNAME=%s;PORT=%d;DATABASE=%s;UID=%s;PWD=******;", cfg.Host, cfg.Port, cfg.Database, cfg.User)
}
