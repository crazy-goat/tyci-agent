# Go build configuration
BINARY=tyci-agent

.PHONY: build release clean install

# Debug build (with debug symbols, no optimizations)
build:
	go build \
		-gcflags "all=-N -l" \
		-o $(BINARY) .

# Optimized release build (stripped, optimized)
release:
	go build \
		-ldflags "-s -w" \
		-o $(BINARY) .

install: build
	cp $(BINARY) ~/local/bin/

clean:
	rm -f $(BINARY)
