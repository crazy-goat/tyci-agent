# Go build configuration
BINARY=tyci

# Version stamped into the binary and reported by `tyci --version`. A release
# passes the tag explicitly (`make release VERSION=v1.2.3`); by default it is
# the nearest git tag, with a distance/dirty suffix when the tree has moved on
# (`git describe`), or "dev" outside a git checkout.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# The version reaches the compiler through the environment, never through the
# recipe text. `git describe` output can legally contain backticks, `$(...)`
# and quotes, and Make substitutes `$(VERSION)` into the recipe before the
# shell parses it — a double-quoted `$(VERSION)` would then run command
# substitution. Exported like this the tag is data the shell expands, not
# source it evaluates.
export TYCI_BUILD_VERSION := $(VERSION)

.PHONY: build release minimal clean install install-local lint

# Static analysis, linters and formatter check (see AGENTS.md)
lint:
	bin/lint.sh

# Debug build (with debug symbols, no optimizations)
build:
	go build \
		-gcflags "all=-N -l" \
		-ldflags "-X main.version=$${TYCI_BUILD_VERSION}" \
		-o $(BINARY) .

# Optimized release build (stripped, optimized, trimmed paths)
release:
	go build \
		-ldflags "-s -w -X main.version=$${TYCI_BUILD_VERSION}" \
		-trimpath \
		-o $(BINARY) .

# Minimal build: no anthropic, no gemini, stripped
minimal:
	go build \
		-tags "noanthropic nogemini" \
		-ldflags "-s -w -X main.version=$${TYCI_BUILD_VERSION}" \
		-trimpath \
		-o $(BINARY) .

# NOTE: use `install` (temp file + atomic rename → fresh inode), not `cp`.
# Overwriting a code-signed binary in place with `cp` reuses the inode, so on
# Apple Silicon the kernel's cached code signature (CDHash) for that inode no
# longer matches the new bytes and it SIGKILLs the binary at exec ("killed: 9"),
# even though `codesign --verify` passes. A fresh inode avoids the stale cache.
install: build
	mkdir -p ~/local/bin
	install -m 0755 $(BINARY) ~/local/bin/$(BINARY)

install-local: release
	mkdir -p ~/.local/bin
	install -m 0755 $(BINARY) ~/.local/bin/tyci

clean:
	rm -f $(BINARY)
