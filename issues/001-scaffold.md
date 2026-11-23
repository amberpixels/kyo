# Scaffold the repo

Labels: skeleton, chore

Mirror ele's layout so both repos feel the same.

- [x] `go.mod` (`github.com/amberpixels/kyo`), MIT `LICENSE`
- [x] `justfile`: build, install, run, test, vet, lint (standardgo via `go run @latest`, no `.golangci.yml`), fmt, fix, ci
- [x] `.gitignore`: logs and recorded event files carry real host and task names, never commit them
- [x] `.github/workflows/ci.yml`: `just ci`
- [x] `cmd/kyo/main.go` with `--version`
- [x] `just lint` clean (standardgo, 0 issues)
