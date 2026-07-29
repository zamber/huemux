VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
DIST := dist

.PHONY: dev dist clean fmt vet test

dev:
	go build -o lightsync ./cmd/lightsync

dist:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/lightsync-linux-amd64     ./cmd/lightsync
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/lightsync-linux-arm64     ./cmd/lightsync
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/lightsync-windows-amd64.exe ./cmd/lightsync
	cd $(DIST) && sha256sum lightsync-* > SHA256SUMS

clean:
	rm -rf $(DIST) lightsync

fmt:
	gofmt -l -w .

vet:
	go vet ./...

test:
	go test ./...
