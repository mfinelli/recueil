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
	"bufio"
	"fmt"
	"io/fs"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/config"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/mirror"
	"github.com/mfinelli/recueil/internal/pgmigrate"
)

var userCreateRole string

var userCmd = &cobra.Command{
	Use:   "user",
	Short: "Manage Recueil user accounts",
}

// userCreateCmd is an operator/admin tool, not a dashboard replacement: it
// talks directly to Postgres (and, for the D1 pairing-token mirror, the
// Worker's internal endpoint) exactly the way `recueil server` does, rather
// than issuing HTTP requests against the backend's own /api/setup or
// /api/auth/register. Intended to be run on the server itself, using the
// same config (TOML/env) as `recueil server`. No bootstrap token involved --
// there's no HTTP request to gate in the first place.
var userCreateCmd = &cobra.Command{
	Use:   "create <username>",
	Short: "Create a user account directly in Postgres",
	Args:  cobra.ExactArgs(1),
	RunE:  runUserCreate,
}

func init() {
	userCreateCmd.Flags().StringVar(&userCreateRole, "role", "member", `account role: "admin" or "member"`)

	userCmd.AddCommand(userCreateCmd)
	rootCmd.AddCommand(userCmd)
}

func runUserCreate(cmd *cobra.Command, args []string) error {
	username := args[0]

	if userCreateRole != "admin" && userCreateRole != "member" {
		return fmt.Errorf(`--role must be "admin" or "member", got %q`, userCreateRole)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	pairingKey, err := auth.ParsePairingKey(cfg.PairingTokenKey)
	if err != nil {
		return fmt.Errorf("parsing pairing token key: %w", err)
	}

	password, err := readNewPassword()
	if err != nil {
		return fmt.Errorf("reading password: %w", err)
	}

	ctx := cmd.Context()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer pool.Close()

	// Applied the same way `recueil server` applies them, so this command
	// works standalone even if the server has never been started against
	// this database yet -- goose no-ops if everything's already current.
	postgresMigrations, err := fs.Sub(PostgresMigrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("preparing embedded postgres migrations: %w", err)
	}
	if err := pgmigrate.Run(ctx, pool, postgresMigrations); err != nil {
		return fmt.Errorf("applying postgres migrations: %w", err)
	}

	queries := db.New(pool)

	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	pairingRaw, pairingHash, err := auth.GeneratePairingToken()
	if err != nil {
		return fmt.Errorf("generating pairing token: %w", err)
	}
	pairingEnc, err := auth.EncryptPairingToken(pairingKey, pairingRaw)
	if err != nil {
		return fmt.Errorf("encrypting pairing token: %w", err)
	}

	user, err := queries.CreateUser(ctx, db.CreateUserParams{
		Username:        username,
		PasswordHash:    hash,
		PairingTokenEnc: pgtype.Text{String: pairingEnc, Valid: true},
		Role:            userCreateRole,
	})
	if err != nil {
		return fmt.Errorf("creating user (username may already be taken): %w", err)
	}

	// Same one-way write Setup/Register perform; doesn't roll back account
	// creation on failure (see mirror.PushUser's doc comment). The account
	// exists and can log in either way -- only device pairing depends on
	// this succeeding.
	mirrorClient := mirror.NewClient(cfg.WorkerURL, cfg.WorkerServiceSecret)
	if err := mirrorClient.PushUser(ctx, user.ID, &pairingHash); err != nil {
		log.Printf("warning: user created, but failed to push pairing-token mirror to D1: %v", err)
		log.Printf("device pairing for this user won't work until a mirror sync runs")
	}

	fmt.Printf("Created user %q (id %d, role %s).\n\n", user.Username, user.ID, user.Role)
	fmt.Printf("Pairing token (shown once -- use it with `recueil auth --url ...`):\n\n  %s\n\n", pairingRaw)
	return nil
}

// readNewPassword prompts twice with no echo and requires the two entries
// to match when stdin is a real terminal -- unlike a bootstrap-token retry,
// a typo here has no recovery path short of running UpdateUserPassword by
// hand, so it's worth catching at entry time. Falls back to a single
// unconfirmed line from stdin when not a terminal, so this still works
// piped/scripted (e.g. `echo "$PASSWORD" | recueil user create --username ...`).
func readNewPassword() (string, error) {
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		password := strings.TrimSpace(line)
		if password == "" {
			return "", fmt.Errorf("no password provided")
		}
		return password, nil
	}

	fmt.Fprint(os.Stderr, "Password: ")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}

	fmt.Fprint(os.Stderr, "Confirm password: ")
	second, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}

	if string(first) != string(second) {
		return "", fmt.Errorf("passwords did not match")
	}
	if len(first) == 0 {
		return "", fmt.Errorf("no password provided")
	}

	return string(first), nil
}
