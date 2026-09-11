# PrismGo SQLite Extension

`github.com/prismgo/sqlite` adds the `sqlite` and `sqlite3` database drivers to one PrismGo Application. The extension uses a pure-Go SQLite implementation and does not require an external database server.

## Installation

```bash
go get github.com/prismgo/sqlite
```

Register the extension between PrismGo's default providers and your application providers:

```go
import (
    sqliteext "github.com/prismgo/sqlite"
    "github.com/prismgo/framework/foundation"
)

app := foundation.Configure().
    WithExtensionProviders(sqliteext.ServiceProvider{}).
    WithProviders(applicationProviders...).
    Create()
```

Your regular database configuration can then select `driver: "sqlite"` and use either an in-memory DSN such as `file::memory:?cache=shared` or a database file path.

The provider registers drivers only on that Application's database manager. It does not open a connection during registration or boot, and the framework closes container-managed connections during Application shutdown.

## Schema and migrations

The extension's Dialector also supplies SQLite-specific Blueprint compilation, metadata queries, foreign-key check toggles, composite-index creation, and object cleanup for `migrate:fresh`. Use the regular framework APIs after registering the provider:

```go
db, err := database.Open("sqlite", "file::memory:?cache=shared", database.MySQLConfig{})
builder := schema.New(db)
err = builder.Create("widgets", func(table *schema.Blueprint) {
    table.Id()
    table.String("name")
})
```

The same installed extension supports file and in-memory databases through `database.Open`, `schema.New(db)` / `schema.Bind(db)`, and all migration commands. The framework core does not depend on a SQLite driver.

SQLite schema migrations must be serialized. In particular, `PRAGMA foreign_keys` and attached databases are scoped to one physical connection; applications that call constraint toggles or inspect an attached schema should use `db.Connection` and construct the Schema builder from that pinned connection. Concurrent schema changes are not supported.

## Direct dialector use

For code that uses GORM directly, `sqlite.Open(dsn)` returns a `gorm.Dialector` without opening a connection:

```go
db, err := gorm.Open(sqlite.Open("storage/database.sqlite"), &gorm.Config{})
```
