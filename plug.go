package pgsql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/go-lynx/lynx"
	"github.com/go-lynx/lynx-sql-sdk/base"
	"github.com/go-lynx/lynx-sql-sdk/interfaces"
	"github.com/go-lynx/lynx/pkg/factory"
	"github.com/go-lynx/lynx/plugins"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// waitForDBInitialInterval is the initial polling interval when waiting for DB readiness.
	waitForDBInitialInterval = 10 * time.Millisecond
	// waitForDBMaxInterval caps the polling interval when waiting for DB readiness.
	waitForDBMaxInterval = 200 * time.Millisecond
)

type dbProvider struct{}

// DBProvider resolves the current pool on each call so callers do not cache a stale *sql.DB across reconnects.
type DBProvider interface {
	DB(ctx context.Context) (*sql.DB, error)
	ValidatedConn(ctx context.Context) (*sql.Conn, error)
	Dialect() string
}

// init function registers the PostgreSQL client plugin to the global plugin factory
func init() {
	factory.GlobalTypedFactory().RegisterPlugin(pluginName, confPrefix, func() plugins.Plugin {
		return NewPgsqlClient()
	})
}

// GetDB gets the database connection from the PostgreSQL plugin.
// Lynx must be initialized and the pgsql plugin started before calling this.
// The pool is validated (ping) before return when ensure_alive_before_handout is enabled (default);
// on failure the plugin reconnects so the pool is not handed out when broken.
//
// When auto-reconnect is enabled, do not cache the returned *sql.DB in long-lived structs (e.g. Data/Repo
// built at startup). After a reconnection the previous *sql.DB is closed; cached references will get
// "sql: database is closed". Call GetDB/GetDBWithContext at the start of each request and use the result
// only for that request, or inject a provider (e.g. GetDBWithContext) and call it when needed.
func GetDB() (*sql.DB, error) {
	return GetDBWithContext(context.Background())
}

// GetDBWithContext gets the database connection with context support (timeout/cancellation).
func GetDBWithContext(ctx context.Context) (*sql.DB, error) {
	if lynx.Lynx() == nil {
		return nil, fmt.Errorf("lynx not initialized")
	}
	plugin := lynx.Lynx().GetPluginManager().GetPlugin(pluginName)
	if plugin == nil {
		return nil, fmt.Errorf("plugin %s not found", pluginName)
	}
	if sqlPlugin, ok := plugin.(interfaces.SQLPlugin); ok {
		return sqlPlugin.GetDBWithContext(ctx)
	}
	return nil, fmt.Errorf("plugin %s is not a SQLPlugin", pluginName)
}

// GetProvider returns a stable provider for the current pgsql pool.
// The provider does not hold a concrete *sql.DB; each call resolves the current pool so it remains valid after reconnect.
func GetProvider() DBProvider {
	return dbProvider{}
}

// GetValidatedConn returns a single connection from the pool that has been verified alive (Ping).
// Use this when you need a connection guaranteed to be usable at handoff time. Caller must call conn.Close() when done.
func GetValidatedConn(ctx context.Context) (*sql.Conn, error) {
	db, err := GetDBWithContext(ctx)
	if err != nil {
		return nil, err
	}
	if db == nil {
		return nil, fmt.Errorf("database connection is nil")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (dbProvider) DB(ctx context.Context) (*sql.DB, error) {
	return GetDBWithContext(ctx)
}

func (dbProvider) ValidatedConn(ctx context.Context) (*sql.Conn, error) {
	return GetValidatedConn(ctx)
}

func (dbProvider) Dialect() string {
	return GetDialect()
}

// WaitForDB blocks until the pgsql plugin has provided a valid *sql.DB (i.e. Lynx initialized,
// plugin registered and StartupTasks has run so that GetDB() succeeds). Use this when your code
// may run before or during plugin startup. The context controls timeout and cancellation.
//
// Extreme cases handled:
//   - Plugin in manager but StartupTasks not yet run: keeps polling until GetDB() returns (db, nil) with db != nil.
//   - StartupTasks failed (e.g. DB down): returns once the pool exists (GetDB() succeeds); use db.PingContext(ctx) or
//     CheckHealth() if you need to ensure the connection is actually up.
//   - Lynx shutdown during wait: cancel the context from your shutdown path so WaitForDB returns with context error.
func WaitForDB(ctx context.Context) (*sql.DB, error) {
	interval := waitForDBInitialInterval
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("wait for pgsql plugin: %w", err)
		}
		app := lynx.Lynx()
		if app == nil {
			sleepOrDone(ctx, interval)
			interval = backoff(interval)
			continue
		}
		plugin := app.GetPluginManager().GetPlugin(pluginName)
		if plugin == nil {
			sleepOrDone(ctx, interval)
			interval = backoff(interval)
			continue
		}
		sqlPlugin, ok := plugin.(interfaces.SQLPlugin)
		if !ok {
			return nil, fmt.Errorf("plugin %s is not a SQLPlugin", pluginName)
		}
		db, err := sqlPlugin.GetDB()
		if err != nil || db == nil {
			// Plugin exists but DB not ready yet (e.g. StartupTasks not run or failed to open)
			sleepOrDone(ctx, interval)
			interval = backoff(interval)
			continue
		}
		return db, nil
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) {
	if ctx.Err() != nil {
		return
	}
	timer := time.NewTimer(d)
	select {
	case <-ctx.Done():
		timer.Stop()
	case <-timer.C:
	}
}

func backoff(interval time.Duration) time.Duration {
	next := interval + interval/2
	if next > waitForDBMaxInterval {
		return waitForDBMaxInterval
	}
	return next
}

// WaitForDBConnected blocks until WaitForDB succeeds and the plugin reports IsConnected() (i.e. health check has passed).
// Use this when you need to ensure the database is reachable before proceeding, not just that the pool exists.
// If the DB is down, this will block until ctx expires; set a reasonable timeout (e.g. 30s) on the context.
func WaitForDBConnected(ctx context.Context) (*sql.DB, error) {
	db, err := WaitForDB(ctx)
	if err != nil {
		return nil, err
	}
	interval := waitForDBInitialInterval
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("wait for pgsql connected: %w", err)
		}
		if IsConnected() {
			return db, nil
		}
		sleepOrDone(ctx, interval)
		interval = backoff(interval)
	}
}

// GetDialect gets the database dialect
func GetDialect() string {
	if lynx.Lynx() == nil {
		return ""
	}
	plugin := lynx.Lynx().GetPluginManager().GetPlugin(pluginName)
	if plugin == nil {
		return ""
	}
	if sqlPlugin, ok := plugin.(interfaces.SQLPlugin); ok {
		return sqlPlugin.GetDialect()
	}
	return ""
}

// IsConnected checks if the database is connected
func IsConnected() bool {
	if lynx.Lynx() == nil {
		return false
	}
	plugin := lynx.Lynx().GetPluginManager().GetPlugin(pluginName)
	if plugin == nil {
		return false
	}
	if sqlPlugin, ok := plugin.(interfaces.SQLPlugin); ok {
		return sqlPlugin.IsConnected()
	}
	return false
}

// CheckHealth performs health check
func CheckHealth() error {
	if lynx.Lynx() == nil {
		return fmt.Errorf("lynx not initialized")
	}
	plugin := lynx.Lynx().GetPluginManager().GetPlugin(pluginName)
	if plugin == nil {
		return fmt.Errorf("plugin %s not found", pluginName)
	}
	if sqlPlugin, ok := plugin.(interfaces.SQLPlugin); ok {
		return sqlPlugin.CheckHealth()
	}
	return fmt.Errorf("plugin %s is not a SQLPlugin", pluginName)
}

// GetDriver gets the ent SQL driver from the PostgreSQL plugin.
// Returns an error if the database connection cannot be obtained.
// When auto-reconnect is enabled, do not cache the returned driver (or an ent.Client built from it) in
// long-lived structs; after Reconnect() the underlying *sql.DB is closed. Obtain the driver at request
// scope (e.g. call GetDriver in each handler and create ent.Client per request) or use GetDriverProvider.
func GetDriver() (*entsql.Driver, error) {
	db, err := GetDB()
	if err != nil {
		return nil, fmt.Errorf("failed to get database connection: %w", err)
	}
	if db == nil {
		return nil, fmt.Errorf("database connection is nil")
	}
	return entsql.OpenDB(GetDialect(), db), nil
}

// GetDriverProvider returns a stable provider for ent SQL drivers.
// The returned closure resolves the current pool on each call and should be preferred over caching GetDriver().
func GetDriverProvider() func(ctx context.Context) (*entsql.Driver, error) {
	provider := GetProvider()
	return func(ctx context.Context) (*entsql.Driver, error) {
		db, err := provider.DB(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get database connection: %w", err)
		}
		if db == nil {
			return nil, fmt.Errorf("database connection is nil")
		}
		return entsql.OpenDB(provider.Dialect(), db), nil
	}
}

// WaitForDriver blocks until the pgsql plugin is available (see WaitForDB), then returns GetDriver().
// Use this when you need the ent driver but your code may run before plugin startup.
func WaitForDriver(ctx context.Context) (*entsql.Driver, error) {
	db, err := WaitForDB(ctx)
	if err != nil {
		return nil, fmt.Errorf("wait for driver: %w", err)
	}
	if db == nil {
		return nil, fmt.Errorf("database connection is nil")
	}
	return entsql.OpenDB(GetDialect(), db), nil
}

// GetStats returns connection pool statistics from the pgsql plugin, or nil if the plugin is not loaded.
func GetStats() *base.ConnectionPoolStats {
	if lynx.Lynx() == nil {
		return nil
	}
	plugin := lynx.Lynx().GetPluginManager().GetPlugin(pluginName)
	if plugin == nil {
		return nil
	}
	if client, ok := plugin.(*DBPgsqlClient); ok {
		return client.SQLPlugin.GetStats()
	}
	return nil
}

// GetConfig returns the current pgsql plugin config, or nil if the plugin is not loaded or not *DBPgsqlClient.
// Config is read-only; do not modify.
func GetConfig() *interfaces.Config {
	if lynx.Lynx() == nil {
		return nil
	}
	plugin := lynx.Lynx().GetPluginManager().GetPlugin(pluginName)
	if plugin == nil {
		return nil
	}
	if client, ok := plugin.(*DBPgsqlClient); ok {
		return client.GetConfig()
	}
	return nil
}

// GetMetricsGatherer returns the Prometheus Gatherer for the pgsql plugin, or nil if the plugin is not loaded or Prometheus is not enabled.
// Use this to merge pgsql metrics into your /metrics endpoint, e.g. with prometheus.Gatherers.
func GetMetricsGatherer() prometheus.Gatherer {
	if lynx.Lynx() == nil {
		return nil
	}
	plugin := lynx.Lynx().GetPluginManager().GetPlugin(pluginName)
	if plugin == nil {
		return nil
	}
	if client, ok := plugin.(*DBPgsqlClient); ok {
		return client.GetMetricsGatherer()
	}
	return nil
}
