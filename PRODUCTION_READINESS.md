# lynx-pgsql Production Readiness

This document describes the production suitability of the lynx-pgsql plugin, recommended configuration, and potential risks.

## Conclusion: Production Ready

The plugin is suitable for production when the following conditions are met:

- The application is started via the Lynx framework, and the pgsql plugin is registered and started before calling `GetDB()` / `GetDriver()` or equivalent APIs.
- A valid `source` (DSN) must be provided in configuration; production should use SSL (e.g. `sslmode=require`).
- Production is advised to enable Prometheus metrics (configure `prometheus` under `lynx.pgsql`) and merge `GetMetricsGatherer()` into the application’s `/metrics` endpoint.

## Production Capabilities

| Capability        | Description |
|-------------------|-------------|
| Connection pool   | Based on `database/sql` + pgx; configurable `min_conn` / `max_conn`, `max_idle_time` / `max_life_time`. |
| Retry             | Automatic retry on connection failure (enabled by default; retry count configurable). |
| Health check      | Periodic health checks and active probing via `CheckHealth()`. |
| Graceful shutdown | `CleanupTasks` cancels the metrics updater goroutine and closes the DB connection. |
| Observability     | Optional Prometheus metrics (pool, health, slow queries, errors); automatic DB tracing is not available on the current sql-sdk rollback line even if lynx-tracer is loaded. |
| Config validation | Initialization validates required `source` and constrains pool parameters (e.g. max_idle ≤ max_open). |

## Production Configuration

### Connection pool

```yaml
lynx:
  pgsql:
    source: "postgres://user:pass@host:5432/db?sslmode=require&connect_timeout=10"
    min_conn: 20
    max_conn: 200
    max_idle_time: "300s"
    max_life_time: "3600s"
```

- Tune `min_conn` / `max_conn` based on instance QPS and DB `max_connections` to avoid excessive connections per instance.
- A `max_life_time` of about 1 hour is recommended to recycle connections for load balancing and failover.

### SSL and timeouts

- Use `sslmode=require` (or `verify-ca` / `verify-full`) in the DSN in production to avoid plaintext transport.
- Set `connect_timeout`, `statement_timeout`, etc. in the DSN to align with your timeout policy.

### Prometheus

- Configure `prometheus` under `lynx.pgsql` (namespace / subsystem / labels) and merge `pgsql.GetMetricsGatherer()` with the default registry when exposing `/metrics`.
- If `prometheus.labels` is set, label keys become metric dimensions and values are applied when recording (buildLabels merges them).

## Handling Call Order

If your code may run before or during Lynx / pgsql plugin startup (e.g. in goroutines, init, or early HTTP handlers), use the **wait-style APIs** and be aware of behavior in edge cases.

### WaitForDB / WaitForDBConnected / WaitForDriver

- **`WaitForDB(ctx)`**: Blocks until **GetDB() returns a non-nil *sql.DB** (Lynx initialized, plugin registered, and **StartupTasks has run and opened the pool**). It does not return as soon as the plugin is in the manager; it keeps polling until GetDB() succeeds.
- **`WaitForDBConnected(ctx)`**: After WaitForDB succeeds, blocks until **IsConnected() is true** (health check has passed, database reachable). If the database stays unavailable, it blocks until `ctx` times out or is cancelled.
- **`WaitForDriver(ctx)`**: Same readiness condition as WaitForDB; returns the ent Driver.

Example:

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
db, err := pgsql.WaitForDB(ctx)
if err != nil {
    return err
}
// Do not close db; it is the plugin-managed pool and is closed by Lynx on exit.
```

To **ensure the database is reachable** (e.g. before accepting traffic), use `WaitForDBConnected(ctx)` and give `ctx` a reasonable timeout so you do not wait indefinitely if the DB is down.

### Edge cases

| Scenario | WaitForDB behavior | Recommendation |
|----------|--------------------|----------------|
| Plugin in manager but StartupTasks not run yet | Keeps polling until GetDB() returns a valid db | No code change; already covered. |
| StartupTasks ran but connection failed (DB down, wrong password, etc.) | May return once the pool exists (GetDB() succeeds); first query/Ping may fail | Use `WaitForDBConnected(ctx)` or call `CheckHealth()` after return when you need a confirmed connection. |
| Process receives SIGTERM / graceful shutdown while waiting | Does not exit by itself; cancel the context in your shutdown path | Use `context.WithCancel` and cancel it on shutdown so WaitForDB returns with a context error. |
| Multiple goroutines calling WaitForDB | All block until the same pool is ready, then receive the same *sql.DB | No extra logic; safe to use. |

### Dependency injection (recommended for business layer)

Construct Handlers/Services that depend on the DB during application assembly, **after** Lynx and all plugins have started, and inject `*sql.DB` or `*entsql.Driver` via constructors. Then all DB-using code runs only after the plugin is ready, without caring about startup order.

---

## Caveats and notes

### 1. Call timing

- **Call `GetDB()` / `GetDriver()` only after** Lynx is initialized and the pgsql plugin is registered; otherwise you get an error. If you cannot guarantee order, use **WaitForDB / WaitForDriver** above.
- If Lynx is not initialized, these APIs return an error or zero value and do not panic (guarded by `lynx.Lynx() == nil`).

### 2. lynx-sql-sdk dependency

- Connection setup, retry, health check, and pool stats are implemented in lynx-sql-sdk base.
- The current rollback line used by this repo does **not** expose `OpenDBFunc`, so `lynx-pgsql` cannot automatically wrap connections with otelsql today. If DB spans are a release requirement, upgrade lynx-sql-sdk first and then restore the integration in plugin code.
- The repo’s `go.mod` uses `replace github.com/go-lynx/lynx-sql-sdk => ../lynx-sql-sdk` for local development; remove or adjust the replace when consuming the published module.

### 3. Prometheus label cardinality

- Keep `prometheus.labels` low-cardinality (e.g. `environment`, `service`, `region`). Avoid high-cardinality values (e.g. request IDs, user IDs) to prevent excessive time series and memory use.

### 4. Multiple data sources

- The plugin is a singleton (one pgsql client per process). For multiple data sources, extend the framework with multiple plugin instances or a custom wrapper.

### 5. DSN security

- Do not commit plaintext passwords in config; use a config server, environment variables, or secret management and inject into `source` in production.

### 6. Prometheus metric type change (backward compatibility)

- `wait_count_total`, `wait_duration_seconds_total`, `max_idle_closed_total`, and `max_lifetime_closed_total` were changed from Counter to Gauge; they are updated with the current cumulative value every 15 seconds via `Set` to avoid double-counting on periodic updates.
- If you use PromQL that assumes monotonic counters (e.g. `rate()`), switch to Gauge-style queries.

## Pre-launch checklist

- [ ] `lynx.pgsql.source` is set and reachable; production uses SSL.
- [ ] Pool parameters (`min_conn` / `max_conn` / `max_idle_time` / `max_life_time`) are tuned for instance and DB capacity.
- [ ] `GetDB()` / `GetDriver()` (or WaitForDB/WaitForDriver) are used only after Lynx and pgsql plugin are started.
- [ ] `lynx.pgsql.prometheus` is configured and `GetMetricsGatherer()` is merged into `/metrics`.
- [ ] Secrets (e.g. DSN password) come from a secure config source, not hardcoded.
- [ ] If using Grafana, dashboards and alerts are updated for the current metric types (including the Gauges above).

## Version and dependencies

- Plugin version: v2.0.0
- Dependencies: lynx, lynx-sql-sdk, pgx/v5, prometheus/client_golang
- Before production deployment, run connection pool load tests and failure drills (e.g. DB briefly unavailable, restart) in staging to confirm retry and graceful shutdown behavior.
