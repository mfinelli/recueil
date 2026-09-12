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
FRONTEND := $(shell find src -type f -not -name "*.test.ts")
MIGRATIONS := $(wildcard migrations/*.sql)
QUERIES := $(wildcard queries/*.sql)

DATE := date
GIT := git
GO := go
JQ := jq
PNPM := pnpm
SQLC := sqlc

ifeq ($(shell uname), Darwin)
        DATE := gdate
endif

GITSHA ?= $(shell $(GIT) rev-parse --short HEAD)

READABILITY_PACKAGE := node_modules/@mozilla/readability/package.json

# Detect target OS (respect GOOS if set, fall back to host)
TARGET_OS ?= $(shell uname -s | tr '[:upper:]' '[:lower:]')
ifdef GOOS
	TARGET_OS = $(GOOS)
endif

# Detect target architecture (respect GOARCH if set, fall back to host arch)
TARGET_ARCH ?= $(shell uname -m)
ifdef GOARCH
	ifeq ($(GOARCH),arm64)
		TARGET_ARCH = aarch64
	endif
	ifeq ($(GOARCH),amd64)
		TARGET_ARCH = x86_64
	endif
endif

# hardening flags adapted from archlinux makepkg.conf (GNU ld only)
LDFLAGS_linux ?= -Wl,-O1 -Wl,--sort-common -Wl,--as-needed -Wl,-z,relro \
		 -Wl,-z,now -Wl,-z,pack-relative-relocs

# macOS linker (ld64/lld) doesn't support GNU ld flags;
# PIE and ASLR are enforced by the OS; dead_strip ~= --as-needed
LDFLAGS_darwin ?= -Wl,-dead_strip

LDFLAGS ?= $(LDFLAGS_$(TARGET_OS))

# base flags for all architectures (linux only)
CGO_CFLAGS_BASE_linux ?= -O2 -fno-plt -fexceptions \
			 -Wp,-U_FORTIFY_SOURCE,-D_FORTIFY_SOURCE=3 \
			 -Wformat -Werror=format-security \
			 -fstack-clash-protection \
			 -fno-omit-frame-pointer \
			 -mno-omit-leaf-frame-pointer

# macOS: conservative base flags, let the SDK handle hardening
CGO_CFLAGS_BASE_darwin ?= -O2 -fexceptions \
			   -Wformat -Werror=format-security \
			   -fno-omit-frame-pointer

CGO_CFLAGS_BASE ?= $(CGO_CFLAGS_BASE_$(TARGET_OS))

# x86_64 only flags (linux only)
CGO_CFLAGS_x86_64_linux ?= -fcf-protection

# final flags to actually use
CGO_CFLAGS ?= $(CGO_CFLAGS_BASE) $(CGO_CFLAGS_$(TARGET_ARCH)_$(TARGET_OS))

all: recueil

clean:
	rm -rf recueil

recueil: export CGO_ENABLED = 1
recueil: export CGO_CFLAGS := $(CGO_CFLAGS)
recueil: export CGO_LDFLAGS := $(LDFLAGS)
recueil: $(SOURCES) internal/db/db.go dist/index.html
	$(GO) build -o $@ \
		-trimpath \
		-mod=readonly \
		-ldflags "-s -w -linkmode=external \
			-X main.commit=$(GITSHA) \
			-X main.date=$(shell $(DATE) --utc --iso-8601=seconds) \
			-X main.version=$(shell $(JQ) -r .version package.json) \
			-X main.readabilityVersion=$(shell $(JQ) -r .version $(READABILITY_PACKAGE))" \
		main.go

internal/db/db.go: $(MIGRATIONS) $(QUERIES) sqlc.yaml
	$(SQLC) generate

dist/index.html: $(FRONTEND)
	$(PNPM) run build

.PHONY: all clean
