// Package sqlite provides the optional SQLite database adapter for PrismGo.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	gormsqlite "github.com/glebarez/sqlite"
	providercontract "github.com/prismgo/framework/contracts/provider"
	"github.com/prismgo/framework/database"
	dbschema "github.com/prismgo/framework/database/schema"
	"github.com/prismgo/framework/exception"
	"gorm.io/gorm"
)

// ServiceProvider installs SQLite dialectors on an application's database manager.
type ServiceProvider struct{}

// Dialector carries PrismGo SQLite capabilities alongside the GORM dialector.
type Dialector struct {
	*gormsqlite.Dialector
}

// Open creates a SQLite GORM dialector without opening a database connection.
func Open(dsn string) gorm.Dialector {
	return Dialector{Dialector: gormsqlite.Open(dsn).(*gormsqlite.Dialector)}
}

// CompileBlueprint compiles a PrismGo Blueprint for SQLite.
func (Dialector) CompileBlueprint(db *gorm.DB, blueprint *dbschema.Blueprint) ([]string, error) {
	definition := blueprint.Definition()
	if !definition.Creating {
		return compileAlterBlueprint(db, definition)
	}
	parts := make([]string, 0, len(definition.Columns))
	for _, column := range definition.Columns {
		parts = append(parts, compileColumn(column))
	}
	for _, index := range definition.Indexes {
		if index.Drop || index.Rename != "" {
			continue
		}
		if sql := inlineIndexSQL(index); sql != "" {
			parts = append(parts, sql)
		}
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("schema: create table %s has no columns", definition.Table)
	}
	sqls := []string{fmt.Sprintf("CREATE TABLE `%s` (%s)", definition.Table, strings.Join(parts, ", "))}
	for _, index := range definition.Indexes {
		if index.Drop || index.Rename != "" || index.Kind == "primary" {
			continue
		}
		unique := ""
		if index.Kind == "unique" {
			unique = "UNIQUE "
		}
		sqls = append(sqls, fmt.Sprintf(
			"CREATE %sINDEX IF NOT EXISTS %s ON %s (%s)",
			unique,
			quote(index.Name),
			quote(definition.Table),
			quotedColumns(index.Columns),
		))
	}
	return sqls, nil
}

func compileAlterBlueprint(db *gorm.DB, definition dbschema.BlueprintDefinition) ([]string, error) {
	var sqls []string
	for _, column := range definition.Columns {
		if column.Change {
			return nil, fmt.Errorf("%w: change column on sqlite", dbschema.ErrUnsupportedFeature)
		}
		if db.Migrator().HasColumn(definition.Table, column.Name) {
			continue
		}
		sqls = append(sqls, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", quote(definition.Table), compileColumn(column)))
	}
	for _, index := range definition.Indexes {
		if index.Drop {
			if !db.Migrator().HasIndex(definition.Table, index.Name) && index.Name != "PRIMARY" {
				continue
			}
			sqls = append(sqls, fmt.Sprintf("DROP INDEX IF EXISTS %s", quote(index.Name)))
			continue
		}
		if index.Rename != "" || db.Migrator().HasIndex(definition.Table, index.Name) {
			continue
		}
		unique := ""
		if index.Kind == "unique" {
			unique = "UNIQUE "
		}
		sqls = append(sqls, fmt.Sprintf(
			"CREATE %sINDEX IF NOT EXISTS %s ON %s (%s)",
			unique,
			quote(index.Name),
			quote(definition.Table),
			quotedColumns(index.Columns),
		))
	}
	for _, command := range definition.Commands {
		if command.SQL != "" {
			sqls = append(sqls, command.SQL)
			continue
		}
		switch command.Kind {
		case "renameColumn":
			if command.From == "" || command.To == "" {
				return nil, fmt.Errorf("schema: rename column requires source and target")
			}
			if !db.Migrator().HasColumn(definition.Table, command.From) || db.Migrator().HasColumn(definition.Table, command.To) {
				continue
			}
			sqls = append(sqls, fmt.Sprintf(
				"ALTER TABLE %s RENAME COLUMN %s TO %s",
				quote(definition.Table),
				quote(command.From),
				quote(command.To),
			))
		case "dropColumn":
			for _, name := range command.Names {
				if strings.TrimSpace(name) == "" || !db.Migrator().HasColumn(definition.Table, name) {
					continue
				}
				sqls = append(sqls, fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", quote(definition.Table), quote(name)))
			}
		}
	}
	return sqls, nil
}

func inlineIndexSQL(index dbschema.BlueprintIndex) string {
	switch index.Kind {
	case "primary":
		if len(index.Columns) > 1 {
			return "PRIMARY KEY (" + quotedColumns(index.Columns) + ")"
		}
	case "unique":
		return "UNIQUE (" + quotedColumns(index.Columns) + ")"
	}
	return ""
}

func compileColumn(column dbschema.BlueprintColumn) string {
	parts := []string{quote(column.Name), sqliteType(column.Kind)}
	if column.Primary {
		parts = append(parts, "PRIMARY KEY")
	}
	if column.AutoIncrement && column.Primary {
		parts = append(parts, "AUTOINCREMENT")
	}
	if !column.Nullable && !column.Primary {
		parts = append(parts, "NOT NULL")
	}
	if column.DefaultValue != nil {
		parts = append(parts, "DEFAULT "+*column.DefaultValue)
	}
	if column.Unique {
		parts = append(parts, "UNIQUE")
	}
	return strings.Join(parts, " ")
}

func sqliteType(kind string) string {
	switch kind {
	case "tinyInteger", "smallInteger", "mediumInteger", "integer", "bigInteger", "boolean":
		return "integer"
	case "float", "double":
		return "real"
	case "decimal":
		return "numeric"
	case "binary":
		return "blob"
	case "date", "dateTime", "time", "timestamp", "year":
		return "datetime"
	default:
		return "text"
	}
}

func quote(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func quotedColumns(columns []string) string {
	quoted := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted = append(quoted, quote(column))
	}
	return strings.Join(quoted, ", ")
}

// GetSchemas returns the SQLite schemas visible to db.
func (Dialector) GetSchemas(db *gorm.DB) ([]dbschema.SchemaInfo, error) {
	var rows []struct{ Name string }
	if err := db.Raw("PRAGMA database_list").Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]dbschema.SchemaInfo, 0, len(rows))
	for _, row := range rows {
		items = append(items, dbschema.SchemaInfo{Name: row.Name})
	}
	return items, nil
}

// GetTables returns SQLite table metadata.
func (Dialector) GetTables(db *gorm.DB, schemas []string) ([]dbschema.TableInfo, error) {
	items := make([]dbschema.TableInfo, 0)
	for _, schemaName := range sqliteSchemas(schemas) {
		var rows []dbschema.TableInfo
		query := fmt.Sprintf("SELECT name, ? AS `schema`, type FROM %s.sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%%' ORDER BY name", quote(schemaName))
		if err := db.Raw(query, schemaName).Scan(&rows).Error; err != nil {
			return nil, err
		}
		items = append(items, rows...)
	}
	return items, nil
}

// GetViews returns SQLite view metadata.
func (Dialector) GetViews(db *gorm.DB, schemas []string) ([]dbschema.ViewInfo, error) {
	items := make([]dbschema.ViewInfo, 0)
	for _, schemaName := range sqliteSchemas(schemas) {
		var rows []dbschema.ViewInfo
		query := fmt.Sprintf("SELECT name, ? AS `schema`, sql AS definition FROM %s.sqlite_master WHERE type = 'view' ORDER BY name", quote(schemaName))
		if err := db.Raw(query, schemaName).Scan(&rows).Error; err != nil {
			return nil, err
		}
		items = append(items, rows...)
	}
	return items, nil
}

func sqliteSchemas(schemas []string) []string {
	if len(schemas) == 0 {
		return []string{"main"}
	}
	items := make([]string, 0, len(schemas))
	for _, schemaName := range schemas {
		if schemaName = strings.TrimSpace(schemaName); schemaName != "" {
			items = append(items, schemaName)
		}
	}
	if len(items) == 0 {
		return []string{"main"}
	}
	return items
}

// GetTypes returns an empty list because SQLite has no independent user-defined type objects.
func (Dialector) GetTypes(*gorm.DB, any) ([]dbschema.TypeInfo, error) {
	return []dbschema.TypeInfo{}, nil
}

// GetForeignKeys returns SQLite foreign key metadata.
func (Dialector) GetForeignKeys(db *gorm.DB, table string) ([]dbschema.ForeignKeyInfo, error) {
	rows, err := db.Raw("PRAGMA foreign_key_list(" + quote(table) + ")").Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := rows.Close(); err != nil {
			exception.Report(context.Background(), err, map[string]any{
				"component": "database",
				"operation": "close_sqlite_foreign_key_rows",
				"table":     table,
			})
		}
	}()
	grouped := make(map[string]*dbschema.ForeignKeyInfo)
	for rows.Next() {
		var (
			id, seq            int
			refTable, from, to string
			onUpdate, onDelete string
			match              sql.NullString
		)
		if err := rows.Scan(&id, &seq, &refTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return nil, err
		}
		name := fmt.Sprintf("fk_%s_%d", table, id)
		item := grouped[name]
		if item == nil {
			item = &dbschema.ForeignKeyInfo{Name: name, ForeignTable: refTable, OnUpdate: onUpdate, OnDelete: onDelete}
			grouped[name] = item
		}
		item.Columns = append(item.Columns, from)
		item.ForeignColumns = append(item.ForeignColumns, to)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := make([]dbschema.ForeignKeyInfo, 0, len(grouped))
	for _, item := range grouped {
		items = append(items, *item)
	}
	return items, nil
}

// EnableForeignKeyConstraints enables SQLite foreign key checks.
func (Dialector) EnableForeignKeyConstraints(db *gorm.DB) error {
	return db.Exec("PRAGMA foreign_keys = ON").Error
}

// DisableForeignKeyConstraints disables SQLite foreign key checks.
func (Dialector) DisableForeignKeyConstraints(db *gorm.DB) error {
	return db.Exec("PRAGMA foreign_keys = OFF").Error
}

// EnsureCompositeIndexes creates SQLite composite indexes idempotently.
func (Dialector) EnsureCompositeIndexes(db *gorm.DB, indexes []database.CompositeIndex) error {
	for _, index := range indexes {
		query := fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (%s)", quote(index.Name), quote(index.Table), index.Columns)
		if err := db.Exec(query).Error; err != nil {
			return fmt.Errorf("create sqlite index %s.%s failed: %w", index.Table, index.Name, err)
		}
	}
	return nil
}

// EnsureCompositeUniqueIndexes creates SQLite composite unique indexes idempotently.
func (Dialector) EnsureCompositeUniqueIndexes(db *gorm.DB, indexes []database.CompositeUniqueIndex) error {
	for _, index := range indexes {
		query := fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s)", quote(index.Name), quote(index.Table), index.Columns)
		if err := db.Exec(query).Error; err != nil {
			return fmt.Errorf("create sqlite index %s.%s failed: %w", index.Table, index.Name, err)
		}
	}
	return nil
}

// Name returns the provider's stable lifecycle identity.
func (ServiceProvider) Name() string { return "prismgo.extension.sqlite" }

// Register declares no eager bindings because SQLite is installed during Boot.
func (ServiceProvider) Register(providercontract.Application) error { return nil }

// Boot registers sqlite and sqlite3 without opening a database connection.
func (ServiceProvider) Boot(app providercontract.Application) error {
	manager, err := database.ManagerFrom(app.Container())
	if err != nil {
		return err
	}
	factory := func(ctx database.DriverContext) (gorm.Dialector, error) {
		return Open(ctx.DSN), nil
	}
	manager.Extend("sqlite", factory)
	manager.Extend("sqlite3", factory)
	return nil
}

var _ providercontract.ServiceProvider = ServiceProvider{}
var _ providercontract.NamedProvider = ServiceProvider{}
