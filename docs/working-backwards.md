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
one SSH session and one guided init:

```sh
ssh root@203.0.113.10
curl -fsSL https://singleserver.com/install.sh | sh
singleserver init
singleserver add me/my-app --deploy
```

After that, every push to the configured branch deploys automatically.

The app repository should only need:

- A `Dockerfile`
- A web process listening on one port, default `80`
- A health endpoint, default `/up`

It should not need GitHub Actions workflows, deploy keys, repo-level secrets,
Kamal config, or per-repo runner setup.

## Concepts

Single Server has five moving parts:

- **Server:** one VPS running Docker, Kamal, cloudflared, and the Single Server
  daemon. This is where every `singleserver` command runs.
- **GitHub App:** the event source and deploy credential provider, connected by
  `singleserver init`. Push webhooks trigger deploys; installation tokens fetch
  code and set commit statuses.
- **`apps.yml`:** the allowlist. Only repositories in this file can deploy, even
  if the GitHub App is installed broadly.
- **App containers:** every project runs in its own Docker container behind the
  host proxy.
- **App domains:** domains belong to apps, not to the host. The server is
  managed over SSH; Cloudflare zones and routes are selected when apps or
  app domains are added.

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
- Install cloudflared
- Install `/usr/local/bin/singleserver`
- Install and start `singleserver.service`
- Create `/etc/singleserver`
- Create `/etc/singleserver/apps.yml`
- Create `/etc/singleserver/singleserver.env`
- Run `singleserver doctor`

The command should be safe to rerun.

### 3. Initialize The Host

Ideal command:

```sh
singleserver init
```

This should configure the host environment, Cloudflare Tunnel, and GitHub App
connection. Single Server assumes Cloudflare Tunnel for public traffic and does
not manage direct public TLS.

The host itself should not need a user-facing domain. If the implementation
needs a stable webhook or control URL, `init` should create or hide that detail.
App domains should be selected or inferred when apps are added.

`init` should end by running:

```sh
singleserver doctor
```

### 4. Repair Provider Connections

Provider repair commands should exist, but they should not be part of the normal
first-run path:

```sh
singleserver github connect
singleserver cloudflare connect
```

`singleserver github connect` should open or print a GitHub URL that creates or
installs the Single Server GitHub App with the minimum permissions:

- Contents: read
- Commit statuses: write
- Events: push

The user should install the app on all repositories or selected repositories.
Single Server should still treat `apps.yml` as the deployment allowlist.

After the browser approval, the CLI should write:

```text
/etc/singleserver/github-app.json
/etc/singleserver/github-app.private-key.pem
```

All follow-up commands continue to run on the server over SSH.

## Adding Apps

### Add And Deploy

Ideal command:

```sh
singleserver add me/my-app --deploy
```

This should:

- Verify the GitHub App can access `me/my-app`
- Detect the default branch
- Check that the repo contains a `Dockerfile`
- Add the app to `/etc/singleserver/apps.yml`
- Ask for or infer the app domain, such as `my-app.example.com`
- Configure Cloudflare DNS and tunnel routing for `my-app.example.com`
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
singleserver add me/my-app
```

This should configure the app but wait for the next push or manual deploy.

### Add With Overrides

```sh
singleserver add me/my-app \
  --branch production \
  --app-port 3000 \
  --healthcheck-path /health \
  --healthcheck https://my-app.example.com/health \
  --deploy
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
singleserver add me/my-node-app --app-port 3000 --deploy
```

## Managing Apps

### List Apps

```sh
singleserver list
```

Ideal output:

```text
my-app  me/my-app  branch=main  hosts=my-app.example.com  status=ok
```

### Check The Server

```sh
singleserver doctor
```

This should verify:

- Daemon health
- Config validity
- GitHub App installation access
- App checkouts
- Deploy config source
- Last deploy result
- Public healthchecks
- Disk space
- Docker health
- Proxy/ingress health
- DNS routing

### Deploy Manually

```sh
singleserver deploy me/my-app
singleserver deploy me/my-app abc1234
```

Manual deploys should use the same path as push-triggered deploys.

### View Logs

```sh
singleserver logs
singleserver logs my-app
singleserver logs my-app --follow
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

Changing domains should update central config, DNS when possible, and the proxy.

### Manage Environment Variables

```sh
singleserver env set my-app DATABASE_URL=sqlite:///storage/app.db
singleserver env list my-app
singleserver env unset my-app OLD_KEY
```

Secrets should live on the server, not in GitHub repositories.

### Manage Storage

```sh
singleserver storage enable my-app --mount /storage
singleserver backup my-app
singleserver restore my-app backup-id
```

SQLite apps should have a first-class backup path.

### Remove An App

```sh
singleserver remove my-app
```

This should stop the container, remove proxy routes, optionally remove DNS, and
remove the app from `apps.yml`. It should ask before deleting persistent storage.

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
singleserver init
singleserver add me/homepage --deploy
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
| `singleserver add` | Partial | Adds apps, validates GitHub access, checks `Dockerfile`, supports explicit hosts and optional deploy. Default host inference and DNS automation are not built. |
| `singleserver doctor` | Partial | Checks daemon, config, GitHub App access, checkouts, deploy config, last deploy, and healthchecks. Needs disk, Docker, proxy, and DNS checks. |
| Installer script | Needed | Should install Docker, Kamal, Single Server, systemd service, and base config. |
| `singleserver init` | Needed | Should configure host environment, Cloudflare Tunnel, and GitHub App connection. No user-facing host domain required. |
| `singleserver github connect` | Needed | Repair command. Today the setup page exists; normal `init` should wrap the flow. |
| DNS provider integration | Needed | Cloudflare DNS should be first-class because Cloudflare Tunnel is required. |
| Ingress setup | Needed | Current production uses host-level cloudflared. The installer should make this reproducible. |
| App domain management | Needed | Add/remove hosts after app creation. |
| App environment variables | Needed | Central server-side env/secrets management. |
| Persistent storage | Needed | First-class storage mounts and SQLite backup/restore. |
| Runtime logs | Needed | Deploy logs exist; app container logs need a CLI path. |
| App removal | Needed | Remove config, proxy routes, containers, and optionally DNS/storage. |
| Upgrade command | Needed | Pull releases, restart service, and run `doctor`. |
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
