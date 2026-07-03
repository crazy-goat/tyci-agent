# Go build configuration
BINARY=tyci

.PHONY: build release minimal clean install install-local

# Debug build (with debug symbols, no optimizations)
build:
	go build \
		-gcflags "all=-N -l" \
		-o $(BINARY) .

# Optimized release build (stripped, optimized, trimmed paths)
release:
	go build \
		-ldflags "-s -w" \
		-trimpath \
		-o $(BINARY) .

# Minimal build: no anthropic, no gemini, stripped
minimal:
	go build \
		-tags "noanthropic nogemini" \
		-ldflags "-s -w" \
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
