binary := "voom"
bin_dir := "bin"

default:
    @just --list

dev: check

build:
    mkdir -p {{bin_dir}}
    go build -o {{bin_dir}}/{{binary}} ./cmd/{{binary}}

docs:
    go run ./cmd/gen-docs

test:
    go test ./...

vet:
    go vet ./...

lint:
    golangci-lint run
    actionlint

tidy-check:
    go mod tidy
    git diff --exit-code go.mod go.sum

scripts-check:
    @if ls scripts/*.sh >/dev/null 2>&1; then bash -n scripts/*.sh; fi

smoke: build
    ./{{bin_dir}}/{{binary}} version
    ./{{bin_dir}}/{{binary}} --help

release-snapshot-check:
    goreleaser check
    goreleaser release --snapshot --clean --skip=publish
    scripts/verify-release-archives.sh dist
    scripts/smoke-release.sh dist

nix-check:
    nix flake check --show-trace
    nix build .#voom
    test -x result/bin/voom
    nix run .#voom -- version

ci: check release-snapshot-check nix-check

release-prep version:
    scripts/check-release-notes.sh '{{version}}'
    just check
    nix flake check --show-trace
    just release-snapshot-check
    @printf '\nRelease prep passed for {{version}}.\n'
    @printf 'Publish with:\n'
    @printf '  git push origin main\n'
    @printf '  git tag -a {{version}} -m "{{version}}"\n'
    @printf '  git push origin {{version}}\n'

check: docs vet test lint tidy-check scripts-check smoke
    goreleaser check
    git diff --exit-code docs/commands

clean:
    rm -rf {{bin_dir}} dist result
