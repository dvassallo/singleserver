# Working Backwards: The Single Server User Experience

This document describes the user experience we want for Single Server before every
piece of it exists. Treat it as both user documentation and a product roadmap.

The goal is simple: start with a blank VPS from Hetzner, DigitalOcean, AWS,
Azure, or any other provider, then deploy many small GitHub projects to it with
fast, isolated, repeatable deploys.

Single Server is intentionally host-centered: SSH to the server, install it
there, and run every `singleserver` command from that one machine.

## The Ideal Outcome

A user should be able to provision a new server and deploy their first app with
one SSH session:

```sh
ssh root@203.0.113.10
curl -fsSL https://singleserver.com/install.sh | sh
singleserver add https://github.com/you/my-app
```

After that, every push to the configured branch deploys automatically.

The app repository should only need:

- A `Dockerfile`
- A web process listening on one port, default `80`
- A health endpoint, default `/up`

It should not need GitHub Actions workflows, deploy keys, repo-level secrets,
Kamal config, or per-repo runner setup.

## Concepts

Single Server has a few moving parts:

- **Server:** one VPS running Docker, Kamal, Tailscale, cloudflared, and the
  Single Server daemon. This is where every `singleserver` command runs.
- **Tailscale:** the private admin network and webhook exposure. Operators can
  reach the host with Tailscale SSH without managing workstation SSH keys, and
  Tailscale Funnel exposes the local setup/webhook endpoint to GitHub at a
  stable `*.ts.net` URL.
- **GitHub App:** the event source and deploy credential provider. Push
  webhooks trigger deploys; installation tokens fetch code and set commit
  statuses.
- **`apps.yml`:** the allowlist. Only repositories in this file can deploy, even
  if the GitHub App is installed broadly.
- **App containers:** every project runs in its own Docker container behind the
  host proxy.
- **App domains:** domains belong to apps, not to the host. By default,
  Cloudflare creates proxied CNAME records to a Cloudflare Tunnel, cloudflared
  carries requests to the server, and Kamal routes traffic to the right
  container behind that tunnel. The server IP stays hidden, Cloudflare handles
  public TLS and edge protections, and a hostname can belong to only one app at
  a time.
- **App names:** the repo name is the default app name, but app names must be
  unique on a server because they drive generated service names, containers,
  storage paths, and inferred domains.

Single Server should feel like a tiny PaaS you own, not like a pile of bespoke
shell scripts.

## Blank Server Setup

### 1. Create A Server

Create a small Linux server with a public IPv4 address. Ubuntu LTS is the default
target.

Minimum recommended starting point:

```text
2 vCPU
2 GB RAM
40 GB disk
Ubuntu LTS
```

For many small apps, static sites, and SQLite-backed Node/Bun apps, a larger box
can host dozens of projects comfortably.

### 2. Install Single Server

Ideal command:

```sh
curl -fsSL https://singleserver.com/install.sh | sh
```

The installer should:

- Create a `deploy` user
- Install Docker
- Install Kamal
- Install Tailscale
- Install cloudflared
- Install `/usr/local/bin/singleserver`
- Install and start `singleserver.service`
- Create `/etc/singleserver`
- Create `/etc/singleserver/apps.yml`
- Create `/etc/singleserver/singleserver.env`
- Connect or repair Tailscale SSH and Funnel
- Connect Cloudflare Tunnel and DNS for app domains when a token is available
- Print the GitHub App setup URL once a Tailscale Funnel URL exists
- Run `singleserver doctor`

The command should be safe to rerun. If a provider is missing credentials or
needs browser approval, the installer should print the specific repair command
and continue to diagnostics.

The host itself should not need a user-facing domain. Tailscale Funnel supplies
the stable setup and webhook URL for the daemon. App domains should be selected
or inferred when apps are added.

### 3. Repair Provider Connections

Provider repair commands should exist, but they should not be part of the normal
first-run path:

```sh
singleserver github connect
singleserver tailscale connect
singleserver cloudflare connect --zone example.com
```

`singleserver github connect` should open or print a GitHub URL that creates or
installs the Single Server GitHub App with the minimum permissions:

- Contents: read
- Commit statuses: write
- Events: push

The user should install the app on all repositories or selected repositories.
Single Server should still treat `apps.yml` as the deployment allowlist.
The generated app should be public/installable so the same app can be installed
under multiple GitHub owners when needed.

After the browser approval, the CLI should write:

```text
/etc/singleserver/github-app.json
/etc/singleserver/github-app.private-key.pem
```

All follow-up commands continue to run on the server over SSH.

`singleserver tailscale connect` should install or repair the server's
Tailscale connection, enable Tailscale SSH when possible, enable Tailscale
Funnel for the daemon's local port, and store the resulting public `*.ts.net`
URL for GitHub setup and webhooks.

`singleserver cloudflare connect --zone <domain>` should connect app-domain DNS
automation for one Cloudflare zone. It should create or reuse a Cloudflare
Tunnel, configure `cloudflared`, and prepare proxied CNAME records to the tunnel
for future app domains.

## Adding Apps

### Add An App

Ideal command:

```sh
singleserver add https://github.com/you/my-app
```

This should:

- Verify the GitHub App can access `https://github.com/you/my-app`
- Detect the default branch
- Check that the repo contains a `Dockerfile`
- Add the app to `/etc/singleserver/apps.yml`
- Ask for or infer the app domain, such as `my-app.example.com`. The inferred
  domain should use a DNS-safe label derived from the app name.
- Configure a proxied Cloudflare DNS record and tunnel route for
  `my-app.example.com`
- Render and validate the generated Kamal config
- Deploy the current branch tip
- Run the app healthcheck
- Set the GitHub commit status
- Show the live URL

Expected output:

```text
my-app github_installation ok id=123456
my-app default_branch ok main
my-app dockerfile ok Dockerfile on main
my-app dns ok my-app.example.com
my-app host ok my-app.example.com
my-app deploy_config ok generated from conventions
my-app config ok added to /etc/singleserver/apps.yml
my-app deploy ok 4280ms
my-app healthcheck ok https://my-app.example.com/up
```

### Add Without Deploying

```sh
singleserver add https://github.com/you/my-app --no-deploy
```

This should configure the app but wait for the next push or manual deploy.

### Add With Overrides

```sh
singleserver add https://github.com/you/my-app \
  --branch production \
  --app-port 3000 \
  --healthcheck-path /health \
  --healthcheck https://my-app.example.com/health
```

Most apps should not need overrides.

### Add A Custom Domain

```sh
singleserver domains add my-app app.example.com
```

Custom, apex, `www`, and legacy migration domains should use domain-management
commands instead of changing the basic `add` shape.

## App Contract

Single Server should keep the app contract small.

Required:

- The repo has a `Dockerfile`.
- The container starts a web server.
- The container listens on the configured app port, default `80`.
- The app has a healthcheck path, default `/up`.

Optional:

- `config/deploy.yml` for apps that need a custom Kamal config.
- A mounted storage directory for SQLite or uploaded files.
- Environment variables managed centrally by Single Server.

Example static-site `Dockerfile`:

```Dockerfile
FROM nginx:alpine
COPY dist /usr/share/nginx/html
```

Example Node/Bun app shape:

```Dockerfile
FROM oven/bun:1
WORKDIR /app
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile
COPY . .
ENV PORT=3000
EXPOSE 3000
CMD ["bun", "start"]
```

Then add it with:

```sh
singleserver add https://github.com/you/my-node-app --app-port 3000
```

## Managing Apps

### List Apps

```sh
singleserver list
```

Ideal output:

```text
my-app  you/my-app  branch=main  hosts=my-app.example.com  status=ok
```

### Check The Server

```sh
singleserver doctor
```

This should verify:

- Daemon health
- Config validity
- Local deploy user and SSH path
- Local image registry
- GitHub App installation access
- App checkouts
- Deploy config source
- Last deploy result
- Public healthchecks
- Disk space
- Docker health
- Proxy/ingress health
- Resolver DNS
- Cloudflare Tunnel routes and DNS targets
- Tailscale status

### Deploy Manually

```sh
singleserver deploy you/my-app
singleserver deploy you/my-app abc1234
```

Manual deploys should use the same path as push-triggered deploys.

### View Logs

```sh
singleserver logs
singleserver logs my-app
singleserver logs my-app --follow
singleserver logs --daemon
```

The default view should show deploy logs. App runtime logs should be available
with an explicit flag:

```sh
singleserver logs my-app --runtime
```

### Manage Domains

```sh
singleserver domains add my-app my-app.example.com
singleserver domains remove my-app old.example.com
```

Changing domains should update central config, Cloudflare DNS and tunnel routes
when possible, and the proxy.

### Manage Environment Variables

```sh
singleserver env set my-app DATABASE_URL=sqlite:///storage/app.db
singleserver env list my-app
singleserver env unset my-app OLD_KEY
```

Secrets should live on the server, not in GitHub repositories.
Environment changes should be visible to the next deploy. The command should
print the deploy command to run when the operator is ready, rather than forcing a
deploy after every single variable change.

### Manage Storage

```sh
singleserver storage enable my-app --mount /storage
singleserver backup my-app
singleserver restore my-app backup-id --yes
```

Enabling storage should update the app config and redeploy the app so the mount
is live. `--no-deploy` should stage the storage change for a later deploy.
SQLite apps should have a first-class backup path.

### Remove An App

```sh
singleserver remove my-app
singleserver remove my-app --delete-storage --yes
singleserver remove my-app --delete-repo --yes
```

This should stop the container, remove proxy routes, remove Cloudflare DNS and
tunnel routes when managed, and remove the app from `apps.yml`. The repository
checkout and persistent storage should be kept by default and deleted only with
explicit confirmation.

### Upgrade Single Server

```sh
singleserver upgrade
```

This should download the latest release, restart the service, and run
`singleserver doctor`.

## Files On The Server

Ideal layout:

```text
/etc/singleserver/apps.yml
/etc/singleserver/singleserver.env
/etc/singleserver/github-app.json
/etc/singleserver/github-app.private-key.pem
/srv/repos/<app>
/srv/storage/<app>
/var/log/singleserver
```

The user should rarely edit these files by hand, but they should be simple enough
to understand when something goes wrong.

## Desired First-Run Flow

This is the complete happy path we should optimize for:

```sh
ssh root@203.0.113.10
curl -fsSL https://singleserver.com/install.sh | sh
singleserver add https://github.com/you/homepage
```

Then:

```sh
git push
```

The push should deploy the app and set a GitHub commit status.

## Roadmap

Status key:

- **Built:** works in the current codebase.
- **Partial:** exists, but the user experience is not yet the ideal flow.
- **Needed:** not built yet.

| Experience | Status | Notes |
| --- | --- | --- |
| Central `apps.yml` allowlist | Built | Pushes for unlisted repos are ignored. |
| GitHub App push deploys | Built | The GitHub App provides webhooks and installation tokens. |
| Generated Kamal config | Built | Repos do not need `config/deploy.yml` unless they want custom behavior. |
| `singleserver add` | Built | Adds apps, validates GitHub access, checks `Dockerfile`, deploys by default, supports `--no-deploy`, infers default domains from Cloudflare, and configures DNS records and tunnel routes. |
| `singleserver doctor` | Built | Checks daemon, config, Docker, local deploy user/SSH, local registry, disk, Tailscale, Cloudflare Tunnel routes, Cloudflare DNS targets, resolver DNS, GitHub App access, checkouts, deploy config, last deploy, and healthchecks. |
| Installer script | Built | `https://singleserver.com/install.sh` installs Docker, Kamal, Tailscale, cloudflared, the hosted Single Server binary, the systemd service, local registry, base config, first-run provider setup, and `doctor`. |
| `singleserver github connect` | Built | Repair command that prints the GitHub App setup URL for the public/installable Single Server GitHub App. |
| `singleserver tailscale connect` | Built | Repair command that connects Tailscale SSH for private server access and Tailscale Funnel for GitHub setup/webhooks. |
| DNS provider integration | Built | Cloudflare DNS is first-class for app domains. |
| Ingress setup | Built | App domains route through proxied Cloudflare Tunnel records by default; Kamal's proxy handles host routing to containers behind the tunnel. |
| App domain management | Built | Add/remove/list/verify hosts after app creation; add/remove deploy by default and support `--no-deploy`. |
| App environment variables | Built | Central server-side env/secrets management exists through `singleserver env`. |
| Persistent storage | Built | Storage mounts, SQLite-aware backups, explicit restore confirmation, rollback copy retention, and container restart behavior are built. Off-server backup destinations can be added later. |
| Runtime logs | Built | `singleserver logs <app> --runtime` streams app container logs. |
| App removal | Built | Removes config, proxy routes, containers, and optionally storage. |
| Upgrade command | Built | Re-runs the installer, restarts the service, and runs `doctor`. |
| Provider-specific server provisioning | Needed | Optional later step: create Hetzner/DO/AWS/Azure instances directly from Single Server. |

## Product Principles

- GitHub is the event source, not the deploy runner.
- The server is the deploy control plane.
- `apps.yml` is the source of truth for what can deploy.
- App repositories should stay boring: code plus `Dockerfile`.
- Secrets stay on the server.
- Every app gets its own container.
- Defaults should handle most apps.
- Overrides should be explicit and visible.
- The system should be inspectable with normal Linux tools.
