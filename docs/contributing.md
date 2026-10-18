# Build and test azath
azath is Go 1.26, module `github.com/maruina/azath`, entry point `cmd/azath/main.go`.

```bash
make build   # build bin/azath (version + commit injected via ldflags)
make test    # go test ./... -v -race -count=1
make lint    # go vet + golangci-lint
make vet     # go vet only
make clean   # remove bin/
```

Run `make test` before marking work complete. It runs the Go tests, not the Docker suite. For changes to the Telegram gate, client/server wiring, endpoints, or Synology boot helper, also run the [Docker end-to-end suite](testing.md). The `Docker E2E` job in [CI](../.github/workflows/ci.yml) runs it on pull requests to `main` and pushes to `main`.

The Docker suite uses real azath binaries and Caddy/TLS with simulated Telegram and mount operations. It does not establish DSM hardware compatibility. See the [Synology guide](install-synology.md) for the validation target and remaining live checks.

Code conventions, invariants, and validation commands are in [AGENTS.md](../AGENTS.md).
