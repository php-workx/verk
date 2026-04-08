# verk project quality gate
# Single source of truth for "my code is clean" — hooks and CI delegate here.

# Pinned tool versions — keep in sync with CI
golangci_lint_ver := "v2.11.3"
gofumpt_ver := "v0.7.0"
govulncheck_ver := "v1.1.4"

version := `git describe --tags --always --dirty 2>/dev/null || echo "dev"`
commit := `git rev-parse --short HEAD 2>/dev/null || echo "unknown"`
build_date := `date -u +"%Y-%m-%dT%H:%M:%SZ"`
ldflags := "-X main.Version=" + version + " -X main.GitCommit=" + commit + " -X main.BuildDate=" + build_date

default:
    @just --list

# --- Quality gates ---

# Pre-commit: local checks + tests (~30s)
pre-commit: fmt vet lint build-check mod-tidy test

# Full quality gate: pre-commit + vuln
check: pre-commit vuln

# Full dev suite: quality gate + sonar
dev: check sonar
    @echo "All checks passed!"

# --- Static analysis ---

# Check formatting with gofumpt (detect-only, no auto-fix)
fmt:
    @command -v gofumpt >/dev/null 2>&1 || (echo "gofumpt not installed (run: just install-dev)" && exit 1)
    @test -z "$(gofumpt --extra -l .)" || (echo "gofumpt: unformatted files:" && gofumpt --extra -l . && exit 1)

# Go vet
vet:
    go vet ./...

# Lint with golangci-lint
lint:
    @command -v golangci-lint >/dev/null 2>&1 || (echo "golangci-lint not installed (run: just install-dev)" && exit 1)
    golangci-lint run

# --- Security ---

# Scan for leaked secrets
gitleaks:
    @if command -v gitleaks >/dev/null 2>&1; then \
        gitleaks git --no-banner; \
    else \
        echo "warning: gitleaks not installed, skipping secret scan"; \
    fi

# Scan for known vulnerabilities in dependencies
vuln:
    @if command -v govulncheck >/dev/null 2>&1; then \
        govulncheck ./...; \
    else \
        echo "govulncheck not installed, skipping (run: just install-dev)"; \
    fi

# --- Testing ---

# Verify the project compiles (fast, no binary output)
build-check:
    go build ./...

# Verify go.mod and go.sum are tidy (detect-only)
mod-tidy:
    @cp go.mod go.mod.bak
    @if [ -f go.sum ]; then cp go.sum go.sum.bak; fi
    @go mod tidy
    @DIRTY=0; \
        diff -q go.mod go.mod.bak >/dev/null 2>&1 || DIRTY=1; \
        if [ -f go.sum.bak ]; then diff -q go.sum go.sum.bak >/dev/null 2>&1 || DIRTY=1; \
        elif [ -f go.sum ]; then DIRTY=1; fi; \
        mv go.mod.bak go.mod; \
        if [ -f go.sum.bak ]; then mv go.sum.bak go.sum; elif [ -f go.sum ]; then rm go.sum; fi; \
        if [ "$$DIRTY" = "1" ]; then echo "go.mod/go.sum not tidy — run 'go mod tidy'" && exit 1; fi

# Run all tests with race detector (no cache)
test:
    go test -race -count=1 ./...

# Run tests with coverage report
cover:
    go test -race -coverprofile=coverage.out -covermode=atomic ./...
    go tool cover -html=coverage.out -o coverage.html
    @echo "Coverage report: coverage.html"

# --- External analysis ---

# Run SonarQube scan (requires SONAR_TOKEN in .env and local SonarQube on localhost:9000)
sonar:
    @if ! command -v sonar-scanner >/dev/null 2>&1; then \
        echo "sonar-scanner not installed, skipping"; \
    elif [ ! -f .env ]; then \
        echo ".env missing, skipping sonar scan"; \
    else \
        TOKEN=$(grep -E '^SONAR_TOKEN=[A-Za-z0-9_]+$$' .env | cut -d= -f2); \
        if [ -z "$$TOKEN" ]; then \
            echo "error: SONAR_TOKEN not found or invalid in .env"; exit 1; \
        fi; \
        SONAR_TOKEN="$$TOKEN" sonar-scanner -Dsonar.qualitygate.wait=true; \
    fi

# --- Build targets ---

# Build the verk binary with version info
build:
    mkdir -p bin
    go build -ldflags '{{ldflags}}' -o bin/verk ./cmd/verk

# Install verk to $GOPATH/bin (or $GOBIN)
install:
    go install -ldflags '{{ldflags}}' ./cmd/verk

# --- Setup ---

# Format all Go files in-place (use when `just fmt` fails)
format:
    gofumpt --extra -w .

# Set up development environment
setup: install-dev
    @echo "Development environment ready."

# Install required development tools (pinned versions)
install-dev:
    @echo "Installing Go tools..."
    go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@{{golangci_lint_ver}}
    go install mvdan.cc/gofumpt@{{gofumpt_ver}}
    go install golang.org/x/vuln/cmd/govulncheck@{{govulncheck_ver}}
    @echo "Done!"

# Remove build artifacts
clean:
    rm -rf bin/ coverage.out coverage.html .verk/runs/
    go clean
