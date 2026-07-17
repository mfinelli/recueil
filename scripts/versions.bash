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
  local dockerfile dockerlabel cmdroot packagejson

  packagejson="$(jq -r .version package.json)"
  cmdroot="$(grep -m1 Version: cmd/root.go | awk -F\" '{print $2}')"
  dockerlabel=org.opencontainers.image.version
  dockerfile="$(grep $dockerlabel Dockerfile | awk -F= '{print $2}')"
  extpackagejson="$(jq -r .version extension/package.json)"
  extmanifestjson="$(jq -r .version extension/manifest.base.json)"

  if [[ $packagejson != "$cmdroot" ]]; then
    echo >&2 "error: cmd/root.go version mismatch"
    exit 1
  fi

  if [[ v$packagejson != "$dockerfile" ]]; then
    echo >&2 "error: Dockerfile version mismatch"
    exit 1
  fi

  if [[ $packagejson != "$extpackagejson" ]]; then
    echo >&2 "error: extension/package.json version mismatch"
    exit 1
  fi

  if [[ $packagejson != "$extmanifestjson" ]]; then
    echo >&2 "error: extension/manifest.base.json version mismatch"
    exit 1
  fi

  echo "MY VERSION OK"
}

function sqlc() {
  echo "CHECKING SQLC VERSION"
  local dockerfile github ghsqlc

  ghsqlc=sqlc-dev/setup-sqlc@v5
  github="$(yq e ".jobs.main.steps[] | select(.uses == \"$ghsqlc\") | \
    .with.sqlc-version" .github/workflows/default.yml)"

  if [[ -z $github ]]; then
    echo >&2 "error: can't get sqlc version from github workflow"
    echo >&2 "       did you update the version of the action? you need to"
    echo >&2 "       update this script too"
    exit 1
  fi

  dockerfile="$(grep "RUN go install github.com/sqlc-dev/sqlc" Dockerfile |
    awk -F@ '{print $2}')"

  if [[ v$github != "$dockerfile" ]]; then
    echo >&2 "error: Dockerfile version mismatch"
    exit 1
  fi

  echo "SQLC VERSION OK"
}

function gover() {
  echo "CHECKING GO VERSION"
  local dockerfile gomod

  gomod="$(grep "^go " go.mod | awk '{print $2}')"
  dockerfile="$(grep "AS buildgo" Dockerfile | awk '{print $2}' |
    awk -F: '{print $2}')"

  if [[ ${gomod}-alpine != "$dockerfile" ]]; then
    echo >&2 "error: Dockerfile version mismatch"
    exit 1
  fi

  echo "GO VERSION OK"
}

myversion
sqlc
gover

exit 0
