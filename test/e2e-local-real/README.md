# Local Real-Dependency E2E

This test spins up a disposable Linux host in Docker Desktop and runs the real
Single Server installer against it. It uses real Tailscale, Cloudflare, GitHub,
Docker, and Kamal instead of fake provider services.

The test is intentionally serial and stateful. It creates temporary Tailscale
nodes, Cloudflare tunnels, Cloudflare DNS records, Git commits, and app deploys.

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
  `TAILSCALE_TAG`: OAuth client used to generate a fresh ephemeral Tailscale
  auth key for each run. `TAILSCALE_AUTHKEY` is still accepted as a fallback.
- `GITHUB_APP_ID`, `GITHUB_APP_SLUG`, `GITHUB_WEBHOOK_SECRET`, and
  `GITHUB_APP_PRIVATE_KEY_PATH`: credentials for a GitHub App installed on the
  test repository.
- `GITHUB_TEST_REPO`: repository used for test commits and deploys.

The local `.env` and generated work directory are ignored by git.

## Tailscale OAuth Setup

Create a Tailscale OAuth client with the `auth_keys` scope and the tag used by
`TAILSCALE_TAG`, usually `tag:singleserver-e2e`. The E2E runner uses this OAuth
client to create a fresh one-hour, one-off, ephemeral, pre-authorized auth key
for each test run.

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

The run verifies:

- Local installer downloads the just-built binary.
- Docker, Kamal, Tailscale, cloudflared, registry, deploy user, and daemon boot.
- Tailscale Funnel exposes the GitHub webhook endpoint.
- Cloudflare Tunnel and DNS route a temporary app domain.
- GitHub App webhook URL is updated to the current Funnel URL.
- A test app is deployed from GitHub.
- A pushed GitHub commit triggers a webhook deploy.
- The app is removed and the temporary DNS record is cleaned up.

Set `SINGLESERVER_E2E_KEEP_CONTAINER=1` to keep the disposable host after a
failed run for debugging.
