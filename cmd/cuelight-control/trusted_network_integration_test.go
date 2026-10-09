package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmilldotdev/cuelight/internal/control/app"
	"github.com/jmilldotdev/cuelight/internal/control/composition"
	"github.com/jmilldotdev/cuelight/internal/control/operatorapi"
	"github.com/jmilldotdev/cuelight/internal/control/persistence"
	"github.com/jmilldotdev/cuelight/migrations"
)

// TestTrustedNetworkFullStack drives the production handler graph: operator
// routes admit token-less trusted peers as the persisted built-in actor, while
// node, browser-node, app, and remote credentials are untouched.
func TestTrustedNetworkFullStack(t *testing.T) {
	dsn := os.Getenv("CUELIGHT_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("CUELIGHT_POSTGRES_TEST_DSN is not configured")
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	// A whole database, not a schema: migrations install btree_gist, which a
	// concurrently running package's DROP SCHEMA … CASCADE would remove.
	database := fmt.Sprintf("cuelight_trusted_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.ExecContext(context.Background(), `DROP DATABASE `+database+` WITH (FORCE)`)
		admin.Close()
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + database
	databaseDSN := parsed.String()

	dir := filepath.Join(t.TempDir(), "control")
	var stderr bytes.Buffer
	args := []string{"init", "--dir", dir, "--hostname", "cuelight.test", "--public-url", "https://cuelight.test:8470", "--database-url", databaseDSN, "--trusted-network", "192.0.2.0/24,2001:db8::/32"}
	if code := run(ctx, args, nil, &bytes.Buffer{}, &stderr); code != 0 {
		t.Fatalf("init exit %d: %s", code, stderr.String())
	}
	env := readEnvFile(t, filepath.Join(dir, envFileName))
	env["CUELIGHT_CONTROL_UPDATE_CHECK"] = "off"
	config, err := app.LoadConfig(func(name string) (string, bool) { value, ok := env[name]; return value, ok })
	if err != nil {
		t.Fatal(err)
	}
	db, err := persistence.OpenPostgres(ctx, config.DatabaseURL, persistence.PoolConfig{MaxOpenConnections: 4, MaxIdleConnections: 1, ConnectionLifetime: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := persistence.ApplyMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	handler, err := composition.HandlerFactory(app.Runtime{Config: config, Database: db})
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, target, peer string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, nil)
		r.RemoteAddr = peer
		for name, value := range headers {
			r.Header.Set(name, value)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	trusted, untrusted := "192.0.2.10:40000", "198.51.100.7:40000"
	for _, path := range []string{"/api/v1/nodes", "/api/v1/media", "/api/v1/software", "/api/v1/browser-nodes", "/api/v1/admin-tokens"} {
		if w := do("GET", path, trusted, nil); w.Code != http.StatusOK {
			t.Errorf("trusted GET %s = %d %s", path, w.Code, w.Body.String())
		}
		if w := do("GET", path, untrusted, nil); w.Code != http.StatusUnauthorized {
			t.Errorf("untrusted GET %s = %d", path, w.Code)
		}
	}
	// Writes reference the built-in actor through created_by foreign keys.
	if w := do("POST", "/api/v1/enrollment-tokens", "[2001:db8::7]:1", map[string]string{"Idempotency-Key": "0123456789abcdef"}); w.Code != http.StatusCreated {
		t.Fatalf("trusted IPv6 write = %d %s", w.Code, w.Body.String())
	}
	var createdBy string
	if err := db.QueryRowContext(ctx, `SELECT a.name FROM enrollment_tokens t JOIN actors a ON a.id = t.created_by`).Scan(&createdBy); err != nil || createdBy != operatorapi.TrustedNetworkActorName {
		t.Fatalf("enrollment token created by %q, %v", createdBy, err)
	}

	var me operatorapi.OperatorMe
	w := do("GET", "/api/v1/operator/me", "[::ffff:192.0.2.9]:1", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil || me.AuthMethod != operatorapi.AuthMethodNetwork || me.PeerAddress != "192.0.2.9" {
		t.Fatalf("operator/me = %d %s", w.Code, w.Body.String())
	}
	if w := do("GET", "/api/v1/nodes", trusted, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}); w.Code != http.StatusForbidden {
		t.Fatalf("cross-site = %d", w.Code)
	}
	if w := do("GET", "/api/v1/nodes", trusted, map[string]string{"Origin": "https://cuelight.test:8470", "Sec-Fetch-Site": "same-origin"}); w.Code != http.StatusOK {
		t.Fatalf("same-origin = %d", w.Code)
	}

	// Node, browser-node, app, and remote-session credentials are unaffected.
	for _, test := range []struct{ method, path string }{
		{"GET", "/api/v1/player/assignment"},
		{"GET", "/api/v1/browser-node/player/assignment"},
		{"GET", "/api/v1/app/context"},
		{"POST", "/api/v1/remote/sessions/00000000-0000-4000-8000-000000000001/events"},
	} {
		if w := do(test.method, test.path, trusted, nil); w.Code != http.StatusUnauthorized {
			t.Errorf("trusted %s %s = %d, want 401", test.method, test.path, w.Code)
		}
	}
	// Only the app API advertises CORS; operator routes never do.
	if w := do("OPTIONS", "/api/v1/app/context", untrusted, map[string]string{"Origin": "https://app.example"}); w.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Fatalf("app preflight lost CORS: %v", w.Header())
	}
	if w := do("OPTIONS", "/api/v1/nodes", trusted, map[string]string{"Origin": "https://cuelight.test:8470", "Access-Control-Request-Method": "DELETE"}); w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("operator preflight sent CORS: %v", w.Header())
	}
}
