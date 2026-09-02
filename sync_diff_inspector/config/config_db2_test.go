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
