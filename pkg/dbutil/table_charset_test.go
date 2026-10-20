package dbutil

import (
	"context"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/mysql"
	"github.com/pingcap/tidb/pkg/util/collate"
	"github.com/pingcap/tidb/pkg/util/mock"
	"github.com/stretchr/testify/require"
)

// Match the reported SHOW CREATE TABLE, including column-level collations and
// a table without a unique key. This must not require changing the live table.
const gb18030TableDDL = "CREATE TABLE `ABNOMALY_RECORDS` (" +
	"`CASE_NO` varchar(100) COLLATE gb18030_bin DEFAULT NULL," +
	"`CST_NO` varchar(50) COLLATE gb18030_bin DEFAULT NULL," +
	"`ACC_NAME` varchar(40) COLLATE gb18030_bin DEFAULT NULL," +
	"`UNIT_FLAG` varchar(2) COLLATE gb18030_bin DEFAULT NULL," +
	"`ID_TYPE` varchar(2) COLLATE gb18030_bin DEFAULT NULL," +
	"`ID_NO` varchar(128) COLLATE gb18030_bin DEFAULT NULL," +
	"`ACC_NAME_TRA` varchar(40) COLLATE gb18030_bin DEFAULT NULL," +
	"`CST_TYPE_TRA` varchar(20) COLLATE gb18030_bin DEFAULT NULL," +
	"`ID_TYPE_TRA` varchar(2) COLLATE gb18030_bin DEFAULT NULL," +
	"`ID_NO_TRA` varchar(50) COLLATE gb18030_bin DEFAULT NULL," +
	"`ACC_NUMBER` varchar(4) COLLATE gb18030_bin DEFAULT NULL," +
	"`GD_ABN_TRA` varchar(8) COLLATE gb18030_bin DEFAULT NULL," +
	"`SOU_ABN_TRA` varchar(8) COLLATE gb18030_bin DEFAULT NULL," +
	"`TYP_ABN_TRA` varchar(8) COLLATE gb18030_bin DEFAULT NULL," +
	"`DET_ABN_TRA` varchar(500) COLLATE gb18030_bin DEFAULT NULL," +
	"`VER_RES` varchar(20) COLLATE gb18030_bin DEFAULT NULL," +
	"`VAL_OPI` varchar(500) COLLATE gb18030_bin DEFAULT NULL," +
	"`EOC_DATA` varchar(8) COLLATE gb18030_bin DEFAULT NULL," +
	"`CST_STATE` varchar(2) COLLATE gb18030_bin DEFAULT NULL," +
	"`CST_STATE_DATE` varchar(8) COLLATE gb18030_bin DEFAULT NULL," +
	"`RISK_CODE` varchar(2) COLLATE gb18030_bin DEFAULT NULL," +
	"`RISK_TIME` varchar(8) COLLATE gb18030_bin DEFAULT NULL," +
	"`TRANS_COUNT` decimal(16,0) DEFAULT NULL," +
	"`TRANS_AMT` decimal(16,2) DEFAULT NULL" +
	") ENGINE=InnoDB DEFAULT CHARSET=gb18030 COLLATE=gb18030_bin"

func TestTableInfoGB18030ShowCreate(t *testing.T) {
	// The executable disables local collation evaluation while reading metadata.
	previous := collate.NewCollationEnabled()
	collate.SetNewCollationEnabledForTest(false)
	t.Cleanup(func() { collate.SetNewCollationEnabledForTest(previous) })
	for _, withSession := range []bool{false, true} {
		t.Run(fmt.Sprint("session=", withSession), func(t *testing.T) {
			info, err := GetTableInfoBySQL(gb18030TableDDL, parser.New())
			if withSession {
				info, err = GetTableInfoBySQLWithSessionContext(mock.NewContext(), gb18030TableDDL, parser.New())
			}
			require.NoError(t, err)
			require.Equal(t, "ABNOMALY_RECORDS", info.Name.O)
			require.Equal(t, "gb18030", info.Charset)
			require.Equal(t, "gb18030_bin", info.Collate)
			require.Len(t, info.Columns, 24)
			require.Empty(t, info.Indices)
			for _, col := range info.Columns[:22] {
				require.Equal(t, "gb18030", col.GetCharset())
				require.Equal(t, "gb18030_bin", col.GetCollate())
				require.False(t, mysql.HasNotNullFlag(col.GetFlag()))
			}
			require.Equal(t, 500, info.Columns[14].GetFlen())
			require.Equal(t, mysql.TypeNewDecimal, info.Columns[23].GetType())
			require.Equal(t, 16, info.Columns[23].GetFlen())
			require.Equal(t, 2, info.Columns[23].GetDecimal())
		})
	}
}

func TestTableInfoGB18030MetadataConnection(t *testing.T) {
	previous := collate.NewCollationEnabled()
	collate.SetNewCollationEnabledForTest(false)
	t.Cleanup(func() { collate.SetNewCollationEnabledForTest(previous) })
	db, sqlMock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	sqlMock.ExpectQuery("SHOW CREATE TABLE").WillReturnRows(
		sqlmock.NewRows([]string{"Table", "Create Table"}).AddRow("ABNOMALY_RECORDS", gb18030TableDDL))
	sqlMock.ExpectQuery("SHOW VARIABLES LIKE 'sql_mode'").WillReturnRows(
		sqlmock.NewRows([]string{"Variable_name", "Value"}).AddRow("sql_mode", "STRICT_TRANS_TABLES"))
	info, err := GetTableInfoWithVersion(context.Background(), db, "odsrun", "ABNOMALY_RECORDS", nil)
	require.NoError(t, err)
	require.Equal(t, "gb18030", info.Charset)
	require.Len(t, info.Columns, 24)
	require.NoError(t, sqlMock.ExpectationsWereMet())
}

func TestTableInfoGB18030Inheritance(t *testing.T) {
	previous := collate.NewCollationEnabled()
	collate.SetNewCollationEnabledForTest(false)
	t.Cleanup(func() { collate.SetNewCollationEnabledForTest(previous) })
	for _, tableOption := range []string{"DEFAULT CHARSET=GB18030", "COLLATE=gb18030_bin"} {
		t.Run(tableOption, func(t *testing.T) {
			ddl := "CREATE TABLE t (a varchar(20) DEFAULT 'gb18030中文𠀀' COMMENT 'gb18030_bin', " +
				"b varchar(20) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin) " + tableOption
			info, err := GetTableInfoBySQL(ddl, parser.New())
			require.NoError(t, err)
			require.Equal(t, "gb18030", info.Columns[0].GetCharset())
			require.Equal(t, "gb18030_bin", info.Columns[0].GetCollate())
			require.Equal(t, "gb18030中文𠀀", info.Columns[0].GetDefaultValue())
			require.Equal(t, "gb18030_bin", info.Columns[0].Comment)
			require.Equal(t, "utf8mb4", info.Columns[1].GetCharset())
			require.Equal(t, "utf8mb4_bin", info.Columns[1].GetCollate())
		})
	}
}
