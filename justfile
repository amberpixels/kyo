import? 'justfile.local'

binary_name := "kyo"
build_dir   := "bin"
binary      := build_dir / binary_name
cmd_dir     := "./cmd/kyo"
version     := `git describe --tags --always --dirty 2>/dev/null || echo dev`

standardgo := "github.com/amberpixels/standardgo/cmd/standardgo@latest"

# Default: list available commands
[private]
default:
    @just --list

# --- Build & run ---

# Build the binary into bin/
[group('dev')]
build:
    @mkdir -p {{build_dir}}
    go build -ldflags "-X main.version={{version}}" -o {{binary}} {{cmd_dir}}

# Install the binary to GOPATH/bin
[group('dev')]
install:
    go install -ldflags "-X main.version={{version}}" {{cmd_dir}}

# Run the wrapper with ansible-playbook args, e.g. `just run site.yml --check`
[group('dev')]
run *ARGS: build
    ./{{binary}} {{ARGS}}

# Run the fixture playbook on this machine as a plan - no remote hosts
[group('dev')]
demo: build
    KYO_TEST_DIR="$(mktemp -d)" ANSIBLE_LOCALHOST_WARNING=false ./{{binary}} -i testdata/inventory.ini --check --diff testdata/playbooks/basic.yml

# Remove build artifacts
[group('dev')]
clean:
    rm -rf {{build_dir}} dist

# --- Formatting & linting ---
#
# Linting is standardgo: ruleset and golangci-lint engine in one binary, so
# there is no .golangci.yml here (it would not be read). Same convention as ele:
# `fmt` rewrites, `fix` applies judgement, `lint` only reports.

# Format Go code - rewrites to canonical form
[group('fmt')]
[no-exit-message]
fmt:
    go run {{ standardgo }} fmt ./...
    go mod tidy

# Auto-fix lint findings - mutates and makes judgement calls
[group('fix')]
[no-exit-message]
fix:
    go run {{ standardgo }} ./... --fix

# Lint Go code with standardgo - reports findings, changes nothing
[group('lint')]
[no-exit-message]
lint: vet
    go run {{ standardgo }} ./...

# Vet - the real `go vet` across all packages - immutable
[group('lint')]
vet:
    go vet ./...

# --- Testing ---

# Run all tests, e.g. `just test -run Runner -v`. Tests that need ansible-playbook skip without it
[group('test')]
test *ARGS:
    go test {{ARGS}} ./...

# --- CI ---

# Everything CI runs, in CI's order (build, lint, test) - immutable
[group('ci')]
ci: build lint test

alias t := test
alias b := build
