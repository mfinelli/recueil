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
	"errors"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/mfinelli/recueil/internal/config"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/devices"
)

var deviceCmd = &cobra.Command{
	Use:   "device",
	Short: "Manage a user's paired devices",
}

// deviceListCmd and deviceRevokeCmd are operator-only tools, same category
// as userResyncCmd. The rare case where the dashboard's own self-scoped
// Manage Devices screen isn't available or isn't an option -- the person
// locked out can't reach it themselves (lost/stolen device, forgotten
// password with no session left), so someone with server access revokes on
// their behalf. Talks to the Worker directly via internal/devices.Client,
// the same client the dashboard's own ListDevices/RevokeDevice handlers use
// -- Postgres is only ever consulted here to resolve a username into the user
// id devices.Client's calls need.
var deviceListCmd = &cobra.Command{
	Use:   "list <username>",
	Short: "List a user's paired devices",
	Args:  cobra.ExactArgs(1),
	RunE:  runDeviceList,
}

var deviceRevokeCmd = &cobra.Command{
	Use:   "revoke <username> <device-id>",
	Short: "Revoke one of a user's paired devices",
	Args:  cobra.ExactArgs(2),
	RunE:  runDeviceRevoke,
}

func init() {
	deviceCmd.AddCommand(deviceListCmd)
	deviceCmd.AddCommand(deviceRevokeCmd)
	rootCmd.AddCommand(deviceCmd)
}

func runDeviceList(cmd *cobra.Command, args []string) error {
	username := args[0]

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	ctx := cmd.Context()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer pool.Close()

	queries := db.New(pool)

	user, err := queries.GetUserByUsername(ctx, username)
	if err != nil {
		return fmt.Errorf("looking up user %q: %w", username, err)
	}

	devicesClient := devices.NewClient(cfg.WorkerURL, cfg.WorkerServiceSecret)
	tokens, err := devicesClient.ListTokens(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("listing devices for %q: %w", username, err)
	}

	if len(tokens) == 0 {
		fmt.Printf("No paired devices for %q.\n", username)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tDEVICE NAME\tTYPE\tPAIRED\tLAST USED")
	for _, t := range tokens {
		lastUsed := "never"
		if t.LastUsedAt != nil {
			lastUsed = t.LastUsedAt.Format("2006-01-02 15:04")
		}
		_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n",
			t.ID, t.DeviceName, t.DeviceType, t.CreatedAt.Format("2006-01-02 15:04"), lastUsed)
	}
	return w.Flush()
}

func runDeviceRevoke(cmd *cobra.Command, args []string) error {
	username := args[0]
	deviceID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid device id %q: %w", args[1], err)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	ctx := cmd.Context()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer pool.Close()

	queries := db.New(pool)

	user, err := queries.GetUserByUsername(ctx, username)
	if err != nil {
		return fmt.Errorf("looking up user %q: %w", username, err)
	}

	devicesClient := devices.NewClient(cfg.WorkerURL, cfg.WorkerServiceSecret)

	// Listed first, rather than revoking blind, so a successful run can
	// report which device it actually revoked by name -- and so a wrong
	// device id fails with "no such device" before ever reaching the
	// Worker, rather than surfacing as devices.ErrNotFound after the fact.
	tokens, err := devicesClient.ListTokens(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("listing devices for %q: %w", username, err)
	}

	var target *devices.Token
	for i := range tokens {
		if tokens[i].ID == deviceID {
			target = &tokens[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("no device with id %d for user %q", deviceID, username)
	}

	if err := devicesClient.RevokeToken(ctx, user.ID, deviceID); err != nil {
		if errors.Is(err, devices.ErrNotFound) {
			return fmt.Errorf("no device with id %d for user %q", deviceID, username)
		}
		return fmt.Errorf("revoking device %d for %q: %w", deviceID, username, err)
	}

	fmt.Printf("Revoked %q (%s), device id %d, for %q.\n", target.DeviceName, target.DeviceType, deviceID, username)
	return nil
}
