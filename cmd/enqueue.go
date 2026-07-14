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
	"net/url"
	"os"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/mfinelli/recueil/internal/clicreds"
	"github.com/mfinelli/recueil/internal/deviceapi"
)

var enqueueCmd = &cobra.Command{
	Use:   "enqueue <url> [<url>...]",
	Short: "Enqueue one or more URLs for capture",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runEnqueue,
}

func init() {
	rootCmd.AddCommand(enqueueCmd)
}

func runEnqueue(cmd *cobra.Command, args []string) error {
	creds, err := clicreds.Load()
	if err != nil {
		if errors.Is(err, clicreds.ErrNotFound) {
			return fmt.Errorf("not paired with a Worker yet -- run `recueil auth --url <worker-url>` first")
		}
		return fmt.Errorf("loading credentials: %w", err)
	}

	client := deviceapi.NewClient(creds.WorkerURL, creds.Token)

	var failed int
	for _, rawURL := range args {
		if !looksLikeAURL(rawURL) {
			fmt.Fprintf(os.Stderr, "skipping %q: not a valid URL\n", rawURL)
			failed++
			continue
		}

		id := uuid.NewString()
		if err := client.Enqueue(cmd.Context(), id, rawURL); err != nil {
			fmt.Fprintf(os.Stderr, "failed to enqueue %q: %v\n", rawURL, err)
			failed++
			continue
		}
		fmt.Printf("enqueued %s\n", rawURL)
	}

	if failed > 0 {
		return fmt.Errorf("%d of %d url(s) failed to enqueue", failed, len(args))
	}
	return nil
}

// looksLikeAURL is a light client-side sanity check, not a substitute for
// the Worker's own validation (handleEnqueue's `new URL(url)`, which
// still runs regardless): failing fast on an obviously-malformed argument
// avoids a pointless round trip, but the Worker remains the actual source
// of truth for what counts as a valid URL.
func looksLikeAURL(raw string) bool {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return false
	}
	return parsed.Scheme != "" && parsed.Host != ""
}
