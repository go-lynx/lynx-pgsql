// Package pgsql provides a Lynx plugin for PostgreSQL using the pgx driver
// via database/sql. Configure with config key "lynx.pgsql" (source required).
package pgsql

import (
	"errors"
	"fmt"

	"github.com/go-lynx/lynx"
	"github.com/go-lynx/lynx-pgsql/conf"
	"github.com/go-lynx/lynx-sql-sdk/base"
	"github.com/go-lynx/lynx-sql-sdk/interfaces"
	"github.com/go-lynx/lynx/log"
	"github.com/go-lynx/lynx/plugins"
	"github.com/prometheus/client_golang/prometheus"

	// PostgreSQL driver
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Plugin metadata
const (
	pluginName        = "pgsql.client"
	pluginVersion     = "v1.6.3"
	pluginDescription = "pgsql client plugin for lynx framework"
	confPrefix        = "lynx.pgsql"
	// pluginPriority is the startup order relative to other plugins (higher runs later)
	pluginPriority = 101
	// tracerPluginName is the Lynx plugin name of lynx-tracer.
	tracerPluginName = "tracer.server"
)

// DBPgsqlClient represents PostgreSQL client plugin instance
type DBPgsqlClient struct {
	*base.SQLPlugin
	config           *interfaces.Config
	pbConfig         *conf.Pgsql // protobuf configuration
	prometheusMetrics *PrometheusMetrics
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
	c.SQLPlugin.SetProvider(dbProvider{})

	return c
}

// InitializeResources loads protobuf configuration and initialises the base plugin.
// Config is loaded exclusively from the proto schema (source, max_conn, min_conn …).
// The base plugin's config scan is intentionally skipped via InitializeFromConfig so
// that proto values cannot be overwritten by a second scan of the same YAML key.
func (p *DBPgsqlClient) InitializeResources(rt plugins.Runtime) error {
	pbConfig := &conf.Pgsql{}
	if err := rt.GetConfig().Value(confPrefix).Scan(pbConfig); err != nil {
		return fmt.Errorf("failed to load PostgreSQL configuration: %w", err)
	}
	p.pbConfig = pbConfig

	if pbConfig.Source == "" {
		return fmt.Errorf("postgresql source (DSN) is required")
	}

	// Map proto config fields onto interfaces.Config before base runs.
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

	if lynx.Lynx() != nil && lynx.Lynx().GetPluginManager().GetPlugin(tracerPluginName) != nil {
		log.Warnf("pgsql: lynx-tracer detected, but sql-sdk lacks OpenDBFunc; automatic DB tracing is disabled")
	}

	// InitializeFromConfig applies defaults+validation without re-scanning the same
	// YAML key, preventing proto values from being silently overwritten.
	if err := p.SQLPlugin.InitializeFromConfig(rt); err != nil {
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

	log.Infof("pgsql database successfully initialized with connection pool: max_open=%d, max_idle=%d",
		p.config.MaxOpenConns, p.config.MaxIdleConns)
	return nil
}

// CleanupTasks gracefully closes database connection
func (p *DBPgsqlClient) CleanupTasks() error {
	log.Infof("closing pgsql database connection")
	err := p.SQLPlugin.CleanupTasks()
	if errors.Is(err, base.ErrAlreadyClosed) {
		return nil
	}
	return err
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
