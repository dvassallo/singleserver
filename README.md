# Single Server

Single Server is a tiny deploy daemon for running many small apps on one server.

It receives GitHub App `push` webhooks through your server's public setup URL, checks a central allowlist, fetches the exact pushed SHA, and runs Kamal on the host.
Cloudflare Tunnel is the public ingress for webhooks and app domains, with Tailscale used for private server access.
All `singleserver` commands are run on that host over SSH.

For the intended user experience and roadmap, see
[Working Backwards: The Single Server User Experience](docs/working-backwards.md).
For the marketing homepage, see [www/index.html](www/index.html).

## Naming

The product name is **Single Server**. The repository and service slug are `singleserver`, matching `singleserver.com`.

```text
Product:     Single Server
Repo:        dvassallo/singleserver
Binary:      singleserver
Daemon:      singleserver.service
GitHub App:  Single Server
```

## Install

Run this as root on a Linux server:

```sh
curl -fsSL https://singleserver.com/install.sh | sh
```

The installer downloads the hosted Linux binary and installs Docker, Kamal,
Tailscale, and cloudflared. It also runs first-run setup: Tailscale connection,
Cloudflare Tunnel setup when credentials are available, GitHub App setup URL
printing when a public hook URL exists, and `singleserver doctor`.

## Minimal config

```yaml
apps:
  - dvassallo/sillyface-games
  - dvassallo/fullsend
```

The repo name drives the defaults:

```text
app name:  repository name
checkout:  /srv/repos/<app>
deploy:    kamal setup -q on first deploy, kamal redeploy -q after that
branch:    repository default branch from the webhook payload
kamal:     generated from conventions unless config/deploy.yml is tracked
```

App names must be unique on the server because they drive checkout paths, Kamal
service names, containers, storage paths, and inferred domains. If two GitHub
owners have a repo with the same name, add one of them with `--name` or set a
different `name` in `apps.yml`.

Hostnames must also be unique across apps. A domain can route to one app at a
time; use `singleserver domains remove` before assigning it somewhere else.

Use an object only when an app needs overrides:

```yaml
apps:
  - repo: dvassallo/fullsend
    branch: master
    healthcheck: https://fullsend.game/up
    hosts:
      - fullsend.game
      - fullsend.assetstacks.com
```

By convention, a repo needs a `Dockerfile`, but it does not need a Kamal config.
If the repo tracks `config/deploy.yml`, Single Server uses it as-is. Otherwise,
Single Server writes a temporary `config/deploy.yml` for the deploy and removes it
after Kamal exits.

Generated Kamal config defaults:

```text
service/image:      app name
server host:        127.0.0.1
ssh user/key:       deploy, /root/.ssh/id_ed25519
registry:           127.0.0.1:5555
builder:            local Docker builder for the server architecture
proxy app_port:     80
proxy ssl:          false behind Cloudflare Tunnel
proxy healthcheck:  /up
timeouts:           deploy 10s, drain 1s
```

Optional app overrides for the generated config:

```yaml
apps:
  - repo: smallbets/userbase-homepage
    branch: master
    hosts:
      - userbase.com
      - www.userbase.com
      - userbase.dev
      - www.userbase.dev
    app_port: 80
    healthcheck_path: /up
    healthcheck: https://userbase.com/up
```

## Host secrets

Secrets live on the server, not in app repositories.

```text
/etc/singleserver/apps.yml
/etc/singleserver/singleserver.env
/etc/singleserver/github-app.json
/etc/singleserver/github-app.private-key.pem
```

The installer writes the daemon's service environment under
`/etc/singleserver/singleserver.env`. `singleserver cloudflare connect` adds the
public webhook URL after the tunnel is ready:

```sh
SINGLESERVER_CONFIG=/etc/singleserver/apps.yml
SINGLESERVER_STATE_DIR=/etc/singleserver
SINGLESERVER_PORT=8787
SINGLESERVER_PUBLIC_URL=https://hooks.example.com
```

The GitHub App setup stores its webhook secret and private key in `/etc/singleserver`, so app repositories do not need GitHub Actions secrets, deploy keys, or repo-level webhooks.

## GitHub App

The GitHub App needs:

- repository contents: read
- commit statuses: write
- event subscription: push

Install it with access to all repositories, then let `apps.yml` be the deployment allowlist.
The generated GitHub App is public/installable so the same app can be installed
on each owner account or organization that contains deployable repositories.
Single Server still only deploys repositories listed in `apps.yml`.

The daemon includes a one-time setup page:

```text
https://hooks.example.com/setup/github-app?token=<setup-token>
```

That page creates the GitHub App from a manifest, exchanges GitHub's callback code, and stores the app credentials under `/etc/singleserver`.

## Operator Commands

Install the daemon binary as both `/usr/local/bin/singleserverd` and `/usr/local/bin/singleserver`.

```sh
ssh root@203.0.113.10
singleserver tailscale connect
singleserver cloudflare connect --zone example.com
singleserver list
singleserver status
singleserver add https://github.com/owner/repo
singleserver deploy dvassallo/fullsend
singleserver render-deploy smallbets/userbase-homepage
singleserver logs fullsend
singleserver domains add fullsend play.example.com
singleserver storage enable fullsend --mount /storage
singleserver backup fullsend
singleserver restore fullsend 20260608T181500Z --yes
singleserver remove fullsend --delete-repo --delete-storage --yes
```

`singleserver tailscale connect` enables Tailscale SSH when possible. It can
use `TS_AUTHKEY` or `TAILSCALE_AUTHKEY` for unattended server joins, or run
`tailscale up --ssh` manually and then run `singleserver tailscale connect`.

`singleserver cloudflare connect --zone <domain>` connects Cloudflare Tunnel
and DNS for app domains. It stores the zone,
tunnel credentials, a `hooks.<zone>` webhook hostname, and a `cloudflared`
systemd service. Future app domains are created as proxied CNAME records to the
tunnel and routed through `cloudflared`, so the server IP stays hidden and
Cloudflare handles public TLS, proxying, CDN, and DDoS protection.

`singleserver add <github-url>` validates GitHub App access, checks the repo's
default branch and `Dockerfile`, appends the normalized `owner/repo` to
`/etc/singleserver/apps.yml`, validates the generated Kamal config, deploys the
current branch tip, and runs `doctor` afterward. When Cloudflare is connected,
the default app domain is a DNS-safe app label plus the
connected zone, such as `my-app.example.com` or `singleserver-com.example.com`.
Pass `--no-deploy` to configure the app and wait for the next push or manual
deploy.

`singleserver deploy <owner/repo|app> [ref]` runs the same deploy path as a push webhook. If `ref` is omitted, Single Server deploys the configured branch or the repository default branch.

`singleserver render-deploy <owner/repo|app>` prints the generated Kamal `deploy.yml`
for a configured app. It does not inspect or modify the app repository.

`singleserver domains add <app> <domain>` and `singleserver domains remove <app>
<domain>` update `apps.yml`, Cloudflare DNS and tunnel routes when connected,
and then deploy the app so Kamal picks up the changed proxy hosts. Pass
`--no-deploy` to stage the domain change without applying it to the running app
immediately. `singleserver domains verify [app]` checks resolver DNS and
Cloudflare records when credentials are available.

`singleserver env set <app> KEY=value` and `singleserver env unset <app> KEY`
update server-side app secrets. Env changes are injected by Kamal on the next
deploy, so the command prints the deploy command to run when you are ready.

`singleserver storage enable <app>` creates the host storage directory, updates
`apps.yml`, and deploys the app so Kamal mounts it into the running container.
Pass `--no-deploy` to stage the storage config without applying it immediately.

`singleserver backup <app>` archives the app's configured persistent storage
under `/srv/backups/<app>`. SQLite database files are copied with SQLite's backup
API before the archive is written. `singleserver restore <app> <backup-id> --yes`
replaces the storage directory, keeps the previous copy next to it, and restarts
the app containers unless `--no-restart` is passed.

`singleserver remove <app>` removes config, DNS records when managed, and containers. It keeps the
repo checkout and persistent storage by default. Pass `--delete-repo --yes` or
`--delete-storage --yes` to delete those files explicitly.

## Adding An App

1. Install the Single Server GitHub App on the repository owner, if it is not already installed.
2. Make sure the repository contains a `Dockerfile`.
3. Add it from the server:

```sh
singleserver add https://github.com/owner/repo
```

Future pushes to the configured branch deploy automatically.

## Logs

```sh
journalctl -u singleserver.service -f
singleserver logs
singleserver logs app-name
singleserver logs app-name --runtime
singleserver logs app-name --follow
singleserver logs --daemon
```
