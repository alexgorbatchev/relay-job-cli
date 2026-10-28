set dotenv-load := false
export GOWORK := "off"

default:
    @just --list

# Prepare development dependencies
setup:
    go mod download

# Build the CLI
build:
    mkdir -p bin
    go build -o bin/relay-jobs ./cmd/relay-jobs

# Run in human mode
run *args:
    go run ./cmd/relay-jobs {{ args }}

# Run in agent mode
run-ai *args:
    AGENT=1 go run ./cmd/relay-jobs {{ args }}

# Run behavioral tests with the race detector
test:
    go test -count=1 -race ./...

# Check module hygiene and static analysis
lint:
    go mod tidy -diff
    go vet ./...

vet: lint

# Format source
fmt:
    go fmt ./...

# Validate and enforce combined statement coverage
check: lint
    go test -count=1 -race -coverpkg=./... -coverprofile=coverage.out ./...
    go tool cover -func=coverage.out | awk '$1 == "total:" { sub(/%$/, "", $3); found=1; printf "Coverage: %s%%\n", $3; if ($3+0 < 90) exit 1 } END { if (!found) exit 1 }'

# Cross-compile a native macOS worker and test executable
build-macos:
    mkdir -p bin
    GOOS=darwin GOARCH=amd64 go build -o bin/relay-jobs-darwin-amd64 ./cmd/relay-jobs
    GOOS=darwin GOARCH=amd64 go test -c -o bin/relay-jobs-darwin-test ./internal/job
