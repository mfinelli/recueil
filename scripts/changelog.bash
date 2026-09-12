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

# pulls the current release changelog entry
# usage: ./changelog.bash

MDQ_URL=https://github.com/yshavit/mdq
MDQ_VERSION=0.10.0

if [[ $# -ne 0 ]]; then
  echo >&2 "usage: $(basename "$0")"
  exit 1
fi

sdir="$(pwd)"
wdir=$(mktemp -d)

cd "$wdir"

wget $MDQ_URL/releases/download/v$MDQ_VERSION/mdq-linux-x64.tar.gz
tar xf mdq-linux-x64.tar.gz
mv mdq /usr/local/bin

cd "$sdir"
rm -rf "$wdir"

mdq "#{2} v$(jq -r .version package.json)" CHANGELOG.md |
  perl -0777 -pe 's/^(.*?)\n//; s/^\s*\n+//; s/\n+\s*$//' |
  awk '{print}' > /tmp/release-notes.md

if [[ ! -s /tmp/release-notes.md ]]; then
  echo >&2 "error: no changelog entry found"
  exit 1
fi

exit 0
