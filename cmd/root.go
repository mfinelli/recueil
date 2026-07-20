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
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:     "recueil",
	Short:   "Recueil backend server and admin CLI",
	Version: "1.0.0",
}

func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		return 1
	}
	return 0
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "",
		"path to a TOML config file (optional)")
	if err := rootCmd.MarkPersistentFlagFilename("config", "toml"); err != nil {
		panic(err)
	}

	bindEnvOrPanic(
		"database_url",
		"listen_addr",
		"worker_url",
		"worker_service_secret",
		"session_cookie_secure",
		"pairing_token_key",
		"cloudflare_account_id",
		"cloudflare_d1_database_id",
		"cloudflare_api_token",
		"archive_dir",
		"r2_account_id",
		"r2_bucket_name",
		"r2_access_key_id",
		"r2_access_key_secret",
		"agent_worker_poll_interval_seconds",
		"agent_local_poll_interval_seconds",
		"screenshot_sidecar_url",
		"screenshot_render_host",
		"screenshot_worker_concurrency",
		"screenshot_max_attempts",
	)
}

func bindEnvOrPanic(keys ...string) {
	for _, k := range keys {
		if err := viper.BindEnv(k); err != nil {
			panic(err)
		}
	}
}

// initConfig wires up viper: TOML only, no format auto-detection, and no
// automatic search of $HOME or the working directory (a config file is
// either explicitly named via --config or not used at all).
func initConfig() {
	viper.SetConfigType("toml")
	viper.AutomaticEnv()

	if cfgFile == "" {
		return
	}

	viper.SetConfigFile(cfgFile)
	if err := viper.ReadInConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "error reading config file %s: %v\n",
			cfgFile, err)
		os.Exit(1)
	}
}
