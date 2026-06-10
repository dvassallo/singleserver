# Local Real-Dependency E2E

This test spins up disposable Linux hosts in Docker Desktop and runs the real
Single Server installer against them. It uses real Tailscale, Cloudflare,
GitHub, Docker, and Kamal instead of fake provider services.

The test is intentionally serial and stateful. It creates Cloudflare tunnels,
Cloudflare DNS records, Git commits, and app deploys. Each distro gets one
fresh host, then the selected app cases run sequentially on that host.

Tailscale is the one deliberately reused provider state. The runner keeps a
small Tailscale state directory per distro under `test/e2e-local-real/state/`
so repeated local runs keep the same `.ts.net` name and cached Funnel
certificate state instead of asking Let's Encrypt for a new identity every run.

## Setup

Copy the example env file and fill it with real test credentials:

```sh
cp test/e2e-local-real/.env.example test/e2e-local-real/.env
chmod 600 test/e2e-local-real/.env
```

Required values:

- `CLOUDFLARE_API_TOKEN`: token that can manage DNS and Cloudflare Tunnels.
- `CLOUDFLARE_ACCOUNT_ID`: account used to create the test tunnel.
- `TEST_ZONE`: Cloudflare zone used for temporary app domains.
- `TAILSCALE_OAUTH_CLIENT_ID`, `TAILSCALE_OAUTH_CLIENT_SECRET`, and
  `TAILSCALE_TAG`: OAuth client used to generate pre-authorized Tailscale auth
  keys for E2E hosts. `TAILSCALE_AUTHKEY` is still accepted as a fallback.
- `GITHUB_APP_ID`, `GITHUB_APP_SLUG`, `GITHUB_WEBHOOK_SECRET`, and
  `GITHUB_APP_PRIVATE_KEY_PATH`: credentials for a GitHub App installed on the
  test repository.
- `GITHUB_TEST_REPO`: repository used for test commits and deploys.

The runner expects local `curl`, `docker`, `gh`, `git`, `go`, `dig`, `openssl`,
and `python3`.

The local `.env`, generated work directory, and Tailscale state cache are
ignored by git.

## Tailscale OAuth Setup

Create a Tailscale OAuth client with the `auth_keys` scope and the tag used by
`TAILSCALE_TAG`, usually `tag:singleserver-e2e`. The E2E runner uses this OAuth
client to create a one-hour, one-off, pre-authorized auth key when a distro host
does not already have usable cached state.

The tag must exist in the tailnet policy file, and it must be allowed to use
Funnel:

```json
{
  "tagOwners": {
    "tag:singleserver-e2e": ["autogroup:admin"]
  },
  "nodeAttrs": [
    {
      "target": ["tag:singleserver-e2e"],
      "attr": ["funnel"]
    }
  ]
}
```

The default Tailscale hostnames are `singleserver-e2e-ubuntu`,
`singleserver-e2e-<distro>`, and so on. Set
`SINGLESERVER_E2E_RESET_TAILSCALE_STATE=1` to log out and remove the cached
state for a run. That is useful when debugging auth, but it may force Tailscale
to request a new Funnel certificate.

## GitHub App Bootstrap

If you do not have test GitHub App credentials yet, run:

```sh
test/e2e-local-real/bootstrap-github-app.sh
```

The helper starts a temporary Single Server container, exposes setup through
Tailscale Funnel, and opens the GitHub App manifest flow. GitHub still requires
a browser approval step. Install the app on the test repository, then copy the
printed values into `test/e2e-local-real/.env`.

## Run

```sh
test/e2e-local-real/run.sh
```

By default this runs the Ubuntu, Debian, Amazon Linux 2023, and Rocky Linux 9
host images and all app cases:

```sh
E2E_DISTROS="ubuntu debian amazonlinux rocky"
E2E_CASES="dockerfile static node"
```

`E2E_DISTROS` and `E2E_CASES` also accept comma-separated values. To run one
fast slice:

```sh
E2E_DISTROS=amazonlinux E2E_CASES=node test/e2e-local-real/run.sh
```

The run verifies:

- Local installer downloads the just-built binary.
- Docker, Kamal, Tailscale, cloudflared, registry, deploy user, and daemon boot.
- Tailscale Funnel exposes the GitHub webhook endpoint.
- Cloudflare Tunnel and DNS route a temporary app domain.
- GitHub App webhook URL is updated to the current Funnel URL.
- A Dockerfile app is deployed from GitHub.
- A static app without a Dockerfile is deployed with a generated Dockerfile.
- A Node app without a Dockerfile is deployed with a generated Dockerfile.
- The Dockerfile app covers an external `/up` healthcheck.
- The static app covers a generated Dockerfile with generated `/ready` container readiness.
- The Node app covers generated Node containerization with `/readyz` container readiness and no external healthcheck URL.
- Pushed GitHub commits trigger webhook deploys for every app case.
- The app is removed and the temporary DNS record is cleaned up.

For app marker checks, the runner tries the public Cloudflare edge whenever the
temporary hostname has published DNS. If the Cloudflare API has accepted the
record but the new test zone has not published the hostname yet, the runner
falls back to the same host/path through Kamal on the local test host. `doctor`
still verifies the Cloudflare DNS record and Tunnel route through the real
provider APIs.

Supported distro images live in `test/e2e-local-real/images/`. To add another
distro later, add `test/e2e-local-real/images/<name>.Dockerfile` and include
`<name>` in `E2E_DISTROS`.

Set `SINGLESERVER_E2E_KEEP_CONTAINER=1` to keep the disposable host after a
failed run for debugging.
