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

[private]
default:
  @just --list

compose PROFILE:
  docker compose --profile={{ PROFILE }} up

create-migration NAME:
  goose -dir migrations -s create {{ NAME }} sql

fmt:
  go fmt ./...
  pnpm run fmt
  tofu fmt -recursive

lint:
  errcheck -ignoregenerated ./...
  go-critic check -checkGenerated=false -checkTests=true -enableAll ./...
  staticcheck ./...
  pnpm run lint

serve:
  make all
  ./recueil server --config local.toml

test:
  go test ./...
  pnpm run test
