/*
 * recueil: self-hosted webpage bookmarker and archiver
 * Copyright © 2026 Mario Finelli
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

package d1migrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/d1"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadMigrations(t *testing.T) {
	tests := []struct {
		name      string
		fsys      fstest.MapFS
		wantIDs   []string
		wantErr   bool
		errSubstr string
	}{
		{
			name: "sorts and filters non-sql files and subdirectories",
			fsys: fstest.MapFS{
				"0002_captures.sql":          {Data: []byte("-- c")},
				"0000_schema_migrations.sql": {Data: []byte("-- a")},
				"0001_users.sql":             {Data: []byte("-- b")},
				"README.md":                  {Data: []byte("not a migration")},
				"sub/0003_ignored.sql":       {Data: []byte("-- nested, should be skipped")},
			},
			wantIDs: []string{"0000_schema_migrations", "0001_users", "0002_captures"},
		},
		{
			name:      "rejects filenames outside the safe charset",
			fsys:      fstest.MapFS{"0001-bad name!.sql": {Data: []byte("-- x")}},
			wantErr:   true,
			errSubstr: "invalid characters",
		},
		{
			name:    "empty directory returns no ids and no error",
			fsys:    fstest.MapFS{},
			wantIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, files, err := readMigrations(tt.fsys)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errSubstr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantIDs, ids)
			assert.Len(t, files, len(tt.wantIDs))
		})
	}
}

func TestQueryParams(t *testing.T) {
	cfg := Config{AccountID: "acct-123", DatabaseID: "db-456"}
	params := queryParams(cfg, "SELECT 1;")
	assert.Equal(t, "acct-123", params.AccountID.Value)

	body, ok := params.Body.(d1.DatabaseQueryParamsBodyD1SingleQuery)
	require.True(t, ok, "expected Body to be a single-query body")
	assert.Equal(t, "SELECT 1;", body.Sql.Value)
}

// TestRun covers Run's top-level control flow: the bootstrap-must-sort-first
// invariant (checked before any network call), and the apply/skip decision
// against a mocked D1 API.
func TestRun(t *testing.T) {
	t.Run("requires bootstrap migration to sort first", func(t *testing.T) {
		// client is nil deliberately: this check happens before Run ever
		// touches the network, so a nil client proves that ordering.
		fsys := fstest.MapFS{
			"0001_users.sql": {Data: []byte("CREATE TABLE users (id INTEGER PRIMARY KEY);")},
		}

		err := Run(context.Background(), nil, Config{}, fsys)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected first migration to be")
	})

	t.Run("applies pending migrations and skips already-applied ones", func(t *testing.T) {
		// Checks, in order:
		//   - the bootstrap migration is always sent, combined with its own
		//     INSERT OR IGNORE, as a single request
		//   - schema_migrations is queried for already-applied ids
		//   - a migration already reported as applied is never re-sent
		//   - a pending migration is sent, combined with a plain INSERT
		fsys := fstest.MapFS{
			"0000_schema_migrations.sql": {Data: []byte("CREATE TABLE IF NOT EXISTS schema_migrations (id TEXT PRIMARY KEY) STRICT, WITHOUT ROWID;")},
			"0001_users.sql":             {Data: []byte("CREATE TABLE users (id INTEGER PRIMARY KEY) STRICT;")},
			"0002_sessions.sql":          {Data: []byte("CREATE TABLE sessions (id TEXT PRIMARY KEY) STRICT;")},
		}

		var receivedSQL []string

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				SQL string `json:"sql"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			receivedSQL = append(receivedSQL, body.SQL)

			w.Header().Set("Content-Type", "application/json")

			var resp map[string]any
			switch body.SQL {
			case "SELECT id FROM schema_migrations;":
				// Report 0001_users as already applied; 0000 and 0002 are not.
				resp = map[string]any{
					"success": true,
					"errors":  []any{},
					"result": []map[string]any{
						{
							"success": true,
							"results": []map[string]any{{"id": "0001_users"}},
							"meta":    map[string]any{},
						},
					},
				}
			default:
				resp = map[string]any{
					"success": true,
					"errors":  []any{},
					"result": []map[string]any{
						{"success": true, "results": []any{}, "meta": map[string]any{}},
					},
				}
			}

			if err := json.NewEncoder(w).Encode(resp); err != nil {
				t.Errorf("failed to encode mock D1 response: %v", err)
			}
		}))
		defer server.Close()

		client := cloudflare.NewClient(
			option.WithAPIToken("test-token"),
			option.WithBaseURL(server.URL),
		)

		err := Run(context.Background(), client, Config{AccountID: "acct", DatabaseID: "db"}, fsys)
		require.NoError(t, err)

		require.Len(t, receivedSQL, 3, "expected: bootstrap apply, schema_migrations select, one pending migration apply")

		assert.Contains(t, receivedSQL[0], "CREATE TABLE IF NOT EXISTS schema_migrations")
		assert.Contains(t, receivedSQL[0], "INSERT OR IGNORE INTO schema_migrations (id) VALUES ('0000_schema_migrations')")

		assert.Equal(t, "SELECT id FROM schema_migrations;", receivedSQL[1])

		// 0001_users was reported applied and must not appear again;
		// 0002_sessions was pending and must be sent with a plain
		// (non-IGNORE) insert.
		assert.NotContains(t, receivedSQL[2], "CREATE TABLE users")
		assert.Contains(t, receivedSQL[2], "CREATE TABLE sessions")
		assert.Contains(t, receivedSQL[2], "INSERT INTO schema_migrations (id) VALUES ('0002_sessions')")
		assert.NotContains(t, receivedSQL[2], "INSERT OR IGNORE")
	})
}
