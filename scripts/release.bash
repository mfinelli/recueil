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

# builds github release artifacts
# usage: ./release.bash

if [[ $# -ne 0 ]]; then
  echo >&2 "usage: $(basename "$0")"
  exit 1
fi

pnpm ci
sqlc generate
pnpm run build
go mod vendor

go-licenses save . --ignore github.com/mfinelli/recueil --save_path licenses \
  || true
pnpm exec license-checker-rseidelsohn --production --files licenses
find licenses -type f -exec chmod 0644 {} \;

bname="recueil_${GITHUB_REF_NAME//\//-}"

mkdir "${bname}"
git archive HEAD | tar -x -C "${bname}"

(
  cd internal/urlnorm/clearurls-rules
  git archive HEAD | tar -x -C "../../../${bname}/internal/urlnorm/clearurls-rules/"
)

(
  cd "${bname}"
  sqlc generate
)

cp -r dist "${bname}"
cp -r vendor "${bname}"
cp -r node_modules "${bname}"
cp -r extension/node_modules "${bname}/extension"
[[ -d terraform/worker/node_modules ]] && cp -r terraform/worker/node_modules \
  "${bname}/terraform/worker"
[[ -d www/node_modules ]] && cp -r www/node_modules "${bname}/www"

tar --owner=0 --group=0 --sort=name -cavf "${bname}.tar.zst" "${bname}"
if [[ ${GITHUB_EVENT_NAME} != pull_request ]]; then
  gpg -u ci@recueil.app -ba "${bname}.tar.zst"
fi

make
./recueil completion bash > recueil.bash
./recueil completion fish > recueil.fish
./recueil completion zsh > recueil.zsh

mkdir "${bname}_amd64"
mkdir "${bname}_arm64"

mv recueil "${bname}_amd64"
export CC=aarch64-linux-gnu-gcc
export GOARCH=arm64
make recueil
mv recueil "${bname}_arm64"

for arch in amd64 arm64; do
  cp CHANGELOG.md "${bname}_${arch}"
  cp LICENSE "${bname}_${arch}"
  cp -r licenses "${bname}_${arch}"
  cp recueil.1 "${bname}_${arch}"
  cp recueil.{bash,fish,zsh} "${bname}_${arch}"

  tar --owner=0 --group=0 --sort=name -cavf "${bname}_${arch}.tar.zst" \
    "${bname}_${arch}"
  if [[ ${GITHUB_EVENT_NAME} != pull_request ]]; then
    gpg -u ci@recueil.app -ba "${bname}_${arch}.tar.zst"
  fi
done

(
  cd extension
  pnpm run package
  mv dist/packages/recueil-firefox.xpi "../${bname}.xpi"
  if [[ ${GITHUB_EVENT_NAME} != pull_request ]]; then
    gpg -u ci@recueil.app -ba "../${bname}.xpi"
  fi
)

sha256sum -b ./*.tar.zst > "${bname}.sha256"
sha256sum -b ./*.xpi > "${bname}.sha256"
if [[ ${GITHUB_EVENT_NAME} != pull_request ]]; then
  gpg -u ci@recueil.app -ba "${bname}.sha256"
fi

exit 0
