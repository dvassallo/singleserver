# Single Server

Single Server is a tiny deploy daemon for running many small apps on one server.

It receives GitHub App `push` webhooks through Tailscale Funnel, checks a central allowlist, fetches the exact pushed SHA, and runs Kamal on the host.
Cloudflare Tunnel is the public ingress for app domains, with Tailscale used for private server access and the GitHub setup/webhook endpoint.
All `singleserver` commands are run on that host over SSH.

For the product docs, see [www/docs/index.html](www/docs/index.html).
For the marketing homepage, see [www/index.html](www/index.html).

## Naming

The product name is **Single Server**. The repository and service slug are `singleserver`, matching `singleserver.com`.

```text
Product:     Single Server
Repo:        dvassallo/singleserver
Binary:      singleserver
Daemon:      singleserver.service
GitHub App:  Single Server <hostname>
```

## Install

Run this as root on a Linux server:

```sh
curl -fsSL https://singleserver.com/install.sh | sh
```

The installer downloads the hosted Linux binary and installs Docker, Kamal,
Tailscale, and cloudflared. It also runs first-run setup: Tailscale SSH and
Funnel setup, Cloudflare Tunnel setup for app domains, GitHub App setup URL
printing when a Funnel URL exists, and `singleserver doctor`.

On an interactive SSH session, the installer prompts for Tailscale login and a
Cloudflare API token. For unattended installs, pass `TAILSCALE_AUTHKEY` or
`TS_AUTHKEY`, `CLOUDFLARE_API_TOKEN` or `CF_API_TOKEN`, and optionally
`SINGLESERVER_CLOUDFLARE_ZONE` or `CLOUDFLARE_ZONE`.

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
    hosts:
      - fullsend.game
      - fullsend.assetstacks.com
```

By convention, a repo with a `Dockerfile` uses that Dockerfile as-is. If a repo
does not have one, Single Server can generate a temporary Dockerfile during
deploy from explicit runtime settings such as `runtime`, `install`, `build`,
`start`, `static_dir`, and `app_port`. It does not infer package-manager
commands or ports.

Repos do not need a Kamal config. If the repo tracks `config/deploy.yml`, Single
Server uses it as-is. Otherwise, Single Server writes a temporary
`config/deploy.yml` for the deploy and removes it after Kamal exits.

Generated Kamal config defaults:

```text
service/image:      app name
server host:        127.0.0.1
ssh user/key:       deploy, /root/.ssh/id_ed25519
registry:           127.0.0.1:5555
builder:            local Docker builder for the server architecture
proxy app_port:     80
proxy ssl:          false behind Cloudflare Tunnel
proxy healthcheck:  / for normal apps, /up for generated static output
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

`healthcheck_path` controls Kamal's container readiness path. It defaults to `/`
for normal app containers and `/up` for generated static containers. The
`healthcheck` URL is optional external monitoring for Single Server's
post-deploy/status checks; if it is absent, Single Server treats that external
check as assumed healthy.

## Host secrets

Secrets live on the server, not in app repositories.

```text
/etc/singleserver/apps.yml
/etc/singleserver/singleserver.env
/etc/singleserver/github-app.json
/etc/singleserver/github-app.private-key.pem
```

The installer writes the daemon's service environment under
`/etc/singleserver/singleserver.env`. `singleserver tailscale connect` adds the
public setup and webhook URL after Tailscale Funnel is ready:

```sh
SINGLESERVER_CONFIG=/etc/singleserver/apps.yml
SINGLESERVER_STATE_DIR=/etc/singleserver
SINGLESERVER_PORT=8787
SINGLESERVER_PUBLIC_URL=https://your-server.your-tailnet.ts.net
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

The daemon includes a one-time setup page exposed through Tailscale Funnel:

```text
https://your-server.your-tailnet.ts.net/setup/github-app?token=<setup-token>
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
singleserver edit owner/repo --healthcheck-path /ready
singleserver deploy dvassallo/fullsend
singleserver render-deploy smallbets/userbase-homepage
singleserver logs fullsend
singleserver domains add fullsend play.example.com
singleserver storage enable fullsend --mount /storage
singleserver backup fullsend
singleserver restore fullsend 20260608T181500Z --yes
singleserver remove fullsend --delete-repo --delete-storage --yes
```

`singleserver tailscale connect` enables Tailscale SSH and Tailscale Funnel when
possible. Funnel exposes the local daemon to GitHub at the server's `*.ts.net`
URL, so the GitHub App setup page and signed push webhooks do not need a
custom DNS record. The command can use `TS_AUTHKEY` or `TAILSCALE_AUTHKEY` for
unattended server joins, or run `tailscale up --ssh` manually and then run
`singleserver tailscale connect`.

`singleserver cloudflare connect --zone <domain>` connects Cloudflare Tunnel
and DNS for app domains. It stores the zone, tunnel credentials, and a
`cloudflared` systemd service. Future app domains are created as proxied CNAME
records to the tunnel and routed through `cloudflared`, so the server IP stays
hidden and Cloudflare handles public TLS, proxying, CDN, and DDoS protection.

`singleserver add <github-url>` validates GitHub App access, checks the repo's
default branch and Dockerfile path, appends the normalized `owner/repo` to
`/etc/singleserver/apps.yml`, validates the generated Kamal config, deploys the
current branch tip, and runs `doctor` afterward. In an interactive SSH session,
`add` prompts for the missing pieces: generated runtime settings when a repo has
no Dockerfile, the readiness path, optional external healthcheck URL, and whether
to deploy immediately. It also prints the equivalent non-interactive command. In
scripts or non-interactive shells, pass explicit generated-runtime options, such
as `--runtime static --static-dir dist` for a static site or
`--runtime node --start "npm start" --app-port 3000` for a web process. When Cloudflare is
connected, the default app domain is a DNS-safe app label plus the connected
zone, such as `my-app.example.com` or
`singleserver-com.example.com`. Pass `--no-deploy` to configure the app and wait
for the next push or manual deploy.

`singleserver edit <app|owner/repo|github-url>` changes app config after an app
has been added. With no flags in an interactive SSH session, it prompts with the
current settings and prints the equivalent non-interactive command. In scripts,
pass the setting flags directly: `--dockerfile` to use the repo Dockerfile,
`--runtime static|node|bun` plus generated Dockerfile options, `--app-port`,
`--healthcheck-path`, `--healthcheck`, or `--no-healthcheck`. Config changes
deploy immediately by default; pass `--no-deploy` to stage the change.

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
2. Add it from the server:

```sh
singleserver add https://github.com/owner/repo
```

In an interactive SSH session, Single Server asks for anything it cannot infer
safely and then prints the equivalent command. In non-interactive usage, provide
the build contract explicitly when the repo does not have a Dockerfile:

```sh
singleserver add https://github.com/owner/static-site --runtime static --static-dir dist
singleserver add https://github.com/owner/node-site --runtime node --install "npm ci" --build "npm run build" --static-dir dist
singleserver add https://github.com/owner/node-app --runtime node --install "npm ci" --start "npm start" --app-port 3000
```

Future pushes to the configured branch deploy automatically.

## Editing An App

Run `edit` without flags when you want Single Server to walk through the current
settings:

```sh
singleserver edit my-app
```

For scripted changes, pass the same answers as flags:

```sh
singleserver edit https://github.com/owner/repo --healthcheck-path /ready
singleserver edit my-app --dockerfile --no-healthcheck
singleserver edit my-app --runtime static --static-dir public
singleserver edit my-app --runtime node --install "npm ci" --start "npm start" --app-port 3000
```

`edit` deploys after writing `apps.yml` unless `--no-deploy` is passed.

## Logs

```sh
journalctl -u singleserver.service -f
singleserver logs
singleserver logs app-name
singleserver logs app-name --runtime
singleserver logs app-name --follow
singleserver logs --daemon
```
