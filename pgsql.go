// Package pgsql provides a Lynx plugin for PostgreSQL using the pgx driver
// via database/sql. Configure with config key "lynx.pgsql" (source required).
package pgsql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/XSAM/otelsql"
	"github.com/go-lynx/lynx"
	"github.com/go-lynx/lynx-pgsql/conf"
	"github.com/go-lynx/lynx-sql-sdk/base"
	"github.com/go-lynx/lynx-sql-sdk/interfaces"
	"github.com/go-lynx/lynx/log"
	"github.com/go-lynx/lynx/plugins"
	"github.com/prometheus/client_golang/prometheus"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"

	// PostgreSQL driver
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Plugin metadata
const (
	pluginName        = "pgsql.client"
	pluginVersion     = "v1.6.0-beta"
	pluginDescription = "pgsql client plugin for lynx framework"
	confPrefix        = "lynx.pgsql"
	// pluginPriority is the startup order relative to other plugins (higher runs later)
	pluginPriority = 101
	// tracerPluginName is the Lynx plugin name of lynx-tracer; when present we inject otelsql for tracing.
	tracerPluginName = "tracer.server"
)

// DBPgsqlClient represents PostgreSQL client plugin instance
type DBPgsqlClient struct {
	*base.SQLPlugin
	config            *interfaces.Config
	pbConfig          *conf.Pgsql // protobuf configuration
	prometheusMetrics *PrometheusMetrics
	metricsCancel     context.CancelFunc // stop periodic pool stats update
}

// NewPgsqlClient creates a new PostgreSQL client plugin instance
func NewPgsqlClient() *DBPgsqlClient {
	config := &interfaces.Config{
		Driver: "pgx",
		// Default connection pool settings
		MaxOpenConns:    25,
		MaxIdleConns:    5,
		ConnMaxLifetime: 3600, // 1 hour
		ConnMaxIdleTime: 300,  // 5 minutes
		// Default health check settings
		HealthCheckInterval: 30, // 30 seconds
		HealthCheckQuery:    "SELECT 1",
		// Production-ready: enable retry on connection failure
		RetryEnabled:     true,
		RetryMaxAttempts: 3,
	}

	c := &DBPgsqlClient{
		config:   config,
		pbConfig: &conf.Pgsql{},
	}

	c.SQLPlugin = base.NewBaseSQLPlugin(
		plugins.GeneratePluginID("", pluginName, pluginVersion),
		pluginName,
		pluginDescription,
		pluginVersion,
		confPrefix,
		pluginPriority,
		config,
	)

	return c
}

// InitializeResources loads protobuf configuration and initializes resources.
// We set p.config from proto first; base.InitializeResources(rt) is then called and
// will Scan(p.config) again—ensure your config source can fill interfaces.Config
// (e.g. same keys as Config) so base validation passes and proto values are not lost.
func (p *DBPgsqlClient) InitializeResources(rt plugins.Runtime) error {
	pbConfig := &conf.Pgsql{}
	if err := rt.GetConfig().Value(confPrefix).Scan(pbConfig); err != nil {
		return fmt.Errorf("failed to load PostgreSQL configuration: %w", err)
	}
	p.pbConfig = pbConfig

	if pbConfig.Source == "" {
		return fmt.Errorf("postgresql source (DSN) is required")
	}

	// Apply proto to shared config before base runs (base may overwrite via Scan)
	if pbConfig.Driver != "" {
		p.config.Driver = pbConfig.Driver
	}
	p.config.DSN = pbConfig.Source
	if pbConfig.MaxConn > 0 {
		p.config.MaxOpenConns = int(pbConfig.MaxConn)
	}
	if pbConfig.MinConn > 0 {
		p.config.MaxIdleConns = int(pbConfig.MinConn)
		p.config.WarmupEnabled = true
		p.config.WarmupConns = int(pbConfig.MinConn)
	}
	if pbConfig.MaxIdleConn > 0 {
		p.config.MaxIdleConns = int(pbConfig.MaxIdleConn)
		if p.config.WarmupConns == 0 {
			p.config.WarmupEnabled = true
			p.config.WarmupConns = int(pbConfig.MaxIdleConn)
		}
	}
	if p.config.MaxIdleConns > p.config.MaxOpenConns {
		p.config.MaxIdleConns = p.config.MaxOpenConns
	}
	if p.config.WarmupConns > p.config.MaxOpenConns {
		p.config.WarmupConns = p.config.MaxOpenConns
	}
	if pbConfig.MaxLifeTime != nil {
		p.config.ConnMaxLifetime = int(pbConfig.MaxLifeTime.AsDuration().Seconds())
	}
	if pbConfig.MaxIdleTime != nil {
		p.config.ConnMaxIdleTime = int(pbConfig.MaxIdleTime.AsDuration().Seconds())
	}

	// When lynx-tracer plugin is present, open DB via otelsql so every query gets a span in the same trace.
	if lynx.Lynx() != nil && lynx.Lynx().GetPluginManager().GetPlugin(tracerPluginName) != nil {
		p.config.OpenDBFunc = func(driver, dsn string) (*sql.DB, error) {
			return otelsql.Open(driver, dsn,
				otelsql.WithAttributes(semconv.DBSystemPostgreSQL),
			)
		}
		log.Infof("pgsql: lynx-tracer detected, database operations will be traced automatically")
	}

	if err := p.SQLPlugin.InitializeResources(rt); err != nil {
		return err
	}

	// Wire Prometheus metrics when prometheus config is present
	if p.pbConfig.Prometheus != nil {
		pmConfig := createPrometheusConfig(p.pbConfig)
		p.prometheusMetrics = NewPrometheusMetrics(pmConfig)
		adapter := newPgsqlMetricsAdapter(p.prometheusMetrics, p.pbConfig)
		p.SQLPlugin.SetMetricsRecorder(adapter)
	}

	return nil
}

// StartupTasks initializes database connection
func (p *DBPgsqlClient) StartupTasks() error {
	log.Infof("initializing pgsql database connection")

	if err := p.SQLPlugin.StartupTasks(); err != nil {
		return err
	}

	// When Prometheus is enabled, periodically push connection pool stats to metrics
	if p.prometheusMetrics != nil {
		ctx, cancel := context.WithCancel(context.Background())
		p.metricsCancel = cancel
		go p.runPoolStatsUpdater(ctx)
	}

	log.Infof("pgsql database successfully initialized with connection pool: max_open=%d, max_idle=%d",
		p.config.MaxOpenConns, p.config.MaxIdleConns)
	return nil
}

// runPoolStatsUpdater periodically updates Prometheus connection pool metrics.
// Updates even when not connected so dashboards show current state (e.g. zeros when DB is down).
func (p *DBPgsqlClient) runPoolStatsUpdater(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if p.prometheusMetrics != nil && p.SQLPlugin != nil {
				stats := p.SQLPlugin.GetStats()
				if stats != nil {
					p.prometheusMetrics.UpdateMetrics(stats, p.pbConfig)
				}
			}
		}
	}
}

// CleanupTasks gracefully closes database connection
func (p *DBPgsqlClient) CleanupTasks() error {
	if p.metricsCancel != nil {
		p.metricsCancel()
		p.metricsCancel = nil
	}
	log.Infof("closing pgsql database connection")
	return p.SQLPlugin.CleanupTasks()
}

// GetConfig returns the current database config (read-only). May be nil if plugin not initialized.
func (p *DBPgsqlClient) GetConfig() *interfaces.Config {
	return p.config
}

// GetMetricsGatherer returns the Prometheus Gatherer for this plugin's metrics, or nil if Prometheus is not enabled.
// The application can merge this with the default registry when exposing /metrics.
func (p *DBPgsqlClient) GetMetricsGatherer() prometheus.Gatherer {
	if p.prometheusMetrics == nil {
		return nil
	}
	return p.prometheusMetrics.GetGatherer()
}
