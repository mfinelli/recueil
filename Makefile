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

SOURCES := $(shell find . -name '*.go' -not -name '*_test.go')
MIGRATIONS := $(wildcard migrations/*.sql)
QUERIES := $(wildcard queries/*.sql)

DATE := date
GIT := git
GO := go
JQ := jq
SQLC := sqlc

ifeq ($(shell uname), Darwin)
        DATE := gdate
endif

all: recueil

clean:
	rm -rf recueil

recueil: $(SOURCES) internal/db/db.go
	$(GO) build -o $@ \
		-trimpath \
		-mod=readonly \
		-ldflags "-s -w -linkmode=external \
			-X main.commit=$(shell $(GIT) rev-parse --short HEAD) \
			-X main.date=$(shell $(DATE) --utc --iso-8601=seconds) \
			-X main.version=$(shell $(JQ) -r .version package.json)" \
		main.go

internal/db/db.go: $(MIGRATIONS) $(QUERIES) sqlc.yaml
	$(SQLC) generate

.PHONY: all clean
