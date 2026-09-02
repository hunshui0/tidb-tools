package db2util

import (
	"testing"

	"github.com/pingcap/tidb-tools/sync_diff_inspector/config"
	"github.com/stretchr/testify/require"
)

func TestBuildDSN(t *testing.T) {
	cfg := &config.DataSource{
		Type: config.DatabaseTypeDB2, Host: "db2.example", Port: 50001,
		Database: "sample", Schema: "app", User: "diff", Password: "secret",
		ConnectionParams: map[string]string{"Security": "SSL", "ConnectTimeout": "5"},
	}
	dsn, err := BuildDSN(cfg)
	require.NoError(t, err)
	require.Equal(t, "HOSTNAME=db2.example;PORT=50001;DATABASE=sample;UID=diff;PWD=secret;CURRENTSCHEMA=APP;CONNECTTIMEOUT=5;SECURITY=SSL;", dsn)
	require.NotContains(t, RedactedDSN(cfg), "secret")
}

func TestBuildDSNRejectsUnsafeParameters(t *testing.T) {
	cfg := &config.DataSource{Type: config.DatabaseTypeDB2, Host: "host", Port: 50000, Database: "db", User: "user", ConnectionParams: map[string]string{"PWD": "override"}}
	_, err := BuildDSN(cfg)
	require.ErrorContains(t, err, "must not override PWD")
	cfg.ConnectionParams = map[string]string{"x": "bad;value"}
	_, err = BuildDSN(cfg)
	require.ErrorContains(t, err, "invalid character")
}

func TestIdentifiersAndErrorCategories(t *testing.T) {
	require.Equal(t, "APP", NormalizeIdentifier("app"))
	require.Equal(t, "Mixed", NormalizeIdentifier(`"Mixed"`))
	require.Equal(t, `"A""B"`, QuoteIdentifier(`"A"B"`))
	require.ErrorContains(t, ClassifyError(assertError("SQL30082N")), "authentication failed")
	require.ErrorContains(t, ClassifyError(assertError("SQL0204N")), "object not found")
}

type assertError string

func (e assertError) Error() string { return string(e) }
