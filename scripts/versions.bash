#!/usr/bin/env bash

# recueil: self-hosted webpage bookmarker and archiver
# Copyright © 2026 Mario Finelli
#
# This program is free software: you can redistribute it and/or modify
# it under the terms of the GNU Affero General Public License as published by
# the Free Software Foundation, either version 3 of the License, or
# (at your option) any later version.
#
# This program is distributed in the hope that it will be useful,
# but WITHOUT ANY WARRANTY; without even the implied warranty of
# MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
# GNU Affero General Public License for more details.
#
# You should have received a copy of the GNU Affero General Public License
# along with this program. If not, see <https://www.gnu.org/licenses/>.

set -e

# checks that all of our version strings are the same
# usage: ./versions.bash

if [[ $# -ne 0 ]]; then
  echo >&2 "usage: $(basename "$0")"
  exit 1
fi

function myversion() {
  echo "CHECKING MY VERSION"
  local packagejson cmdroot

  packagejson="$(jq -r .version package.json)"
  cmdroot="$(grep -m1 Version: cmd/root.go | awk -F\" '{print $2}')"

  if [[ $packagejson != "$cmdroot" ]]; then
    echo >&2 "error: cmd/root.go version mismatch"
    exit 1
  fi

  echo "MY VERSION OK"
}

myversion

exit 0
