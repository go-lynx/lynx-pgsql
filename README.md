# PgSQL Database Plugin

This is a PgSQL database connection plugin for the Lynx framework, providing complete database connection management functionality.

## Features

### ✅ Implemented Features

1. **Configuration Validation**: Automatic validation of configuration parameter validity
2. **Error Handling**: Graceful error handling to avoid panics
3. **Retry Mechanism**: Automatic retry on connection failure
4. **Connection Pool Monitoring**: Real-time monitoring of connection pool status
5. **Health Check**: Comprehensive database health check
6. **Graceful Shutdown**: Safe connection closing mechanism
7. **Configuration Updates**: Support for runtime configuration updates
8. **Detailed Logging**: Provides detailed debugging and monitoring logs
9. **Statistics**: Provides connection pool statistics
10. **Status Query**: Provides connection status query interface
11. **Tracing**: When the [lynx-tracer](https://github.com/go-lynx/lynx-tracer) plugin is loaded, DB operations are automatically traced (OpenTelemetry); each query/exec appears as a child span in the same trace as the request.

## Tracing (lynx-tracer integration)

If your application loads the **lynx-tracer** plugin (e.g. `tracer.server`), the pgsql plugin detects it and **automatically** opens the database connection through [otelsql](https://github.com/XSAM/otelsql). Every `QueryContext` / `ExecContext` / `BeginTx` etc. will then create a span under the current trace context, so you see DB latency and errors in the same trace as your HTTP/gRPC handler in Jaeger, Zipkin, or any OTLP backend.

- **No extra config**: Just register both `lynx-tracer` and `lynx-pgsql`; no pgsql config needed for tracing.
- **No tracer**: If lynx-tracer is not loaded, pgsql uses a normal connection (no tracing overhead).
- **Requires**: [lynx-sql-sdk](https://github.com/go-lynx/lynx-sql-sdk) with `OpenDBFunc` support (optional driver wrapper). When using the repo from source, a `replace` in `go.mod` for the local sql-sdk is used so this works before a new SDK release.

## Configuration Guide

### Basic Configuration

```yaml
lynx:
  pgsql:
    driver: "pgx"
    source: "postgres://username:password@host:port/database?sslmode=disable"
    min_conn: 10
    max_conn: 50
    max_idle_time: "30s"
    max_life_time: "300s"
```


### Configuration Parameters

| Parameter | Type | Default Value | Description |
|-----------|------|---------------|-------------|
| `driver` | string | "pgx" | Database driver name (use `pgx` for this plugin) |
| `source` | string | required | Database connection string (DSN) |
| `min_conn` | int | 5 | Minimum number of idle connections; enables pool warmup when set |
| `max_conn` | int | 25 | Maximum number of open connections |
| `max_idle_conn` | int | 0 | Maximum idle connections (overrides min_conn for idle cap when set) |
| `max_idle_time` | duration | "300s" | Maximum idle time for connections |
| `max_life_time` | duration | "3600s" | Maximum lifetime for connections |
| `prometheus` | object | nil | When set, enables Prometheus metrics (namespace/subsystem/labels) |

### Connection String Format

```
postgres://username:password@host:port/database?param1=value1&param2=value2
```


Common parameters:
- `sslmode`: SSL mode (disable, require, verify-ca, verify-full)
- `connect_timeout`: Connection timeout
- `statement_timeout`: Statement timeout
- `application_name`: Application name

## Usage

### 1. Getting Database Driver

```go
import (
    "github.com/go-lynx/lynx/plugins/db/pgsql"
    "entgo.io/ent/dialect/sql"
)

// Get database driver
driver, err := pgsql.GetDriver()
if err != nil {
    // Handle error
    log.Errorf("Failed to get database driver: %v", err)
    return
}

// Create client using ent
client := ent.NewClient(ent.Driver(driver))
```


### 2. Health Check

```go
// Perform health check
err := pgsql.CheckHealth()
if err != nil {
    log.Errorf("Database health check failed: %v", err)
}
```


### 3. Getting Connection Pool Statistics

```go
// Get connection pool statistics
stats := pgsql.GetStats()
if stats != nil {
    log.Infof("Connection pool stats: open=%d, in_use=%d, idle=%d", 
        stats.OpenConnections, stats.InUse, stats.Idle)
}
```


### 4. Checking Connection Status

```go
// Check if connected
if pgsql.IsConnected() {
    log.Info("Database is connected")
} else {
    log.Warn("Database is not connected")
}
```


### 5. Getting Configuration Information

```go
// Get current configuration (read-only)
config := pgsql.GetConfig()
if config != nil {
    log.Infof("Current config: driver=%s, max_open_conns=%d",
        config.Driver, config.MaxOpenConns)
}
```


### 6. Prometheus Monitoring

Enable Prometheus by adding a `prometheus` block to your `lynx.pgsql` config (see below). Then merge the plugin's metrics into your `/metrics` endpoint:

```go
import (
    "net/http"
    "github.com/go-lynx/lynx-pgsql"
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)

// Merge pgsql metrics with default registry when exposing /metrics
var gatherers prometheus.Gatherers
if g := pgsql.GetMetricsGatherer(); g != nil {
    gatherers = append(gatherers, g)
}
gatherers = append(gatherers, prometheus.DefaultGatherer)
http.Handle("/metrics", promhttp.HandlerFor(gatherers, promhttp.HandlerOpts{}))
```


## Prometheus Monitoring Configuration

### Enabling Monitoring

Add a `prometheus` block under `lynx.pgsql` to enable metrics (connection pool, health check, connect attempts, etc.). Metrics are exposed via `pgsql.GetMetricsGatherer()` and merged into your app's `/metrics` endpoint.

```yaml
lynx:
  pgsql:
    driver: "pgx"
    source: "postgres://user:pass@localhost:5432/dbname"
    min_conn: 10
    max_conn: 50
    prometheus:
      namespace: "lynx"
      subsystem: "pgsql"
      labels:
        environment: "production"
        service: "myapp"
```


### Monitoring Metrics

The plugin provides the following Prometheus metrics:

#### Connection Pool Metrics
- `lynx_pgsql_max_open_connections`: Maximum number of connections
- `lynx_pgsql_open_connections`: Current number of connections
- `lynx_pgsql_in_use_connections`: Number of connections in use
- `lynx_pgsql_idle_connections`: Number of idle connections
- `lynx_pgsql_max_idle_connections`: Maximum number of idle connections

#### Wait Metrics
- `lynx_pgsql_wait_count_total`: Total number of connection waits
- `lynx_pgsql_wait_duration_seconds_total`: Total time waiting for connections

#### Connection Close Metrics
- `lynx_pgsql_max_idle_closed_total`: Number of connections closed due to idle timeout
- `lynx_pgsql_max_lifetime_closed_total`: Number of connections closed due to lifetime expiration

#### Health Check Metrics
- `lynx_pgsql_health_check_total`: Total number of health checks
- `lynx_pgsql_health_check_success_total`: Number of successful health checks
- `lynx_pgsql_health_check_failure_total`: Number of failed health checks

#### Configuration Metrics
- `lynx_pgsql_config_min_connections`: Configured minimum number of connections
- `lynx_pgsql_config_max_connections`: Configured maximum number of connections

### Accessing Monitoring Metrics

After starting the application and registering `GetMetricsGatherer()` with your HTTP `/metrics` handler, access metrics at your app's metrics endpoint:

```bash
# Replace with your app's metrics host and port
curl http://localhost:8080/metrics | grep lynx_pgsql
```


### Prometheus Configuration

Add scrape targets in the Prometheus configuration file:

```yaml
scrape_configs:
  - job_name: 'lynx-pgsql'
    static_configs:
      - targets: ['localhost:9090']
    metrics_path: '/metrics'
    scrape_interval: 15s
```


### Grafana Dashboard

You can create Grafana dashboards to visualize monitoring metrics:

```json
{
  "dashboard": {
    "title": "Lynx PgSQL Monitoring",
    "panels": [
      {
        "title": "Connection Pool Status",
        "type": "stat",
        "targets": [
          {
            "expr": "lynx_pgsql_open_connections",
            "legendFormat": "Current Connections"
          }
        ]
      },
      {
        "title": "Connection Pool Utilization",
        "type": "gauge",
        "targets": [
          {
            "expr": "lynx_pgsql_in_use_connections / lynx_pgsql_max_open_connections * 100",
            "legendFormat": "Utilization %"
          }
        ]
      }
    ]
  }
}
```


## Monitoring and Debugging

### Connection Pool Statistics

The plugin provides the following statistics:

- **MaxOpenConnections**: Maximum open connections
- **OpenConnections**: Current open connections
- **InUse**: Connections in use
- **Idle**: Idle connections
- **MaxIdleConnections**: Maximum idle connections
- **WaitCount**: Number of connection waits
- **WaitDuration**: Total time waiting for connections
- **MaxIdleClosed**: Connections closed due to idle timeout
- **MaxLifetimeClosed**: Connections closed due to lifetime expiration

### Log Information

The plugin outputs detailed log information:

- Configuration loading and validation
- Connection establishment process
- Retry attempts
- Health check results
- Connection pool status
- Error and warning messages

## Error Handling

The plugin implements comprehensive error handling mechanisms:

1. **Configuration Validation Errors**: Validate configuration validity during initialization
2. **Connection Errors**: Automatic retry on connection failure
3. **Health Check Errors**: Provide detailed health check error information
4. **Shutdown Errors**: Gracefully handle connection closing errors

## Best Practices

### 1. Connection Pool Configuration

```yaml
# Development environment
min_conn: 5
max_conn: 20

# Production environment
min_conn: 20
max_conn: 200
```


### 2. Timeout Configuration

```yaml
# Reasonable timeout configuration
max_idle_time: "300s"    # 5 minutes
max_life_time: "3600s"   # 1 hour
```


### 3. SSL Configuration

```yaml
# Development environment
source: "postgres://user:pass@localhost:5432/db?sslmode=disable"

# Production environment
source: "postgres://user:pass@db.example.com:5432/db?sslmode=require"
```


### 4. Monitoring Integration

```go
// Periodically check connection pool status
go func() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ticker.C:
            stats := pgsql.GetStats()
            if stats != nil {
                // Send monitoring metrics
                metrics.RecordConnectionPoolStats(stats)
            }
        }
    }
}()
```


## Troubleshooting

### Common Issues

1. **Connection Failure**
   - Check connection string format
   - Verify database service is running
   - Check network connection

2. **Connection Pool Exhaustion**
   - Increase `max_conn` configuration
   - Check for connection leaks
   - Optimize query performance

3. **Health Check Failure**
   - Check database service status
   - Verify network connection
   - Check detailed error logs

### Debugging Tips

1. Enable detailed logging
2. Monitor connection pool statistics
3. Perform regular health checks
4. Use connection pool monitoring tools

## Version History

- **v2.0.0**: Refactored version, added complete error handling, retry mechanism, monitoring functions, etc.
- **v1.x.x**: Basic version, provides fundamental database connection functionality