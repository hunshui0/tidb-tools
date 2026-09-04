package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDataSourceDatabaseType(t *testing.T) {
	legacy := &DataSource{}
	require.True(t, legacy.IsAutoDetected())
	require.Equal(t, DatabaseTypeMySQL, legacy.DatabaseType())
	require.NoError(t, legacy.ValidateDatabaseType())

	db2 := &DataSource{Type: "DB2", Database: "SAMPLE", Schema: "APP"}
	require.False(t, db2.IsAutoDetected())
	require.Equal(t, DatabaseTypeDB2, db2.DatabaseType())
	require.NoError(t, db2.ValidateDatabaseType())

	require.ErrorContains(t, (&DataSource{Type: "db2"}).ValidateDatabaseType(), "requires database")
	require.ErrorContains(t, (&DataSource{Type: "postgres"}).ValidateDatabaseType(), "unsupported data source type")
}

func TestTaskRejectsDB2Target(t *testing.T) {
	task := &TaskConfig{Source: []string{"source"}, Target: "target", OutputDir: t.TempDir()}
	err := task.Init(map[string]*DataSource{
		"source": {Type: DatabaseTypeDB2, Database: "SAMPLE"},
		"target": {Type: DatabaseTypeDB2, Database: "SAMPLE"},
	}, nil)
	require.ErrorContains(t, err, "supported only as an upstream source")
}

func TestDB2ExampleConfigReferencesTableConfig(t *testing.T) {
	cfg := NewConfig()
	require.NoError(t, cfg.configFromFile("config_db2.toml"))
	require.Contains(t, cfg.Task.Source, "db2_source")
	require.Equal(t, "tidb_target", cfg.Task.Target)
	require.Contains(t, cfg.Task.CheckTables, "app.orders")
	require.Equal(t, []string{"orders"}, cfg.Task.TableConfigs)
	tableConfig, ok := cfg.TableConfigs["orders"]
	require.True(t, ok)
	require.Equal(t, []string{"app.orders"}, tableConfig.TargetTables)
	require.Equal(t, int64(2), tableConfig.ChunkSize)
	require.Equal(t, []string{"id"}, tableConfig.Fields)
	require.Equal(t, DatabaseTypeDB2, cfg.DataSources["db2_source"].DatabaseType())
}
