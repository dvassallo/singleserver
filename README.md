<p align="center">
  <a href="https://singleserver.com"><img src="www/server-single-wordmark.svg" alt="single server" width="484"></a>
</p>

<p><strong>Git push to production in &lt;5 seconds.</strong></p>

<p>One Linux box can run every app you've ever shipped. Single Server wires up Cloudflare, Tailscale, Docker, Kamal, and GitHub. From there, every <strong>git push</strong> goes live in under 5 seconds. No build queue, no per-app bills, no pile of YAML.</p>

<p>This README is for working on Single Server itself. For what it does and why, see <a href="https://singleserver.com">singleserver.com</a>. For the full manual, see the <a href="https://singleserver.com/docs/">docs</a>.</p>

## Quickstart

SSH into any fresh Linux box:

```sh
ssh root@<server_ip>
```

Run the interactive installer:

```sh
curl -fsSL https://singleserver.com/install.sh | sh
```

Point it at your repos:

```sh
singleserver add https://github.com/you/your-app
```

Every push to the configured branch deploys from then on. The [docs](https://singleserver.com/docs/) cover everything else.

## Repository layout

- `cmd/singleserverd` — entry point for the one Go binary, which runs as both the deploy daemon and the CLI.
- `internal/singleserver` — all of the product: config, deploys, provider connections, and commands.
- `www` — singleserver.com, including the docs page and `install.sh`. The site deploys as a Single Server app, and its `Dockerfile` builds the hosted binaries it serves at `/bin/`.
- `test/e2e` — the real-dependency end-to-end harness.
- `config/apps.example.yml` — an example app config.

## Build and test

The module targets Go 1.26 and has no service dependencies for development:

```sh
go build ./...
go test ./...
```

The unit tests are plain `go test` with no setup, no network, and no build tags. They also run inside the site's Docker build, so a deploy fails before it ships if a test fails.

`go run ./cmd/singleserverd help` prints the CLI. Most commands expect to run on a configured server, so for real behavior use the E2E harness or a throwaway VPS.

## Releases

To release a new version, push a SemVer tag. That triggers `.github/workflows/release.yml`, which builds the binaries and publishes a GitHub Release with checksums and notes:

```sh
git tag -a v0.2.0 -m "…"
git push origin v0.2.0
```

`install.sh` and `singleserver upgrade` read two channels:

- **stable** (default) — the latest tagged release, checksum-verified.
- **edge** — the latest `main` build at `singleserver.com/bin`.

Both default to stable. To install or upgrade from edge instead, set the channel:

```sh
curl -fsSL https://singleserver.com/install.sh | SINGLESERVER_CHANNEL=edge sh   # fresh box
singleserver upgrade --edge   # existing box
```

## End-to-end tests

The E2E harness spins up disposable Linux hosts in Docker Desktop and runs the real installer against real Tailscale, Cloudflare, and GitHub. It is serial, stateful, and needs Docker plus a local `.env` of test credentials. [test/e2e/README.md](test/e2e/README.md) covers the `.env` and setup, then:

```sh
test/e2e/run.sh
```

---

<p align="center">MIT License · <a href="https://singleserver.com">singleserver.com</a> · <a href="https://singleserver.com/docs/">Docs</a></p>
