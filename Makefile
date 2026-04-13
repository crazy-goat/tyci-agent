.PHONY: build install clean

build:
	go build -ldflags "-s -w" -o tyci-agent .

install: build
	cp tyci-agent ~/local/bin/

clean:
	rm -f tyci-agent
