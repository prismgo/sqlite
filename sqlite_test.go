package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prismgo/framework/cmd"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/console"
	"github.com/prismgo/framework/container"
	"github.com/prismgo/framework/database"
	dbschema "github.com/prismgo/framework/database/schema"
	"github.com/prismgo/framework/foundation"
	"github.com/spf13/cobra"
	"gorm.io/gorm"
)

func TestServiceProviderRegistersSQLiteDialectorsOnApplicationManager(t *testing.T) {
	app := foundation.Configure().WithExtensionProviders(ServiceProvider{}).Create()
	t.Cleanup(func() { _ = app.Close() })
	if err := app.Boot(); err != nil {
		t.Fatalf("Boot failed: %v", err)
	}
	manager, err := database.ManagerFrom(app.Container())
	if err != nil {
		t.Fatalf("resolve database manager: %v", err)
	}

	for _, driver := range []string{"sqlite", "sqlite3"} {
		db, err := manager.Open(driver, ":memory:", database.MySQLConfig{})
		if err != nil {
			t.Fatalf("Open(%q) failed: %v", driver, err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("Open(%q) sql.DB failed: %v", driver, err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		if got := db.Name(); got != "sqlite" {
			t.Fatalf("Open(%q) dialector = %q, want sqlite", driver, got)
		}
	}
}

func TestOpenPreservesGORMOptionalDialectorCapabilities(t *testing.T) {
	dialector := Open(":memory:")
	if _, ok := dialector.(gorm.SavePointerDialectorInterface); !ok {
		t.Fatal("Open() dialector does not implement gorm.SavePointerDialectorInterface")
	}
	if _, ok := dialector.(gorm.ErrorTranslator); !ok {
		t.Fatal("Open() dialector does not implement gorm.ErrorTranslator")
	}
}

func TestExtensionSupportsMemoryFilePrefixSchemaMetadataAndClose(t *testing.T) {
	tests := []struct {
		name string
		dsn  func(*testing.T) string
	}{
		{name: "memory", dsn: func(*testing.T) string { return "file::memory:?cache=shared" }},
		{name: "file", dsn: func(t *testing.T) string { return filepath.Join(t.TempDir(), "database.sqlite") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := foundation.Configure().WithExtensionProviders(ServiceProvider{}).Create()
			t.Cleanup(func() { _ = app.Close() })
			if err := app.Boot(); err != nil {
				t.Fatalf("Boot failed: %v", err)
			}
			manager, err := database.ManagerFrom(app.Container())
			if err != nil {
				t.Fatalf("resolve manager: %v", err)
			}
			dsn := tt.dsn(t)
			db, err := manager.Open("sqlite", dsn, database.MySQLConfig{
				Schema: database.MySQLSchemaConfig{TablePrefix: "ext_"},
			})
			if err != nil {
				t.Fatalf("open SQLite: %v", err)
			}
			if err := db.AutoMigrate(&extensionWidget{}); err != nil {
				t.Fatalf("migrate widget: %v", err)
			}
			builder := dbschema.New(db)
			if !builder.HasTable("ext_extension_widgets") || !builder.HasColumn("ext_extension_widgets", "name") {
				t.Fatal("schema metadata did not expose the prefixed widget table and name column")
			}
			columns, err := builder.GetColumns("ext_extension_widgets")
			if err != nil || len(columns) < 2 {
				t.Fatalf("GetColumns() = (%#v, %v), want widget metadata", columns, err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatalf("sql.DB failed: %v", err)
			}
			if err := sqlDB.Close(); err != nil {
				t.Fatalf("close SQLite: %v", err)
			}
		})
	}
}

func TestExtensionSupportsOpenDefaultConnection(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "database.sqlite")
	config.Add("database", func() map[string]any {
		return map[string]any{
			"default": "testing",
			"connections": map[string]any{
				"testing": map[string]any{"driver": "sqlite", "dsn": dsn},
			},
		}
	})
	app := foundation.Configure().WithExtensionProviders(ServiceProvider{}).Create()
	t.Cleanup(func() { _ = app.Close() })
	if err := app.Boot(); err != nil {
		t.Fatalf("Boot() error = %v, want nil", err)
	}
	db, err := database.OpenDefaultConnection()
	if err != nil {
		t.Fatalf("OpenDefaultConnection() error = %v, want nil", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("OpenDefaultConnection().DB() error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("OpenDefaultConnection().DB().Close() error = %v, want nil", err)
		}
	})
	if db.Name() != "sqlite" {
		t.Fatalf("OpenDefaultConnection() dialect = %q, want sqlite", db.Name())
	}
}

func TestExtensionCompilesBlueprintThroughSQLiteDialector(t *testing.T) {
	app := foundation.Configure().WithExtensionProviders(ServiceProvider{}).Create()
	t.Cleanup(func() { _ = app.Close() })
	if err := app.Boot(); err != nil {
		t.Fatalf("Boot() error = %v, want nil", err)
	}
	manager, err := database.ManagerFrom(app.Container())
	if err != nil {
		t.Fatalf("ManagerFrom() error = %v, want nil", err)
	}
	db, err := manager.Open("sqlite", "file::memory:?cache=shared", database.MySQLConfig{})
	if err != nil {
		t.Fatalf("Open() error = %v, want nil", err)
	}

	err = dbschema.New(db).Create("widgets", func(table *dbschema.Blueprint) {
		table.Id()
		table.String("slug").Unique()
		table.Boolean("enabled")
		table.Float("ratio")
		table.Decimal("amount")
		table.Binary("blob_value")
		table.Date("business_date")
		table.ForeignId("user_id").Constrained("users").NoActionOnDelete().NoActionOnUpdate()
	})
	if err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	if !dbschema.New(db).HasTable("widgets") {
		t.Fatal("HasTable() = false, want true after Blueprint creation")
	}
}

func TestExtensionSyncModelsAddsMissingColumns(t *testing.T) {
	db := openSQLiteTestDB(t)
	builder := dbschema.New(db)
	if err := builder.SyncModels(&syncBaseWidget{}); err != nil {
		t.Fatalf("SyncModels(base) error = %v, want nil", err)
	}
	if err := builder.SyncModels(&syncExpandedWidget{}); err != nil {
		t.Fatalf("SyncModels(expanded) error = %v, want nil", err)
	}
	if !builder.HasColumn("sync_widgets", "code") {
		t.Fatal("HasColumn(sync_widgets.code) = false, want true")
	}
}

func TestExtensionBlueprintCreatesCompositeIndexes(t *testing.T) {
	db := openSQLiteTestDB(t)
	builder := dbschema.New(db)
	err := builder.Create("memberships", func(table *dbschema.Blueprint) {
		table.Integer("tenant_id")
		table.Integer("owner_id")
		table.String("slug")
		table.Primary("tenant_id", "owner_id")
		table.UniqueNamed("memberships_tenant_slug", "tenant_id", "slug")
	})
	if err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	indexes, err := builder.GetIndexes("memberships")
	if err != nil {
		t.Fatalf("GetIndexes() error = %v, want nil", err)
	}
	if !hasIndex(indexes, "PRIMARY", []string{"tenant_id", "owner_id"}) {
		t.Fatalf("GetIndexes() = %#v, want composite primary index", indexes)
	}
	if !hasIndex(indexes, "memberships_tenant_slug", []string{"tenant_id", "slug"}) {
		t.Fatalf("GetIndexes() = %#v, want memberships_tenant_slug", indexes)
	}
}

func TestExtensionBlueprintAddsColumnsAndIndexes(t *testing.T) {
	db := openSQLiteTestDB(t)
	builder := dbschema.New(db)
	if err := builder.Create("profiles", func(table *dbschema.Blueprint) { table.Id() }); err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	err := builder.Table("profiles", func(table *dbschema.Blueprint) {
		table.String("handle")
		table.IndexNamed("profiles_handle", "handle")
	})
	if err != nil {
		t.Fatalf("Table() error = %v, want nil", err)
	}
	if !builder.HasColumn("profiles", "handle") {
		t.Fatal("HasColumn(handle) = false, want true")
	}
	if !builder.HasIndex("profiles", "profiles_handle") {
		t.Fatal("HasIndex(profiles_handle) = false, want true")
	}
	if err := builder.Table("profiles", func(table *dbschema.Blueprint) {
		table.String("handle")
		table.IndexNamed("profiles_handle", "handle")
	}); err != nil {
		t.Fatalf("second idempotent Table() error = %v, want nil", err)
	}
}

func TestExtensionBlueprintRejectsColumnChange(t *testing.T) {
	db := openSQLiteTestDB(t)
	builder := dbschema.New(db)
	if err := builder.Create("change_guards", func(table *dbschema.Blueprint) {
		table.Id()
		table.String("name")
	}); err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	err := builder.Table("change_guards", func(table *dbschema.Blueprint) {
		table.String("name", 64).Change()
	})
	if !errors.Is(err, dbschema.ErrUnsupportedFeature) {
		t.Fatalf("Table() error = %v, want ErrUnsupportedFeature", err)
	}
}

func TestExtensionBlueprintRenamesColumnsAndDropsIndexes(t *testing.T) {
	db := openSQLiteTestDB(t)
	builder := dbschema.New(db)
	if err := builder.Create("accounts", func(table *dbschema.Blueprint) {
		table.Id()
		table.String("handle")
		table.IndexNamed("accounts_handle", "handle")
	}); err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	err := builder.Table("accounts", func(table *dbschema.Blueprint) {
		table.DropIndex("accounts_handle")
		table.RenameColumn("handle", "name")
	})
	if err != nil {
		t.Fatalf("Table() error = %v, want nil", err)
	}
	if builder.HasColumn("accounts", "handle") || !builder.HasColumn("accounts", "name") {
		t.Fatal("column state did not change from handle to name")
	}
	if builder.HasIndex("accounts", "accounts_handle") {
		t.Fatal("HasIndex(accounts_handle) = true, want false after DropIndex")
	}
}

func TestExtensionBlueprintRejectsIncompleteRename(t *testing.T) {
	db := openSQLiteTestDB(t)
	builder := dbschema.New(db)
	if err := builder.Create("rename_guards", func(table *dbschema.Blueprint) { table.Id() }); err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	err := builder.Table("rename_guards", func(table *dbschema.Blueprint) { table.RenameColumn("", "name") })
	if err == nil || !strings.Contains(err.Error(), "rename column requires") {
		t.Fatalf("Table() error = %v, want rename-column validation error", err)
	}
}

func TestExtensionBlueprintCoversValidationAndAlterBranches(t *testing.T) {
	db := openSQLiteTestDB(t)
	builder := dbschema.New(db)
	if err := builder.Create("empty_blueprint", func(*dbschema.Blueprint) {}); err == nil {
		t.Fatal("Create(empty_blueprint) error = nil, want missing-columns error")
	}
	if err := builder.Create("alter_branches", func(table *dbschema.Blueprint) {
		table.Id()
		table.String("obsolete")
		table.String("status").Default("pending").Nullable()
	}); err != nil {
		t.Fatalf("Create(alter_branches) error = %v, want nil", err)
	}
	if err := builder.Table("alter_branches", func(table *dbschema.Blueprint) {
		table.Raw("UPDATE alter_branches SET status = 'ready'")
		table.DropColumn("obsolete", "missing")
		table.DropIndex("missing_index")
		table.RenameIndex("missing_index", "renamed_index")
		table.RenameColumn("missing", "ignored")
		table.RenameColumn("id", "status")
	}); err != nil {
		t.Fatalf("Table(alter_branches) error = %v, want nil", err)
	}
	if builder.HasColumn("alter_branches", "obsolete") {
		t.Fatal("HasColumn(alter_branches.obsolete) = true, want false")
	}
}

func TestExtensionPropagatesSQLiteOperationErrors(t *testing.T) {
	db := openSQLiteTestDB(t)
	if err := db.Exec("CREATE TABLE closed_db_widgets (tenant_id integer, slug text)").Error; err != nil {
		t.Fatalf("create closed_db_widgets error = %v, want nil", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("DB() error = %v, want nil", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}

	builder := dbschema.New(db)
	if _, err := builder.GetSchemas(); err == nil {
		t.Fatal("GetSchemas() error = nil, want closed-database error")
	}
	if _, err := builder.GetForeignKeys("closed_db_widgets"); err == nil {
		t.Fatal("GetForeignKeys() error = nil, want closed-database error")
	}
	if err := database.EnsureCompositeIndexes(db, []database.CompositeIndex{{
		Table: "closed_db_widgets", Name: "closed_db_widgets_tenant", Columns: "tenant_id, slug",
	}}); err == nil {
		t.Fatal("EnsureCompositeIndexes() error = nil, want closed-database error")
	}
	if err := database.EnsureCompositeUniqueIndexes(db, []database.CompositeUniqueIndex{{
		Table: "closed_db_widgets", Name: "closed_db_widgets_slug", Columns: "tenant_id, slug",
	}}); err == nil {
		t.Fatal("EnsureCompositeUniqueIndexes() error = nil, want closed-database error")
	}
}

func TestExtensionProvidesSQLiteMetadata(t *testing.T) {
	db := openSQLiteTestDB(t)
	statements := []string{
		"CREATE TABLE parents (id integer primary key)",
		"CREATE TABLE children (id integer primary key, parent_id integer, CONSTRAINT fk_children_parent FOREIGN KEY(parent_id) REFERENCES parents(id) ON DELETE CASCADE)",
		"CREATE TABLE metadata_widgets (id integer primary key, name text, code text)",
		"CREATE UNIQUE INDEX metadata_widgets_name ON metadata_widgets(name)",
		"CREATE INDEX metadata_widgets_code ON metadata_widgets(code)",
		"CREATE VIEW child_ids AS SELECT id FROM children",
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("Exec(%q) error = %v, want nil", statement, err)
		}
	}
	builder := dbschema.New(db)
	schemas, err := builder.GetSchemas()
	if err != nil || len(schemas) == 0 || schemas[0].Name != "main" {
		t.Fatalf("GetSchemas() = (%#v, %v), want main schema", schemas, err)
	}
	tables, err := builder.GetTables(nil)
	if err != nil || !hasTable(tables, "parents") || !hasTable(tables, "children") {
		t.Fatalf("GetTables() = (%#v, %v), want parents and children", tables, err)
	}
	listing, err := builder.GetTableListing(nil, false)
	if err != nil || !contains(listing, "metadata_widgets") {
		t.Fatalf("GetTableListing(false) = (%#v, %v), want metadata_widgets", listing, err)
	}
	qualified, err := builder.GetTableListing(nil)
	if err != nil || !contains(qualified, "main.metadata_widgets") {
		t.Fatalf("GetTableListing() = (%#v, %v), want main.metadata_widgets", qualified, err)
	}
	views, err := builder.GetViews(nil)
	if err != nil || len(views) != 1 || views[0].Name != "child_ids" {
		t.Fatalf("GetViews() = (%#v, %v), want child_ids", views, err)
	}
	if !builder.HasView("child_ids") || !builder.HasView("main.child_ids") {
		t.Fatal("HasView() did not accept both plain and schema-qualified names")
	}
	types, err := builder.GetTypes(nil)
	if err != nil || len(types) != 0 {
		t.Fatalf("GetTypes() = (%#v, %v), want empty list", types, err)
	}
	foreignKeys, err := builder.GetForeignKeys("children")
	if err != nil || len(foreignKeys) != 1 || foreignKeys[0].ForeignTable != "parents" {
		t.Fatalf("GetForeignKeys() = (%#v, %v), want parents reference", foreignKeys, err)
	}
	columns, err := builder.GetColumns("metadata_widgets")
	if err != nil || len(columns) != 3 {
		t.Fatalf("GetColumns() = (%#v, %v), want three columns", columns, err)
	}
	columnType, err := builder.GetColumnType("metadata_widgets", "name", true)
	if err != nil || columnType == "" {
		t.Fatalf("GetColumnType(name) = (%q, %v), want non-empty type", columnType, err)
	}
	if _, err := builder.GetColumnType("metadata_widgets", "missing"); err == nil {
		t.Fatal("GetColumnType(missing) error = nil, want not-found error")
	}
	indexes, err := builder.GetIndexes("metadata_widgets")
	if err != nil || !hasIndex(indexes, "metadata_widgets_name", []string{"name"}) {
		t.Fatalf("GetIndexes() = (%#v, %v), want metadata_widgets_name", indexes, err)
	}
	indexNames, err := builder.GetIndexListing("metadata_widgets")
	if err != nil || !contains(indexNames, "metadata_widgets_code") {
		t.Fatalf("GetIndexListing() = (%#v, %v), want metadata_widgets_code", indexNames, err)
	}
	ran := false
	if err := builder.WhenTableHasIndex("metadata_widgets", []string{"code"}, func() error {
		ran = true
		return nil
	}); err != nil || !ran {
		t.Fatalf("WhenTableHasIndex() = (ran %v, error %v), want (true, nil)", ran, err)
	}
	ran = false
	if err := builder.WhenTableDoesntHaveIndex("metadata_widgets", "missing", func() error {
		ran = true
		return nil
	}); err != nil || !ran {
		t.Fatalf("WhenTableDoesntHaveIndex() = (ran %v, error %v), want (true, nil)", ran, err)
	}
}

func TestExtensionFiltersSQLiteTablesAndViewsByAttachedSchema(t *testing.T) {
	// Register TempDir cleanup before the connection cleanup so Windows can remove the attached file.
	auxPath := filepath.Join(t.TempDir(), "aux.sqlite")
	db := openSQLiteTestDB(t)
	if err := db.Exec("CREATE TABLE main_widget (id integer primary key)").Error; err != nil {
		t.Fatalf("create main table: %v", err)
	}
	err := db.Connection(func(connection *gorm.DB) error {
		if err := connection.Exec("ATTACH DATABASE ? AS aux", auxPath).Error; err != nil {
			return fmt.Errorf("attach aux database: %w", err)
		}
		if err := connection.Exec("CREATE TABLE aux.aux_widget (id integer primary key)").Error; err != nil {
			return fmt.Errorf("create aux table: %w", err)
		}
		if err := connection.Exec("CREATE VIEW aux.aux_widget_ids AS SELECT id FROM aux_widget").Error; err != nil {
			return fmt.Errorf("create aux view: %w", err)
		}

		builder := dbschema.New(connection)
		tables, err := builder.GetTables("aux")
		if err != nil {
			return fmt.Errorf("GetTables(aux): %w", err)
		}
		if len(tables) != 1 || tables[0].Name != "aux_widget" || tables[0].Schema != "aux" {
			return fmt.Errorf("GetTables(aux) = %#v, want only aux.aux_widget", tables)
		}
		views, err := builder.GetViews([]string{"aux"})
		if err != nil {
			return fmt.Errorf("GetViews(aux): %w", err)
		}
		if len(views) != 1 || views[0].Name != "aux_widget_ids" || views[0].Schema != "aux" {
			return fmt.Errorf("GetViews(aux) = %#v, want only aux.aux_widget_ids", views)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("attached schema metadata error = %v, want nil", err)
	}
}

func TestExtensionControlsSQLiteForeignKeyChecks(t *testing.T) {
	db := openSQLiteTestDB(t)
	builder := dbschema.New(db)
	if err := builder.EnableForeignKeyConstraints(); err != nil {
		t.Fatalf("EnableForeignKeyConstraints() error = %v, want nil", err)
	}
	if actual := foreignKeyChecks(t, db); actual != 1 {
		t.Fatalf("PRAGMA foreign_keys = %d, want 1 after enable", actual)
	}
	if err := builder.DisableForeignKeyConstraints(); err != nil {
		t.Fatalf("DisableForeignKeyConstraints() error = %v, want nil", err)
	}
	if actual := foreignKeyChecks(t, db); actual != 0 {
		t.Fatalf("PRAGMA foreign_keys = %d, want 0 after disable", actual)
	}
}

func foreignKeyChecks(t *testing.T, db *gorm.DB) int {
	t.Helper()
	var actual int
	if err := db.Raw("PRAGMA foreign_keys").Scan(&actual).Error; err != nil {
		t.Fatalf("read PRAGMA foreign_keys error = %v, want nil", err)
	}
	return actual
}

func TestExtensionCreatesCompositeIndexesThroughDatabaseHelpers(t *testing.T) {
	db := openSQLiteTestDB(t)
	if err := db.Exec("CREATE TABLE widgets (tenant_id integer, slug text, owner_id integer)").Error; err != nil {
		t.Fatalf("create widgets error = %v, want nil", err)
	}
	if err := database.EnsureCompositeIndexes(db, []database.CompositeIndex{{
		Table: "widgets", Name: "widgets_owner", Columns: "tenant_id, owner_id",
	}}); err != nil {
		t.Fatalf("EnsureCompositeIndexes() error = %v, want nil", err)
	}
	if err := database.EnsureCompositeUniqueIndexes(db, []database.CompositeUniqueIndex{{
		Table: "widgets", Name: "widgets_slug", Columns: "tenant_id, slug",
	}}); err != nil {
		t.Fatalf("EnsureCompositeUniqueIndexes() error = %v, want nil", err)
	}
	indexes, err := dbschema.New(db).GetIndexes("widgets")
	if err != nil {
		t.Fatalf("GetIndexes() error = %v, want nil", err)
	}
	if !hasIndex(indexes, "widgets_owner", []string{"tenant_id", "owner_id"}) ||
		!hasIndex(indexes, "widgets_slug", []string{"tenant_id", "slug"}) {
		t.Fatalf("GetIndexes() = %#v, want both helper-created indexes", indexes)
	}
}

func TestExtensionDropsSQLiteObjectsForFreshMigrations(t *testing.T) {
	db := openSQLiteTestDB(t)
	for _, table := range []string{"fresh_widgets", "fresh_posts", "fresh_comments"} {
		if err := db.Exec("CREATE TABLE " + table + " (id integer primary key)").Error; err != nil {
			t.Fatalf("create table %s error = %v, want nil", table, err)
		}
	}
	if err := db.Exec("CREATE VIEW fresh_widget_ids AS SELECT id FROM fresh_widgets").Error; err != nil {
		t.Fatalf("create view error = %v, want nil", err)
	}
	builder := dbschema.New(db)
	if err := builder.DropAllViews(); err != nil {
		t.Fatalf("DropAllViews() error = %v, want nil", err)
	}
	if builder.HasView("fresh_widget_ids") {
		t.Fatal("HasView(fresh_widget_ids) = true, want false")
	}
	if err := builder.DropAllTables(); err != nil {
		t.Fatalf("DropAllTables() error = %v, want nil", err)
	}
	for _, table := range []string{"fresh_widgets", "fresh_posts", "fresh_comments"} {
		if builder.HasTable(table) {
			t.Fatalf("HasTable(%s) = true, want false", table)
		}
	}
	if err := builder.DropAllTypes(); err != nil {
		t.Fatalf("DropAllTypes() error = %v, want nil", err)
	}
}

func TestExtensionDropAllTablesAcceptsEmptyDatabase(t *testing.T) {
	if err := dbschema.New(openSQLiteTestDB(t)).DropAllTables(); err != nil {
		t.Fatalf("DropAllTables() error = %v, want nil for empty database", err)
	}
}

func TestExtensionHasColumns(t *testing.T) {
	db := openSQLiteTestDB(t)
	if err := db.Exec("CREATE TABLE test_users (id integer primary key, name text, email text, age integer)").Error; err != nil {
		t.Fatalf("create test_users error = %v, want nil", err)
	}
	builder := dbschema.New(db)
	tests := []struct {
		name    string
		table   string
		columns []string
		want    bool
	}{
		{name: "all exist", table: "test_users", columns: []string{"id", "name", "email"}, want: true},
		{name: "some missing", table: "test_users", columns: []string{"id", "name", "phone"}, want: false},
		{name: "empty list", table: "test_users", columns: []string{}, want: true},
		{name: "table missing", table: "non_existent_table", columns: []string{"id"}, want: false},
		{name: "single exists", table: "test_users", columns: []string{"email"}, want: true},
		{name: "single missing", table: "test_users", columns: []string{"phone"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if actual := builder.HasColumns(tt.table, tt.columns); actual != tt.want {
				t.Fatalf("HasColumns(%q, %#v) = %v, want %v", tt.table, tt.columns, actual, tt.want)
			}
		})
	}
}

func TestExtensionHasIndexByNameColumnsAndType(t *testing.T) {
	db := openSQLiteTestDB(t)
	statements := []string{
		"CREATE TABLE test_table (id integer primary key, name text, email text)",
		"CREATE INDEX idx_name ON test_table(name)",
		"CREATE UNIQUE INDEX idx_email ON test_table(email)",
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("Exec(%q) error = %v, want nil", statement, err)
		}
	}
	builder := dbschema.New(db)
	tests := []struct {
		name      string
		index     any
		indexType []string
		want      bool
	}{
		{name: "name", index: "idx_name", want: true},
		{name: "unique name and type", index: "idx_email", indexType: []string{"unique"}, want: true},
		{name: "non-unique mismatch", index: "idx_name", indexType: []string{"unique"}, want: false},
		{name: "unique mismatch", index: "idx_email", indexType: []string{"index"}, want: false},
		{name: "columns", index: []string{"name"}, want: true},
		{name: "unique columns and type", index: []string{"email"}, indexType: []string{"unique"}, want: true},
		{name: "columns type mismatch", index: []string{"name"}, indexType: []string{"unique"}, want: false},
		{name: "missing", index: "idx_nonexistent", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if actual := builder.HasIndex("test_table", tt.index, tt.indexType...); actual != tt.want {
				t.Fatalf("HasIndex(%#v, %#v) = %v, want %v", tt.index, tt.indexType, actual, tt.want)
			}
		})
	}
}

func TestExtensionSupportsMigrateFresh(t *testing.T) {
	db := openSQLiteTestDB(t)
	if err := db.Exec("CREATE TABLE stale_widgets (id integer primary key)").Error; err != nil {
		t.Fatalf("create stale table error = %v, want nil", err)
	}
	if err := db.Exec("CREATE VIEW stale_widget_ids AS SELECT id FROM stale_widgets").Error; err != nil {
		t.Fatalf("create stale view error = %v, want nil", err)
	}
	registry := container.NewContainer()
	if err := registry.Instance("database.default", db); err != nil {
		t.Fatalf("bind database.default error = %v, want nil", err)
	}
	if err := registry.Instance("config.default", config.New()); err != nil {
		t.Fatalf("bind config.default error = %v, want nil", err)
	}
	container.SetProvider(func() *container.Container { return registry })
	t.Cleanup(func() { container.SetProvider(nil) })
	t.Setenv("APP_ENV", "local")

	dir := t.TempDir()
	name := "202609110001_create_fresh_widgets"
	if err := os.WriteFile(filepath.Join(dir, name+".go"), []byte("package migrations"), 0o644); err != nil {
		t.Fatalf("write migration file error = %v, want nil", err)
	}
	database.RegisterMigrationAs(name, func(tx *gorm.DB) error {
		return tx.Exec("CREATE TABLE fresh_widgets (id integer primary key)").Error
	}, func(tx *gorm.DB) error {
		return tx.Exec("DROP TABLE IF EXISTS fresh_widgets").Error
	})
	seeded := false
	seeder := "SQLiteFreshSeeder"
	database.RegisterSeederAs(seeder, func(*gorm.DB) error {
		seeded = true
		return nil
	})
	command := cmd.NewMigrateFreshCommand(cmd.MigrationDependencies{
		MigrationPaths: func() []string { return []string{dir} },
		SeedPaths:      func() []string { return []string{dir} },
	})
	input := migrationInput{
		options: map[string]string{"seeder": seeder},
		bools:   map[string]bool{"drop-views": true, "drop-types": true, "seed": true},
	}
	commandContext := console.NewCommandContext(
		context.Background(),
		command,
		*command.Definition(),
		input,
		console.NewIO(strings.NewReader(""), io.Discard, io.Discard),
		nil,
		&cobra.Command{Use: "migrate:fresh"},
	)
	if err := command.Handle(commandContext); err != nil {
		t.Fatalf("migrate:fresh Handle() error = %v, want nil", err)
	}
	builder := dbschema.New(db)
	if builder.HasTable("stale_widgets") || builder.HasView("stale_widget_ids") {
		t.Fatal("migrate:fresh left stale SQLite objects behind")
	}
	if !builder.HasTable("fresh_widgets") {
		t.Fatal("HasTable(fresh_widgets) = false, want true after migrate:fresh")
	}
	if !seeded {
		t.Fatal("migrate:fresh did not run the selected seeder")
	}
}

func TestExtensionSupportsInstallStatusMigrateAndRollback(t *testing.T) {
	db := openSQLiteTestDB(t)
	registry := container.NewContainer()
	if err := registry.Instance("database.default", db); err != nil {
		t.Fatalf("bind database.default error = %v, want nil", err)
	}
	if err := registry.Instance("config.default", config.New()); err != nil {
		t.Fatalf("bind config.default error = %v, want nil", err)
	}
	container.SetProvider(func() *container.Container { return registry })
	t.Cleanup(func() { container.SetProvider(nil) })
	t.Setenv("APP_ENV", "local")

	dir := t.TempDir()
	name := "202609110002_create_command_widgets"
	if err := os.WriteFile(filepath.Join(dir, name+".go"), []byte("package migrations"), 0o644); err != nil {
		t.Fatalf("write migration file error = %v, want nil", err)
	}
	database.RegisterMigrationAs(name, func(tx *gorm.DB) error {
		return tx.Exec("CREATE TABLE command_widgets (id integer primary key)").Error
	}, func(tx *gorm.DB) error {
		return tx.Exec("DROP TABLE IF EXISTS command_widgets").Error
	})
	deps := cmd.MigrationDependencies{MigrationPaths: func() []string { return []string{dir} }}
	commands := []console.Command{
		cmd.NewMigrateInstallCommand(deps),
		cmd.NewMigrateStatusCommand(deps),
		cmd.NewMigrateCommand(deps),
		cmd.NewMigrateStatusCommand(deps),
		cmd.NewMigrateRollbackCommand(deps),
	}
	for _, command := range commands {
		if err := runMigrationCommand(command, migrationInput{}); err != nil {
			t.Fatalf("%s Handle() error = %v, want nil", command.Definition().Name, err)
		}
	}
	if dbschema.New(db).HasTable("command_widgets") {
		t.Fatal("HasTable(command_widgets) = true, want false after rollback")
	}
}

func TestExtensionSupportsRefreshResetAndSeed(t *testing.T) {
	db := openSQLiteTestDB(t)
	registry := container.NewContainer()
	if err := registry.Instance("database.default", db); err != nil {
		t.Fatalf("bind database.default error = %v, want nil", err)
	}
	if err := registry.Instance("config.default", config.New()); err != nil {
		t.Fatalf("bind config.default error = %v, want nil", err)
	}
	container.SetProvider(func() *container.Container { return registry })
	t.Cleanup(func() { container.SetProvider(nil) })
	t.Setenv("APP_ENV", "local")

	dir := t.TempDir()
	name := "202609110003_create_refresh_widgets"
	if err := os.WriteFile(filepath.Join(dir, name+".go"), []byte("package migrations"), 0o644); err != nil {
		t.Fatalf("write migration file error = %v, want nil", err)
	}
	database.RegisterMigrationAs(name, func(tx *gorm.DB) error {
		return tx.Exec("CREATE TABLE IF NOT EXISTS refresh_widgets (id integer primary key)").Error
	}, func(tx *gorm.DB) error {
		return tx.Exec("DROP TABLE IF EXISTS refresh_widgets").Error
	})
	seedCalls := 0
	seeder := "SQLiteRefreshSeeder"
	database.RegisterSeederAs(seeder, func(*gorm.DB) error {
		seedCalls++
		return nil
	})
	deps := cmd.MigrationDependencies{
		MigrationPaths: func() []string { return []string{dir} },
		SeedPaths:      func() []string { return []string{dir} },
	}
	if err := runMigrationCommand(cmd.NewMigrateCommand(deps), migrationInput{}); err != nil {
		t.Fatalf("migrate Handle() error = %v, want nil", err)
	}
	seedInput := migrationInput{options: map[string]string{"seeder": seeder}, bools: map[string]bool{"seed": true}}
	if err := runMigrationCommand(cmd.NewMigrateRefreshCommand(deps), seedInput); err != nil {
		t.Fatalf("migrate:refresh Handle() error = %v, want nil", err)
	}
	if !dbschema.New(db).HasTable("refresh_widgets") || seedCalls != 1 {
		t.Fatalf("refresh result = (table %v, seed calls %d), want (true, 1)", dbschema.New(db).HasTable("refresh_widgets"), seedCalls)
	}
	if err := runMigrationCommand(cmd.NewMigrateResetCommand(deps), migrationInput{}); err != nil {
		t.Fatalf("migrate:reset Handle() error = %v, want nil", err)
	}
	if dbschema.New(db).HasTable("refresh_widgets") {
		t.Fatal("HasTable(refresh_widgets) = true, want false after reset")
	}
	if err := runMigrationCommand(cmd.NewDBSeedCommand(deps), migrationInput{options: map[string]string{"class": seeder}}); err != nil {
		t.Fatalf("db:seed Handle() error = %v, want nil", err)
	}
	if seedCalls != 2 {
		t.Fatalf("seed calls = %d, want 2 after refresh and db:seed", seedCalls)
	}
}

func TestExtensionMigrationPretendDoesNotExecuteOrPersist(t *testing.T) {
	db := openSQLiteTestDB(t)
	bindMigrationTestDatabase(t, db)
	dir := t.TempDir()
	name := "202609110005_pretend_sqlite"
	if err := os.WriteFile(filepath.Join(dir, name+".go"), []byte("package migrations"), 0o644); err != nil {
		t.Fatalf("write migration file: %v", err)
	}
	called := false
	database.RegisterMigrationAs(name, func(*gorm.DB) error {
		called = true
		return nil
	}, func(*gorm.DB) error { return nil })

	command := cmd.NewMigrateCommand(cmd.MigrationDependencies{MigrationPaths: func() []string { return []string{dir} }})
	if err := runMigrationCommand(command, migrationInput{bools: map[string]bool{"pretend": true}}); err != nil {
		t.Fatalf("migrate --pretend error = %v, want nil", err)
	}
	if called {
		t.Fatal("migrate --pretend executed the migration handler")
	}
	if dbschema.New(db).HasTable("migrations") {
		var count int64
		if err := db.Table("migrations").Count(&count).Error; err != nil {
			t.Fatalf("count migration records: %v", err)
		}
		if count != 0 {
			t.Fatalf("migrate --pretend persisted %d records, want 0", count)
		}
	}
}

func TestExtensionMigrationStepUsesDistinctBatches(t *testing.T) {
	db := openSQLiteTestDB(t)
	bindMigrationTestDatabase(t, db)
	dir := t.TempDir()
	names := []string{"202609110006_step_a", "202609110007_step_b"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name+".go"), []byte("package migrations"), 0o644); err != nil {
			t.Fatalf("write migration file %s: %v", name, err)
		}
		migrationName := name
		database.RegisterMigrationAs(migrationName, func(*gorm.DB) error { return nil }, func(*gorm.DB) error { return nil })
	}

	command := cmd.NewMigrateCommand(cmd.MigrationDependencies{MigrationPaths: func() []string { return []string{dir} }})
	if err := runMigrationCommand(command, migrationInput{bools: map[string]bool{"step": true}}); err != nil {
		t.Fatalf("migrate --step error = %v, want nil", err)
	}
	var records []struct {
		Migration string
		Batch     int
	}
	if err := db.Table("migrations").Where("migration IN ?", names).Order("migration").Scan(&records).Error; err != nil {
		t.Fatalf("load step migration records: %v", err)
	}
	if len(records) != 2 || records[0].Batch == records[1].Batch {
		t.Fatalf("step migration batches = %#v, want two distinct batches", records)
	}
}

func TestExtensionStatusIncludesMissingAppliedMigration(t *testing.T) {
	db := openSQLiteTestDB(t)
	bindMigrationTestDatabase(t, db)
	if err := runMigrationCommand(cmd.NewMigrateInstallCommand(), migrationInput{}); err != nil {
		t.Fatalf("migrate:install error = %v, want nil", err)
	}
	dir := t.TempDir()
	existing := "202609110008_existing_status"
	missing := "202609110009_missing_status"
	if err := os.WriteFile(filepath.Join(dir, existing+".go"), []byte("package migrations"), 0o644); err != nil {
		t.Fatalf("write existing migration source: %v", err)
	}
	if err := db.Exec("INSERT INTO migrations (migration, batch) VALUES (?, ?), (?, ?)", existing, 1, missing, 2).Error; err != nil {
		t.Fatalf("insert migration status records: %v", err)
	}
	command := cmd.NewMigrateStatusCommand(cmd.MigrationDependencies{MigrationPaths: func() []string { return []string{dir} }})
	output, err := runMigrationCommandWithOutput(command, migrationInput{})
	if err != nil {
		t.Fatalf("migrate:status error = %v, want nil", err)
	}
	if !strings.Contains(output, existing) || !strings.Contains(output, missing+" [missing]") {
		t.Fatalf("migrate:status output = %q, want existing and missing rows", output)
	}
}

func TestExtensionRefreshStepAndSeed(t *testing.T) {
	db := openSQLiteTestDB(t)
	bindMigrationTestDatabase(t, db)
	dir := t.TempDir()
	names := []string{"202609110010_refresh_step_a", "202609110011_refresh_step_b"}
	upCalls := make([]int, len(names))
	downCalls := make([]int, len(names))
	for index, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name+".go"), []byte("package migrations"), 0o644); err != nil {
			t.Fatalf("write migration source %s: %v", name, err)
		}
		i := index
		database.RegisterMigrationAs(name, func(*gorm.DB) error {
			upCalls[i]++
			return nil
		}, func(*gorm.DB) error {
			downCalls[i]++
			return nil
		})
	}
	seeder := "SQLiteRefreshStepSeeder"
	seedCalls := 0
	database.RegisterSeederAs(seeder, func(*gorm.DB) error {
		seedCalls++
		return nil
	})
	deps := cmd.MigrationDependencies{
		MigrationPaths: func() []string { return []string{dir} },
		SeedPaths:      func() []string { return []string{dir} },
	}
	if err := runMigrationCommand(cmd.NewMigrateCommand(deps), migrationInput{}); err != nil {
		t.Fatalf("initial migrate error = %v, want nil", err)
	}
	input := migrationInput{options: map[string]string{"step": "1", "seeder": seeder}, bools: map[string]bool{"seed": true}}
	if err := runMigrationCommand(cmd.NewMigrateRefreshCommand(deps), input); err != nil {
		t.Fatalf("migrate:refresh --step=1 --seed error = %v, want nil", err)
	}
	if downCalls[0] != 0 || downCalls[1] != 1 || upCalls[0] != 1 || upCalls[1] != 2 || seedCalls != 1 {
		t.Fatalf("refresh calls = up %#v down %#v seed %d, want up [1 2], down [0 1], seed 1", upCalls, downCalls, seedCalls)
	}
}

func TestExtensionTracksApplyAndRollbackInTransactions(t *testing.T) {
	db := openSQLiteTestDB(t)
	bindMigrationTestDatabase(t, db)
	dir := t.TempDir()
	name := "202609110012_track_transaction"
	if err := os.WriteFile(filepath.Join(dir, name+".go"), []byte("package migrations"), 0o644); err != nil {
		t.Fatalf("write migration source: %v", err)
	}
	upTransaction := false
	downTransaction := false
	database.RegisterMigrationAs(name, func(tx *gorm.DB) error {
		_, upTransaction = tx.Statement.ConnPool.(*sql.Tx)
		return nil
	}, func(tx *gorm.DB) error {
		_, downTransaction = tx.Statement.ConnPool.(*sql.Tx)
		return nil
	})
	deps := cmd.MigrationDependencies{MigrationPaths: func() []string { return []string{dir} }}
	if err := runMigrationCommand(cmd.NewMigrateCommand(deps), migrationInput{}); err != nil {
		t.Fatalf("migrate error = %v, want nil", err)
	}
	var applied int64
	if err := db.Table("migrations").Where("migration = ?", name).Count(&applied).Error; err != nil {
		t.Fatalf("count applied record: %v", err)
	}
	if !upTransaction || applied != 1 {
		t.Fatalf("migrate result = (transaction %v, records %d), want (true, 1)", upTransaction, applied)
	}
	if err := runMigrationCommand(cmd.NewMigrateRollbackCommand(deps), migrationInput{}); err != nil {
		t.Fatalf("migrate:rollback error = %v, want nil", err)
	}
	if err := db.Table("migrations").Where("migration = ?", name).Count(&applied).Error; err != nil {
		t.Fatalf("count rolled-back record: %v", err)
	}
	if !downTransaction || applied != 0 {
		t.Fatalf("rollback result = (transaction %v, records %d), want (true, 0)", downTransaction, applied)
	}
}

func TestExtensionRollbackSelectsExplicitOrLatestBatch(t *testing.T) {
	tests := []struct {
		name    string
		options map[string]string
	}{
		{name: "explicit batch", options: map[string]string{"batch": "2"}},
		{name: "latest batch", options: nil},
	}
	for caseIndex, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openSQLiteTestDB(t)
			bindMigrationTestDatabase(t, db)
			if err := runMigrationCommand(cmd.NewMigrateInstallCommand(), migrationInput{}); err != nil {
				t.Fatalf("migrate:install error = %v, want nil", err)
			}
			dir := t.TempDir()
			names := []string{
				fmt.Sprintf("20260911002%d_batch_one", caseIndex),
				fmt.Sprintf("20260911003%d_batch_two_a", caseIndex),
				fmt.Sprintf("20260911004%d_batch_two_b", caseIndex),
			}
			downCalls := make([]int, len(names))
			for index, name := range names {
				if err := os.WriteFile(filepath.Join(dir, name+".go"), []byte("package migrations"), 0o644); err != nil {
					t.Fatalf("write migration source %s: %v", name, err)
				}
				i := index
				database.RegisterMigrationAs(name, func(*gorm.DB) error { return nil }, func(*gorm.DB) error {
					downCalls[i]++
					return nil
				})
				batch := 2
				if index == 0 {
					batch = 1
				}
				if err := db.Exec("INSERT INTO migrations (migration, batch) VALUES (?, ?)", name, batch).Error; err != nil {
					t.Fatalf("insert migration %s: %v", name, err)
				}
			}
			deps := cmd.MigrationDependencies{MigrationPaths: func() []string { return []string{dir} }}
			if err := runMigrationCommand(cmd.NewMigrateRollbackCommand(deps), migrationInput{options: tt.options}); err != nil {
				t.Fatalf("migrate:rollback error = %v, want nil", err)
			}
			if downCalls[0] != 0 || downCalls[1] != 1 || downCalls[2] != 1 {
				t.Fatalf("rollback down calls = %#v, want [0 1 1]", downCalls)
			}
			var remaining []string
			if err := db.Table("migrations").Order("migration").Pluck("migration", &remaining).Error; err != nil {
				t.Fatalf("load remaining migrations: %v", err)
			}
			if len(remaining) != 1 || remaining[0] != names[0] {
				t.Fatalf("remaining migrations = %#v, want %#v", remaining, []string{names[0]})
			}
		})
	}
}

func TestExtensionMigrationCommandsRejectMissingAppliedSource(t *testing.T) {
	commands := []struct {
		name string
		new  func(cmd.MigrationDependencies) console.Command
	}{
		{name: "rollback", new: func(deps cmd.MigrationDependencies) console.Command { return cmd.NewMigrateRollbackCommand(deps) }},
		{name: "refresh", new: func(deps cmd.MigrationDependencies) console.Command { return cmd.NewMigrateRefreshCommand(deps) }},
		{name: "reset", new: func(deps cmd.MigrationDependencies) console.Command { return cmd.NewMigrateResetCommand(deps) }},
	}
	for index, tt := range commands {
		t.Run(tt.name, func(t *testing.T) {
			db := openSQLiteTestDB(t)
			bindMigrationTestDatabase(t, db)
			if err := runMigrationCommand(cmd.NewMigrateInstallCommand(), migrationInput{}); err != nil {
				t.Fatalf("migrate:install error = %v, want nil", err)
			}
			name := fmt.Sprintf("20260911001%d_missing_%s", index, tt.name)
			if err := db.Exec("INSERT INTO migrations (migration, batch) VALUES (?, ?)", name, 1).Error; err != nil {
				t.Fatalf("insert applied migration: %v", err)
			}
			deps := cmd.MigrationDependencies{MigrationPaths: func() []string { return []string{t.TempDir()} }}
			err := runMigrationCommand(tt.new(deps), migrationInput{})
			if err == nil || !strings.Contains(err.Error(), "source file is missing") {
				t.Fatalf("%s missing-source error = %v, want source file is missing", tt.name, err)
			}
		})
	}
}

func TestExtensionMigrationCommandsAcceptEmptyRepositoryAndMissingTable(t *testing.T) {
	commands := []struct {
		name string
		new  func(cmd.MigrationDependencies) console.Command
	}{
		{name: "status", new: func(deps cmd.MigrationDependencies) console.Command { return cmd.NewMigrateStatusCommand(deps) }},
		{name: "rollback", new: func(deps cmd.MigrationDependencies) console.Command { return cmd.NewMigrateRollbackCommand(deps) }},
		{name: "reset", new: func(deps cmd.MigrationDependencies) console.Command { return cmd.NewMigrateResetCommand(deps) }},
	}
	for _, tt := range commands {
		t.Run(tt.name, func(t *testing.T) {
			db := openSQLiteTestDB(t)
			bindMigrationTestDatabase(t, db)
			deps := cmd.MigrationDependencies{MigrationPaths: func() []string { return []string{t.TempDir()} }}
			command := tt.new(deps)
			if err := runMigrationCommand(command, migrationInput{}); err != nil {
				t.Fatalf("%s on missing migrations table error = %v, want nil", tt.name, err)
			}
		})
	}
}

func TestExtensionMigrationCommandsPreserveSeederErrors(t *testing.T) {
	db := openSQLiteTestDB(t)
	registry := container.NewContainer()
	if err := registry.Instance("database.default", db); err != nil {
		t.Fatalf("bind database.default error = %v, want nil", err)
	}
	if err := registry.Instance("config.default", config.New()); err != nil {
		t.Fatalf("bind config.default error = %v, want nil", err)
	}
	container.SetProvider(func() *container.Container { return registry })
	t.Cleanup(func() { container.SetProvider(nil) })
	t.Setenv("APP_ENV", "local")

	dir := t.TempDir()
	name := "202609110004_create_seed_error_widgets"
	if err := os.WriteFile(filepath.Join(dir, name+".go"), []byte("package migrations"), 0o644); err != nil {
		t.Fatalf("write migration file error = %v, want nil", err)
	}
	database.RegisterMigrationAs(name, func(tx *gorm.DB) error {
		return tx.Exec("CREATE TABLE seed_error_widgets (id integer primary key)").Error
	}, func(tx *gorm.DB) error {
		return tx.Exec("DROP TABLE IF EXISTS seed_error_widgets").Error
	})
	missingSeedPath := filepath.Join(dir, "missing-seeders")
	deps := cmd.MigrationDependencies{
		MigrationPaths: func() []string { return []string{dir} },
		SeedPaths:      func() []string { return []string{missingSeedPath} },
	}
	if err := runMigrationCommand(cmd.NewMigrateCommand(deps), migrationInput{bools: map[string]bool{"seed": true}}); err == nil {
		t.Fatal("migrate --seed error = nil, want invalid seed path error")
	}
	seedDeps := cmd.MigrationDependencies{SeedPaths: func() []string { return []string{dir} }}
	input := migrationInput{options: map[string]string{"class": "MissingSQLiteSeeder"}}
	if err := runMigrationCommand(cmd.NewDBSeedCommand(seedDeps), input); err == nil {
		t.Fatal("db:seed error = nil, want missing seeder error")
	}
}

func runMigrationCommand(command console.Command, input migrationInput) error {
	_, err := runMigrationCommandWithOutput(command, input)
	return err
}

func runMigrationCommandWithOutput(command console.Command, input migrationInput) (string, error) {
	var output bytes.Buffer
	ctx := console.NewCommandContext(
		context.Background(),
		command,
		*command.Definition(),
		input,
		console.NewIO(strings.NewReader(""), &output, &output),
		nil,
		&cobra.Command{Use: command.Definition().Name},
	)
	err := command.Handle(ctx)
	return output.String(), err
}

func bindMigrationTestDatabase(t *testing.T, db *gorm.DB) {
	t.Helper()
	registry := container.NewContainer()
	if err := registry.Instance("database.default", db); err != nil {
		t.Fatalf("bind database.default: %v", err)
	}
	if err := registry.Instance("config.default", config.New()); err != nil {
		t.Fatalf("bind config.default: %v", err)
	}
	container.SetProvider(func() *container.Container { return registry })
	t.Cleanup(func() { container.SetProvider(nil) })
	t.Setenv("APP_ENV", "local")
}

type migrationInput struct {
	options map[string]string
	bools   map[string]bool
}

func (i migrationInput) Argument(string) string        { return "" }
func (i migrationInput) Arguments(string) []string     { return nil }
func (i migrationInput) Option(name string) string     { return i.options[name] }
func (i migrationInput) OptionStrings(string) []string { return nil }
func (i migrationInput) OptionBool(name string) bool   { return i.bools[name] }
func (i migrationInput) OptionInt(string) (int, error) { return 0, nil }
func (i migrationInput) HasOption(name string) bool    { return i.options[name] != "" || i.bools[name] }

func openSQLiteTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v, want nil", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB() error = %v, want nil", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func hasTable(tables []dbschema.TableInfo, name string) bool {
	for _, table := range tables {
		if table.Name == name {
			return true
		}
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasIndex(indexes []dbschema.IndexInfo, name string, columns []string) bool {
	for _, index := range indexes {
		nameMatches := index.Name == name || name == "PRIMARY" && index.Primary
		if !nameMatches || len(index.Columns) != len(columns) {
			continue
		}
		matched := true
		for i := range columns {
			if index.Columns[i] != columns[i] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func TestInlineIndexSQLReturnsEmptyForUnknownKind(t *testing.T) {
	actual := inlineIndexSQL(dbschema.BlueprintIndex{Kind: "plain"})
	if actual != "" {
		t.Fatalf("inlineIndexSQL() = %q, want empty SQL for unknown index kind", actual)
	}
}

type extensionWidget struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

type syncBaseWidget struct {
	ID uint
}

func (syncBaseWidget) TableName() string { return "sync_widgets" }

type syncExpandedWidget struct {
	ID   uint
	Code string
}

func (syncExpandedWidget) TableName() string { return "sync_widgets" }
