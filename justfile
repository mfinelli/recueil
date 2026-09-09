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

sed := if os() == "macos" { "gsed" } else { "sed" }

[private]
default:
    @just --list

bump VERSION:
    jq '.version = "{{ VERSION }}"' package.json | sponge package.json
    jq '.version = "{{ VERSION }}"' extension/package.json | \
        sponge extension/package.json
    jq '.version = "{{ VERSION }}"' terraform/worker/package.json | \
        sponge terraform/worker/package.json
    jq '.version = "{{ VERSION }}"' www/package.json | sponge www/package.json
    jq '.version = "{{ VERSION }}"' extension/manifest.base.json | \
        sponge extension/manifest.base.json
    {{ sed }} -i -E "s|(Version:\s+\").*(\",)|\1{{ VERSION }}\2|" cmd/root.go
    {{ sed }} -i -E \
        "s|(LABEL org\.opencontainers\.image\.version=v).*|\1{{ VERSION }}|" \
        Dockerfile
    {{ sed }} -i "s|//terraform?ref=v.*\"|//terraform?ref=v{{ VERSION }}\"|" \
        www/content/docs/operators/deploying-recueil.md
    {{ sed }} -i \
        "s|image: mfinelli/recueil:.*|image: mfinelli/recueil:{{ VERSION }}|" \
        www/content/docs/operators/deploying-recueil.md

compose PROFILE:
    docker compose --profile={{ PROFILE }} up

create-migration NAME:
    goose -dir migrations -s create {{ NAME }} sql

fmt:
    go fmt ./...
    pnpm run fmt
    pnpm run fmt:www
    tofu fmt -recursive
    just --fmt

lint:
    errcheck -ignoregenerated ./...
    go-critic check -checkGenerated=false -checkTests=true -enableAll ./...
    staticcheck ./...
    pnpm run lint
    pnpm run types
    pnpm run --filter=@recueil/extension types
    pnpm run --filter=@recueil/terraform types
    mandoc -Tlint recueil.1

gover VERSION:
    {{ sed }} -i  -E "s|^go .*|go {{ VERSION }}|" go.mod
    {{ sed }} -i "s|FROM golang:.*-alpine|FROM golang:{{ VERSION }}-alpine|" \
        Dockerfile

serve:
    make all
    ./recueil server --config local.toml

agent:
    make all
    ./recueil agent --config local.toml

[private]
www-assets:
    pnpm run --filter=@recueil/www assets

[working-directory('www')]
www-build: www-assets
    zola build --minify

[working-directory('www')]
www-serve: www-assets
    zola serve

test:
    go test -p 1 ./...
    pnpm run test
