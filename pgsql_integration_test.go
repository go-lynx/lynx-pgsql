//go:build integration
// +build integration

package pgsql

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/go-lynx/lynx"
	"github.com/go-lynx/lynx-pgsql/conf"
	"github.com/go-lynx/lynx/plugins"
)

// TestPostgreSQLIntegration tests PostgreSQL plugin with real database connection
func TestPostgreSQLIntegration(t *testing.T) {
	// Check if PostgreSQL is available
	if !isPostgreSQLAvailable() {
		t.Skip("PostgreSQL is not available, skipping integration test")
	}

	// Create a minimal runtime for testing
	rt := createTestRuntime(t)

	// Create PostgreSQL client
	client := NewPgsqlClient()

	// Initialize resources
	err := client.InitializeResources(rt)
	if err != nil {
		t.Fatalf("InitializeResources failed: %v", err)
	}

	// Start plugin
	err = client.StartupTasks()
	if err != nil {
		t.Fatalf("StartupTasks failed: %v", err)
	}
	defer client.CleanupTasks()

	// Test connection
	if !client.IsConnected() {
		t.Error("PostgreSQL client should be connected")
	}

	// Test GetDB
	db, err := client.GetDB()
	if err != nil {
		t.Fatalf("GetDB failed: %v", err)
	}
	if db == nil {
		t.Fatal("GetDB returned nil")
	}

	// Test query execution
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var result int
	err = db.QueryRowContext(ctx, "SELECT 1").Scan(&result)
	if err != nil {
		t.Fatalf("Query execution failed: %v", err)
	}

	if result != 1 {
		t.Errorf("Expected result 1, got %d", result)
	}

	// Test health check
	err = client.CheckHealth()
	if err != nil {
		t.Errorf("CheckHealth failed: %v", err)
	}

	// Test GetDialect
	dialect := client.GetDialect()
	if dialect != "postgres" {
		t.Errorf("Expected dialect 'postgres', got '%s'", dialect)
	}

	// Test GetStats
	stats := client.GetStats()
	if stats == nil {
		t.Error("GetStats returned nil")
	}
}

// TestPostgreSQLPluginIntegration tests PostgreSQL plugin through plugin interface
func TestPostgreSQLPluginIntegration(t *testing.T) {
	if !isPostgreSQLAvailable() {
		t.Skip("PostgreSQL is not available, skipping integration test")
	}

	rt := createTestRuntime(t)
	client := NewPgsqlClient()

	err := client.InitializeResources(rt)
	if err != nil {
		t.Fatalf("InitializeResources failed: %v", err)
	}

	err = client.StartupTasks()
	if err != nil {
		t.Fatalf("StartupTasks failed: %v", err)
	}
	defer client.CleanupTasks()

	// Test through plugin interface
	db, err := GetDB()
	if err != nil {
		t.Fatalf("GetDB failed: %v", err)
	}

	// Create test table
	ctx := context.Background()
	_, err = db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS test_table (
			id SERIAL PRIMARY KEY,
			name VARCHAR(100) NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}

	// Insert data
	var id int
	err = db.QueryRowContext(ctx, "INSERT INTO test_table (name) VALUES ($1) RETURNING id", "test").Scan(&id)
	if err != nil {
		t.Fatalf("Failed to insert data: %v", err)
	}

	// Query data
	var name string
	err = db.QueryRowContext(ctx, "SELECT name FROM test_table WHERE id = $1", id).Scan(&name)
	if err != nil {
		t.Fatalf("Failed to query data: %v", err)
	}

	if name != "test" {
		t.Errorf("Expected name 'test', got '%s'", name)
	}

	// Cleanup
	_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS test_table")
}

// TestPostgreSQLConnectionPool tests connection pool functionality
func TestPostgreSQLConnectionPool(t *testing.T) {
	if !isPostgreSQLAvailable() {
		t.Skip("PostgreSQL is not available, skipping integration test")
	}

	rt := createTestRuntime(t)
	client := NewPgsqlClient()

	err := client.InitializeResources(rt)
	if err != nil {
		t.Fatalf("InitializeResources failed: %v", err)
	}

	err = client.StartupTasks()
	if err != nil {
		t.Fatalf("StartupTasks failed: %v", err)
	}
	defer client.CleanupTasks()

	db, err := client.GetDB()
	if err != nil {
		t.Fatalf("GetDB failed: %v", err)
	}

	// Test concurrent queries
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			var result int
			err := db.QueryRowContext(ctx, "SELECT 1").Scan(&result)
			if err != nil {
				t.Errorf("Concurrent query failed: %v", err)
			}
			done <- true
		}()
	}

	// Wait for all queries
	for i := 0; i < 10; i++ {
		<-done
	}

	// Check stats
	stats := client.GetStats()
	if stats.MaxOpenConnections == 0 {
		t.Error("MaxOpenConnections should be greater than 0")
	}
}

// TestPostgreSQLGetDriver tests GetDriver for Ent integration
func TestPostgreSQLGetDriver(t *testing.T) {
	if !isPostgreSQLAvailable() {
		t.Skip("PostgreSQL is not available, skipping integration test")
	}

	rt := createTestRuntime(t)
	client := NewPgsqlClient()

	err := client.InitializeResources(rt)
	if err != nil {
		t.Fatalf("InitializeResources failed: %v", err)
	}

	err = client.StartupTasks()
	if err != nil {
		t.Fatalf("StartupTasks failed: %v", err)
	}
	defer client.CleanupTasks()

	// Test GetDriver
	driver, err := GetDriver()
	if err != nil {
		t.Fatalf("GetDriver failed: %v", err)
	}
	if driver == nil {
		t.Error("GetDriver returned nil")
	}
}

// Helper functions

func isPostgreSQLAvailable() bool {
	db, err := sql.Open("pgx", pgsqlIntegrationDSN())
	if err != nil {
		return false
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return false
	}
	return true
}

func createTestRuntime(t *testing.T) plugins.Runtime {
	t.Helper()
	// Use the mockRuntime defined in pgsql_test.go so we don't duplicate types.
	rt := &mockRuntime{
		config: map[string]any{
			"lynx.pgsql": &conf.Pgsql{
				Driver:  "pgx",
				Source:  pgsqlIntegrationDSN(),
				MinConn: 5,
				MaxConn: 20,
			},
		},
	}

	// For integration tests we also need to wire the real Lynx runtime so that
	// package-level helpers (GetDB, IsConnected, etc.) work. Wire the plugin
	// into a real TypedRuntimePlugin so Lynx().GetPluginManager() can find it.
	realRT := lynx.NewTypedRuntimePlugin()
	realRT.SetConfig(rt.GetConfig())
	return realRT
}

func pgsqlIntegrationDSN() string {
	if dsn := os.Getenv("LYNX_PGSQL_TEST_DSN"); dsn != "" {
		return dsn
	}
	return "postgres://lynx:lynx-local-password@localhost:5432/lynx_test?sslmode=disable"
}
