VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS  = -s -w -X main.Version=$(VERSION)
OUTDIR   = dist

# Primary target: windows/amd64 (runs on x64 and ARM via emulation)
build-amd64:
	GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" \
	    -o $(OUTDIR)/mdbapi_windows_amd64.exe ./cmd/mdbapi

# Secondary target: native ARM64 Windows (experimental — ACE driver support unvalidated)
build-arm64:
	GOOS=windows GOARCH=arm64 go build -ldflags="$(LDFLAGS)" \
	    -o $(OUTDIR)/mdbapi_windows_arm64.exe ./cmd/mdbapi

# tsnet-enabled variants (requires -tags tsnet; adds ~20 MB)
build-amd64-tsnet:
	GOOS=windows GOARCH=amd64 go build -tags tsnet -ldflags="$(LDFLAGS)" \
	    -o $(OUTDIR)/mdbapi_windows_amd64_tsnet.exe ./cmd/mdbapi

build-arm64-tsnet:
	GOOS=windows GOARCH=arm64 go build -tags tsnet -ldflags="$(LDFLAGS)" \
	    -o $(OUTDIR)/mdbapi_windows_arm64_tsnet.exe ./cmd/mdbapi

# Phase 0 probe tool (must run on windows/amd64 to access ODBC)
build-probe:
	GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" \
	    -o $(OUTDIR)/probe_windows_amd64.exe ./cmd/probe

build-all: build-amd64 build-arm64 build-amd64-tsnet build-probe

# Unit tests (no ODBC required)
test:
	go test ./... -short

# Integration tests (requires Windows with Access ODBC driver installed)
test-integration:
	go test ./... -run Integration -v -tags integration

# Vet + staticcheck
lint:
	go vet ./...

clean:
	rm -rf $(OUTDIR)

$(OUTDIR):
	mkdir -p $(OUTDIR)

build-amd64 build-arm64 build-amd64-tsnet build-arm64-tsnet build-probe build-all: | $(OUTDIR)

.PHONY: build-amd64 build-arm64 build-amd64-tsnet build-arm64-tsnet build-probe \
        build-all test test-integration lint clean
