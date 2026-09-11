// Package sqlite provides the optional SQLite database adapter for PrismGo.
package sqlite

import (
	gormsqlite "github.com/glebarez/sqlite"
	providercontract "github.com/prismgo/framework/contracts/provider"
	"github.com/prismgo/framework/database"
	"gorm.io/gorm"
)

// ServiceProvider installs SQLite dialectors on an application's database manager.
type ServiceProvider struct{}

// Open creates a SQLite GORM dialector without opening a database connection.
func Open(dsn string) gorm.Dialector { return gormsqlite.Open(dsn) }

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
