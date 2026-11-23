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

# Run the SSH ControlPersist test against a throwaway sshd on 127.0.0.1:PORT, run as you
[group('test')]
test-ssh PORT="2222":
    #!/usr/bin/env bash
    set -euo pipefail
    dir="$(mktemp -d)"
    trap 'kill "$(cat "$dir/pid" 2>/dev/null)" 2>/dev/null || true; rm -rf "$dir"' EXIT
    ssh-keygen -q -t ed25519 -N '' -f "$dir/host_key"
    ssh-keygen -q -t ed25519 -N '' -f "$dir/id"
    cp "$dir/id.pub" "$dir/authorized_keys"
    printf '%s\n' \
        "Port {{PORT}}" \
        "ListenAddress 127.0.0.1" \
        "HostKey $dir/host_key" \
        "AuthorizedKeysFile $dir/authorized_keys" \
        "PidFile $dir/pid" \
        "StrictModes no" \
        "PasswordAuthentication no" \
        "KbdInteractiveAuthentication no" > "$dir/sshd_config"
    # sshd refuses to start unless invoked by absolute path
    sshd="$(command -v sshd || echo /usr/sbin/sshd)"
    "$sshd" -f "$dir/sshd_config" -E "$dir/sshd.log"
    for _ in $(seq 50); do [ -s "$dir/pid" ] && break; sleep 0.1; done
    KYO_SSH_HOST=127.0.0.1 KYO_SSH_PORT={{PORT}} KYO_SSH_KEY="$dir/id" \
        go test -count=1 -run TestSSHControlPersist -v ./internal/runner/ \
        || { cat "$dir/sshd.log"; exit 1; }

# --- CI ---

# Everything CI runs, in CI's order (build, lint, test) - immutable
[group('ci')]
ci: build lint test

alias t := test
alias b := build
