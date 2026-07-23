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

package main

import (
	"embed"
	"os"

	"github.com/mfinelli/recueil/cmd"
)

//go:embed migrations/*.sql
var postgresMigrationsFS embed.FS

//go:embed terraform/worker/migrations/*.sql
var d1MigrationsFS embed.FS

//go:embed node_modules/@mozilla/readability/Readability.js
var readabilityJS string
var readabilityVersion string

// all: prefix, not a plain "dist/*" -- go:embed's default pattern
// excludes files/directories starting with "." or "_", which a real Vite
// build doesn't produce, but is a needless foot-gun to leave unhandled if
// that ever changes (a hashed asset filename starting with "_" isn't
// something Vite is contractually forbidden from ever generating).
//
//go:embed all:dist
var dashboardFS embed.FS

var commit string
var date string
var version string

func main() {
	cmd.Commit = commit
	cmd.Date = date
	cmd.Version = version
	cmd.PostgresMigrationsFS = postgresMigrationsFS
	cmd.D1MigrationsFS = d1MigrationsFS
	cmd.ReadabilityJS = readabilityJS
	cmd.ReadabilityVersion = readabilityVersion
	cmd.DashboardFS = dashboardFS

	if r := cmd.Execute(); r != 0 {
		os.Exit(r)
	}
}
