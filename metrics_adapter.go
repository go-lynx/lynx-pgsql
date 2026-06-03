// Package pgsql provides a PostgreSQL client plugin for the Lynx framework.
// It wraps database/sql with a production-grade connection pool, Prometheus metrics
// (connection pool stats, query/transaction latency), configurable health checks, and
// lifecycle management driven by protobuf-based configuration.
package pgsql

import (
	"time"

	"github.com/go-lynx/lynx-pgsql/conf"
	"github.com/go-lynx/lynx-sql-sdk/base"
)

// pgsqlMetricsAdapter adapts PrometheusMetrics to base.MetricsRecorder
// so the SQL base plugin can drive pgsql-specific Prometheus metrics.
type pgsqlMetricsAdapter struct {
	pm     *PrometheusMetrics
	config *conf.Pgsql
}

// newPgsqlMetricsAdapter returns a MetricsRecorder that delegates to pm with config.
func newPgsqlMetricsAdapter(pm *PrometheusMetrics, config *conf.Pgsql) *pgsqlMetricsAdapter {
	return &pgsqlMetricsAdapter{pm: pm, config: config}
}

// RecordConnectionPoolStats implements base.MetricsRecorder
func (a *pgsqlMetricsAdapter) RecordConnectionPoolStats(stats *base.ConnectionPoolStats) {
	if a.pm != nil {
		a.pm.UpdateMetrics(stats, a.config)
	}
}

// RecordHealthCheck implements base.MetricsRecorder
func (a *pgsqlMetricsAdapter) RecordHealthCheck(success bool) {
	if a.pm != nil {
		a.pm.RecordHealthCheck(success, a.config)
	}
}

// RecordQuery implements base.MetricsRecorder
func (a *pgsqlMetricsAdapter) RecordQuery(duration time.Duration, err error, threshold time.Duration) {
	if a.pm != nil {
		a.pm.RecordQuery("query", duration, err, threshold, a.config, "")
	}
}

// RecordTx implements base.MetricsRecorder
func (a *pgsqlMetricsAdapter) RecordTx(duration time.Duration, committed bool) {
	if a.pm != nil {
		a.pm.RecordTx(duration, committed, a.config)
	}
}

// IncConnectAttempt implements base.MetricsRecorder
func (a *pgsqlMetricsAdapter) IncConnectAttempt() {
	if a.pm != nil {
		a.pm.IncConnectAttempt(a.config)
	}
}

// IncConnectRetry implements base.MetricsRecorder
func (a *pgsqlMetricsAdapter) IncConnectRetry() {
	if a.pm != nil {
		a.pm.IncConnectRetry(a.config)
	}
}

// IncConnectSuccess implements base.MetricsRecorder
func (a *pgsqlMetricsAdapter) IncConnectSuccess() {
	if a.pm != nil {
		a.pm.IncConnectSuccess(a.config)
	}
}

// IncConnectFailure implements base.MetricsRecorder
func (a *pgsqlMetricsAdapter) IncConnectFailure() {
	if a.pm != nil {
		a.pm.IncConnectFailure(a.config)
	}
}
