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

package cmd

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/config"
	"github.com/mfinelli/recueil/internal/d1migrate"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/httpapi"
	"github.com/mfinelli/recueil/internal/mirror"
	"github.com/mfinelli/recueil/internal/pgmigrate"
)

var (
	PostgresMigrationsFS embed.FS
	D1MigrationsFS       embed.FS

	Commit  string
	Date    string
	Version string
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Run the Recueil backend server",
	RunE:  runServer,
}

func init() {
	rootCmd.AddCommand(serverCmd)
}

func runServer(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	pool, err := pgxpool.New(cmd.Context(), cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer pool.Close()

	postgresMigrations, err := fs.Sub(PostgresMigrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("preparing embedded postgres migrations: %w", err)
	}
	if err := pgmigrate.Run(cmd.Context(), pool, postgresMigrations); err != nil {
		return fmt.Errorf("applying postgres migrations: %w", err)
	}

	queries := db.New(pool)

	if err := runD1Migrations(cmd.Context(), cfg); err != nil {
		return fmt.Errorf("applying D1 migrations: %w", err)
	}

	bootstrap, err := newBootstrapHolder(cmd.Context(), queries)
	if err != nil {
		return fmt.Errorf("preparing bootstrap token: %w", err)
	}

	mirrorClient := mirror.NewClient(cfg.WorkerURL, cfg.WorkerServiceSecret)
	server := httpapi.NewServer(queries, mirrorClient, bootstrap, cfg.SessionCookieSecure)
	router, err := httpapi.NewRouter(server, pool, queries, httpapi.BuildInfo{
		Version:   Version,
		GitSHA:    Commit,
		BuildDate: Date,
	})
	if err != nil {
		return fmt.Errorf("creating router: %w", err)
	}

	httpServer := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: router,
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("recueil backend listening on %s", cfg.ListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-cmd.Context().Done():
		log.Println("shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

func runD1Migrations(ctx context.Context, cfg config.Config) error {
	client := cloudflare.NewClient(option.WithAPIToken(cfg.CloudflareAPIToken))
	d1Cfg := d1migrate.Config{
		AccountID:  cfg.CloudflareAccountID,
		DatabaseID: cfg.CloudflareD1DatabaseID,
	}

	migrations, err := fs.Sub(D1MigrationsFS, "terraform/migrations")
	if err != nil {
		return fmt.Errorf("preparing embedded D1 migrations: %w", err)
	}

	return d1migrate.Run(ctx, client, d1Cfg, migrations)
}

// newBootstrapHolder always returns a real, usable holder (safe to hand to
// httpapi.NewServer unconditionally), but only prints the token to the
// logs when it could actually matter.
func newBootstrapHolder(ctx context.Context, q *db.Queries) (*auth.BootstrapTokenHolder, error) {
	holder, raw, err := auth.NewBootstrapTokenHolder()
	if err != nil {
		return nil, err
	}

	count, err := q.CountUsers(ctx)
	if err != nil {
		return nil, err
	}
	if count > 0 {
		return holder, nil
	}

	log.Printf(
		"\n\n"+
			"==================================================================\n"+
			"No admin account exists yet. Bootstrap token (valid 1 hour):\n\n"+
			"  %s\n\n"+
			"Use this to create the first admin account via the dashboard's\n"+
			"setup screen (POST /api/setup).\n"+
			"==================================================================\n",
		raw,
	)
	return holder, nil
}
