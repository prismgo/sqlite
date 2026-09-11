package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/prismgo/framework/database"
	dbschema "github.com/prismgo/framework/database/schema"
	"github.com/prismgo/framework/foundation"
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

type extensionWidget struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}
