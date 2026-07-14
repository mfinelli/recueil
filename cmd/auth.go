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
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mfinelli/recueil/internal/clicreds"
	"github.com/mfinelli/recueil/internal/deviceapi"
)

var (
	authWorkerURL  string
	authDeviceName string
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Pair this device with a Recueil Worker and store the resulting credentials locally",
	RunE:  runAuth,
}

func init() {
	authCmd.Flags().StringVar(&authWorkerURL, "url", "", "Worker URL to pair against (required)")
	authCmd.Flags().StringVar(&authDeviceName, "name", "", "device name to register (defaults to this machine's hostname)")
	if err := authCmd.MarkFlagRequired("url"); err != nil {
		panic(err)
	}
	rootCmd.AddCommand(authCmd)
}

func runAuth(cmd *cobra.Command, args []string) error {
	deviceName := authDeviceName
	if deviceName == "" {
		hostname, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("resolving hostname for default device name (pass --name explicitly instead): %w", err)
		}
		deviceName = hostname
	}

	pairingToken, err := readPairingToken()
	if err != nil {
		return fmt.Errorf("reading pairing token: %w", err)
	}
	if pairingToken == "" {
		return fmt.Errorf("no pairing token provided")
	}

	result, err := deviceapi.Pair(cmd.Context(), authWorkerURL, pairingToken, deviceName, "cli")
	if err != nil {
		return fmt.Errorf("pairing failed: %w", err)
	}

	if err := clicreds.Save(&clicreds.Credentials{
		WorkerURL:  authWorkerURL,
		Token:      result.Token,
		DeviceID:   result.DeviceID,
		DeviceName: result.DeviceName,
	}); err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}

	fmt.Printf("Paired successfully as %q (device id %d). You can now use `recueil enqueue`.\n",
		result.DeviceName, result.DeviceID)
	return nil
}

// readPairingToken prompts interactively with no echo when stdin is a
// real terminal, and reads a single line from stdin directly otherwise -- so
// `echo "$TOKEN" | recueil auth --url ...` works for scripting without
// ever needing a --token flag, which would be visible in shell history
// and `ps` output for the whole system.
func readPairingToken() (string, error) {
	if isatty.IsTerminal(os.Stdin.Fd()) {
		fmt.Fprint(os.Stderr, "Pairing token: ")
		tokenBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr) // ReadPassword consumes the Enter keypress without echoing a newline; print one so the next output line doesn't run into the prompt
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(tokenBytes)), nil
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
