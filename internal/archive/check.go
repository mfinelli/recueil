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

package archive

import (
	"fmt"
	"os"
)

// Check verifies the store's root exists, is a directory, and can be written to. It's
// meant for startup, so that a missing bind mount or a directory with the wrong owner
// fails loudly instead of showing up later as captures that vanish when the container is
// recreated, or an agent that fails every cycle.
//
// It never creates the root. NewCapture's MkdirAll would quietly create a missing one,
// which in a container means a directory in the writable layer where a bind mount was
// supposed to be: a missing directory is exactly the signal that a mount is missing.
//
// setting names the configuration key the directory came from (archive_dir, links_dir),
// so the message says which one to fix, and hint is appended to the errors about the
// directory itself: what to do about it differs by store, and an operator who upgraded
// without reading the release notes needs it spelled out. A crash between creating and
// removing the write probe leaves at most one empty .check-* file, which `recueil gc`
// reclaims like any other file nothing references.
func (s *Store) Check(setting, hint string) error {
	info, err := os.Stat(s.root)
	if err != nil {
		return fmt.Errorf("%s %q is not usable: %w%s", setting, s.root, err, hint)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s %q is not a directory%s", setting, s.root, hint)
	}
	probe, err := os.CreateTemp(s.root, ".check-*")
	if err != nil {
		return fmt.Errorf("%s %q is not writable: %w%s", setting, s.root, err, hint)
	}
	name := probe.Name()
	_ = probe.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("%s %q: removing write probe: %w", setting, s.root, err)
	}
	return nil
}
