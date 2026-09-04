package db2util

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/pingcap/errors"
	"github.com/pingcap/tidb/pkg/meta/model"
	pmodel "github.com/pingcap/tidb/pkg/parser/model"
	"github.com/pingcap/tidb/pkg/parser/mysql"
	"github.com/pingcap/tidb/pkg/types"
)

const columnsSQL = `SELECT COLNAME, COLNO, TYPENAME, LENGTH, SCALE, NULLS
FROM SYSCAT.COLUMNS WHERE TABSCHEMA = ? AND TABNAME = ? ORDER BY COLNO`

const indexesSQL = `SELECT i.INDNAME, i.UNIQUERULE, c.COLNAME, c.COLSEQ
FROM SYSCAT.INDEXES i JOIN SYSCAT.INDEXCOLUSE c
  ON i.INDSCHEMA = c.INDSCHEMA AND i.INDNAME = c.INDNAME
WHERE i.TABSCHEMA = ? AND i.TABNAME = ? ORDER BY i.INDNAME, c.COLSEQ`

const constraintsSQL = `SELECT c.CONSTNAME, c.TYPE, k.COLNAME, k.COLSEQ
FROM SYSCAT.TABCONST c JOIN SYSCAT.KEYCOLUSE k
  ON c.TABSCHEMA = k.TABSCHEMA AND c.TABNAME = k.TABNAME AND c.CONSTNAME = k.CONSTNAME
WHERE c.TABSCHEMA = ? AND c.TABNAME = ? AND c.TYPE IN ('P', 'U')
ORDER BY c.CONSTNAME, k.COLSEQ`

// Column describes the Db2 catalog values before their mapping to TiDB's
// internal model. This keeps Db2 catalog terminology at the boundary.
type Column struct {
	Name     string
	Offset   int
	TypeName string
	Length   int
	Scale    int
	Nullable bool
}

// ReadTableInfo reads only the Db2 LUW catalog and maps it to the existing
// comparison model. Unsupported types fail before any table data is scanned.
func ReadTableInfo(ctx context.Context, db *sql.DB, schema, table string) (*model.TableInfo, error) {
	schema, table = NormalizeIdentifier(schema), NormalizeIdentifier(table)
	rows, err := db.QueryContext(ctx, columnsSQL, schema, table)
	if err != nil {
		return nil, errors.Annotate(ClassifyError(err), "read db2 column catalog")
	}
	defer rows.Close()

	info := &model.TableInfo{Name: pmodel.NewCIStr(table)}
	for rows.Next() {
		var c Column
		var nulls string
		if err := rows.Scan(&c.Name, &c.Offset, &c.TypeName, &c.Length, &c.Scale, &nulls); err != nil {
			return nil, errors.Annotate(err, "scan db2 column catalog")
		}
		c.Nullable = strings.EqualFold(nulls, "Y")
		ft, err := MapType(c.TypeName, c.Length, c.Scale)
		if err != nil {
			return nil, errors.Annotatef(err, "unsupported db2 column %s.%s.%s", schema, table, c.Name)
		}
		col := &model.ColumnInfo{Name: pmodel.NewCIStr(c.Name), Offset: c.Offset, FieldType: *ft}
		if !c.Nullable {
			col.AddFlag(mysql.NotNullFlag)
		}
		info.Columns = append(info.Columns, col)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Annotate(err, "iterate db2 column catalog")
	}
	if len(info.Columns) == 0 {
		return nil, errors.Errorf("db2 table %s.%s not found or has no columns", schema, table)
	}
	indices, err := readIndexes(ctx, db, schema, table, info.Columns)
	if err != nil {
		return nil, err
	}
	info.Indices = indices
	return info, nil
}

func readIndexes(ctx context.Context, db *sql.DB, schema, table string, columns []*model.ColumnInfo) ([]*model.IndexInfo, error) {
	indexByName := make(map[string]*model.IndexInfo)
	if err := readIndexRows(ctx, db, constraintsSQL, schema, table, columns, indexByName, true); err != nil {
		return nil, err
	}
	if err := readIndexRows(ctx, db, indexesSQL, schema, table, columns, indexByName, false); err != nil {
		return nil, err
	}
	result := make([]*model.IndexInfo, 0, len(indexByName))
	for _, index := range indexByName {
		result = append(result, index)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Primary != result[j].Primary {
			return result[i].Primary
		}
		if result[i].Unique != result[j].Unique {
			return result[i].Unique
		}
		return result[i].Name.O < result[j].Name.O
	})
	return result, nil
}

func readIndexRows(ctx context.Context, db *sql.DB, query, schema, table string, columns []*model.ColumnInfo, out map[string]*model.IndexInfo, constraint bool) error {
	rows, err := db.QueryContext(ctx, query, schema, table)
	if err != nil {
		return errors.Annotate(ClassifyError(err), "read db2 index catalog")
	}
	defer rows.Close()
	for rows.Next() {
		var name, kind, column string
		var sequence int
		if err := rows.Scan(&name, &kind, &column, &sequence); err != nil {
			return errors.Annotate(err, "scan db2 index catalog")
		}
		index := out[name]
		if index == nil {
			primary := constraint && strings.EqualFold(kind, "P")
			index = &model.IndexInfo{Name: pmodel.NewCIStr(name), State: model.StatePublic, Unique: primary || (constraint && strings.EqualFold(kind, "U")) || strings.EqualFold(kind, "U"), Primary: primary}
			out[name] = index
		}
		columnInfo := findColumn(columns, column)
		if columnInfo == nil {
			return errors.Errorf("db2 index %s references unknown column %s", name, column)
		}
		index.Columns = append(index.Columns, &model.IndexColumn{Name: columnInfo.Name, Offset: columnInfo.Offset, Length: types.UnspecifiedLength})
	}
	return rows.Err()
}

func findColumn(columns []*model.ColumnInfo, name string) *model.ColumnInfo {
	for _, column := range columns {
		if strings.EqualFold(column.Name.O, name) {
			return column
		}
	}
	return nil
}

// MapType maps the V1 Db2 LUW type set to the internal column model. Types
// that cannot be represented without semantic loss are rejected.
func MapType(typeName string, length, scale int) (*types.FieldType, error) {
	name := strings.ToUpper(strings.TrimSpace(typeName))
	var tp byte
	switch name {
	case "SMALLINT":
		tp = mysql.TypeShort
	case "INTEGER", "INT":
		tp = mysql.TypeLong
	case "BIGINT":
		tp = mysql.TypeLonglong
	case "DECIMAL", "NUMERIC":
		tp = mysql.TypeNewDecimal
	case "REAL":
		tp = mysql.TypeFloat
	case "DOUBLE", "DOUBLE PRECISION":
		tp = mysql.TypeDouble
	case "CHAR", "CHARACTER", "GRAPHIC":
		tp = mysql.TypeString
	case "VARCHAR", "CHARACTER VARYING", "VARGRAPHIC":
		tp = mysql.TypeVarString
	case "DATE":
		tp = mysql.TypeDate
	case "TIME":
		tp = mysql.TypeDuration
	case "TIMESTAMP":
		tp = mysql.TypeDatetime
	case "BOOLEAN":
		tp = mysql.TypeTiny
	case "BINARY", "VARBINARY":
		tp = mysql.TypeBlob
	case "BLOB", "CLOB", "DBCLOB":
		tp = mysql.TypeLongBlob
	case "DECFLOAT", "XML", "ROWID", "TIMESTAMP WITH TIME ZONE", "TIME WITH TIME ZONE":
		return nil, errors.Errorf("db2 type %s is not supported by V1", name)
	default:
		return nil, errors.Errorf("db2 type %s is not supported by V1", name)
	}
	ft := types.NewFieldType(tp)
	if length > 0 {
		ft.SetFlen(length)
	}
	if scale >= 0 && (tp == mysql.TypeNewDecimal || tp == mysql.TypeDatetime || tp == mysql.TypeDuration) {
		ft.SetDecimal(scale)
	}
	return ft, nil
}

func TypeSupportMatrix() map[string]string {
	return map[string]string{
		"SMALLINT/INTEGER/BIGINT": "supported", "DECIMAL/NUMERIC": "supported", "REAL/DOUBLE": "supported",
		"CHAR/VARCHAR/GRAPHIC/VARGRAPHIC": "supported", "DATE/TIME/TIMESTAMP": "supported",
		"BOOLEAN/BINARY/VARBINARY": "supported", "BLOB/CLOB/DBCLOB": "supported with configured size limit",
		"DECFLOAT/XML/ROWID/time zone types": "rejected in V1",
	}
}

func CatalogQueries() map[string]string {
	return map[string]string{"columns": columnsSQL, "indexes": indexesSQL, "constraints": constraintsSQL}
}

func ColumnDiagnostic(schema, table, column string) string {
	return fmt.Sprintf("%s.%s.%s", NormalizeIdentifier(schema), NormalizeIdentifier(table), NormalizeIdentifier(column))
}
