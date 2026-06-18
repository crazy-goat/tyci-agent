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

install: build
	cp $(BINARY) ~/local/bin/

install-local: build
	mkdir -p ~/.local/bin
	cp $(BINARY) ~/.local/bin/

clean:
	rm -f $(BINARY)
