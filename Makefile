VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
DIST := dist

.PHONY: dev dist dev-desktop dist-desktop clean fmt vet test

dev:
	go build -o huemux ./cmd/huemux

dist:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/huemux-linux-amd64     ./cmd/huemux
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/huemux-linux-arm64     ./cmd/huemux
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/huemux-windows-amd64.exe ./cmd/huemux
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/huemux-darwin-amd64    ./cmd/huemux
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/huemux-darwin-arm64    ./cmd/huemux
	cd $(DIST) && sha256sum huemux-* > SHA256SUMS

# huemux-desktop (feature/astilectron-wrapper branch only): wraps the same
# core in an Electron shell via go-astilectron, for capture without browser
# variability. --headless reproduces plain `huemux` exactly. First launch
# (non-headless) downloads Electron at runtime — needs internet access.
dev-desktop:
	go build -o huemux-desktop ./cmd/huemux-desktop

dist-desktop:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/huemux-desktop-linux-amd64     ./cmd/huemux-desktop
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/huemux-desktop-linux-arm64     ./cmd/huemux-desktop
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/huemux-desktop-windows-amd64.exe ./cmd/huemux-desktop
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/huemux-desktop-darwin-amd64    ./cmd/huemux-desktop
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/huemux-desktop-darwin-arm64    ./cmd/huemux-desktop
	cd $(DIST) && sha256sum huemux-desktop-* >> SHA256SUMS

clean:
	rm -rf $(DIST) huemux huemux-desktop

fmt:
	gofmt -l -w .

vet:
	go vet ./...

test:
	go test ./...
