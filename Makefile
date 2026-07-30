VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
DIST := dist

.PHONY: dev dist dev-desktop dist-desktop clean fmt vet test

dev:
	go build -o lightsync ./cmd/lightsync

dist:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/lightsync-linux-amd64     ./cmd/lightsync
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/lightsync-linux-arm64     ./cmd/lightsync
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/lightsync-windows-amd64.exe ./cmd/lightsync
	cd $(DIST) && sha256sum lightsync-* > SHA256SUMS

# lightsync-desktop (feature/astilectron-wrapper branch only): wraps the same
# core in an Electron shell via go-astilectron, for capture without browser
# variability. --headless reproduces plain `lightsync` exactly. First launch
# (non-headless) downloads Electron at runtime — needs internet access.
dev-desktop:
	go build -o lightsync-desktop ./cmd/lightsync-desktop

dist-desktop:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/lightsync-desktop-linux-amd64     ./cmd/lightsync-desktop
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/lightsync-desktop-linux-arm64     ./cmd/lightsync-desktop
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/lightsync-desktop-windows-amd64.exe ./cmd/lightsync-desktop
	cd $(DIST) && sha256sum lightsync-desktop-* >> SHA256SUMS

clean:
	rm -rf $(DIST) lightsync lightsync-desktop

fmt:
	gofmt -l -w .

vet:
	go vet ./...

test:
	go test ./...
