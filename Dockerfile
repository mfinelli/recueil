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

FROM alpine AS source
WORKDIR /app
COPY . /app

FROM golang:alpine AS gotools
WORKDIR /app
RUN go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1

FROM node:lts-alpine AS buildjs
WORKDIR /app
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml /app/
RUN pnpm install --frozen-lockfile

FROM golang:1.26.5-alpine AS buildgo
ARG GITSHA
WORKDIR /app
RUN apk add coreutils gawk gcc git jq make musl-dev
COPY --from=gotools /go/bin/sqlc /usr/local/bin/sqlc
COPY --from=buildjs /app/node_modules /app/node_modules
COPY . /app/
RUN make internal/db/db.go
RUN go mod vendor
RUN GITSHA=$GITSHA make

FROM alpine
LABEL org.opencontainers.image.title=recueil
LABEL org.opencontainers.image.version=v1.0.0
LABEL org.opencontainers.image.description="webpage bookmarker and archiver"
LABEL org.opencontainers.image.url=https://recueil.app
LABEL org.opencontainers.image.source=https://github.com/mfinelli/recueil
LABEL org.opencontainers.image.licenses=AGPL-3.0-or-later
RUN addgroup -S recueil && adduser -S recueil -G recueil
COPY --from=source /app /usr/src/recueil
COPY --from=buildgo /app/vendor /usr/src/recueil/vendor
COPY --from=buildgo /app/internal/db /usr/src/recueil/internal/db
COPY --from=buildgo /app/recueil /usr/bin/
USER recueil
