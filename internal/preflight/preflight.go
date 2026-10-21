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

// Package preflight holds what `server` and `agent` verify about their environment
// before doing anything else, so a misconfiguration fails at startup with a message
// that says what to fix, instead of surfacing later as data going nowhere.
package preflight

import (
	"github.com/mfinelli/recueil/internal/archive"
	"github.com/mfinelli/recueil/internal/config"
	"github.com/mfinelli/recueil/internal/linkicons"
)

// archiveDirHint is appended to the errors about archive_dir itself, so an operator
// who hit one knows what to do about it.
const archiveDirHint = " -- provide that directory (a bind mount, so its contents " +
	"survive the container being recreated) and make it writable by the user recueil runs as"

// StorageDirs verifies that the directories `server` and `agent` write to exist and can
// be written to. Both write to the archive
// (`server` for manual upload, `agent` for ingestion and its jobs), so both need it
// read-write; the links store is checked only while links are on, as it isn't used
// otherwise. archive_dir is checked first, and neither directory is ever created: a
// missing one means a bind mount is missing, and creating it would hide that.
func StorageDirs(cfg *config.Config) error {
	if err := archive.New(cfg.ArchiveDir).Check("archive_dir", archiveDirHint); err != nil {
		return err
	}
	if cfg.EnableLinks {
		return linkicons.New(cfg.LinksDir).Check()
	}
	return nil
}
