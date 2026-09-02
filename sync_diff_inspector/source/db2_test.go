package source

import (
	"context"
	"testing"

	"github.com/pingcap/tidb-tools/sync_diff_inspector/config"
	"github.com/stretchr/testify/require"
)

func TestDB2ConnectionBranchExplainsNativeRequirement(t *testing.T) {
	_, err := connectDataSource(context.Background(), &config.DataSource{
		Type: config.DatabaseTypeDB2, Host: "db2.example", Port: 50000, Database: "SAMPLE", User: "diff",
	}, 1)
	require.ErrorContains(t, err, "build with -tags db2cli")
}
