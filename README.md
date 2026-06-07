# Single Server

Single Server is a tiny deploy daemon for running many small apps on one server.

It receives GitHub App `push` webhooks, checks a central allowlist, fetches the exact pushed SHA, and runs Kamal on the host.

## Naming

The product name is **Single Server**. The repository and service slug are `singleserver`, matching `singleserver.com`.

```text
Product:     Single Server
Repo:        dvassallo/singleserver
Binary:      singleserver
Daemon:      singleserver.service
GitHub App:  single-server
```

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
ssh user/key:       deploy, ~/.ssh/id_ed25519
registry:           127.0.0.1:5555
builder:            local amd64 Docker builder
proxy app_port:     80
proxy ssl:          false
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

Required environment:

```sh
SINGLESERVER_CONFIG=/etc/singleserver/apps.yml
SINGLESERVER_STATE_DIR=/etc/singleserver
SINGLESERVER_PORT=8787
SINGLESERVER_PUBLIC_URL=https://hooks.singleserver.com
```

The GitHub App setup stores its webhook secret and private key in `/etc/singleserver`, so app repositories do not need GitHub Actions secrets, deploy keys, or repo-level webhooks.

## GitHub App

The GitHub App needs:

- repository contents: read
- commit statuses: write
- event subscription: push

Install it with access to all repositories, then let `apps.yml` be the deployment allowlist.

If repositories live under multiple GitHub owners, the app must be public/installable, then installed on each owner account or organization that contains deployable repositories. Single Server still only deploys repositories listed in `apps.yml`.

The daemon includes a one-time setup page:

```text
https://hooks.singleserver.com/setup/github-app?token=<setup-token>
```

That page creates the GitHub App from a manifest, exchanges GitHub's callback code, and stores the app credentials under `/etc/singleserver`.

## Operator Commands

Install the daemon binary as both `/usr/local/bin/singleserverd` and `/usr/local/bin/singleserver`.

```sh
singleserver list
singleserver status
singleserver deploy dvassallo/fullsend
singleserver render-deploy smallbets/userbase-homepage
singleserver logs fullsend
```

`singleserver deploy <owner/repo> [ref]` runs the same deploy path as a push webhook. If `ref` is omitted, Single Server deploys the configured branch or the repository default branch.

`singleserver render-deploy <owner/repo>` prints the generated Kamal `deploy.yml`
for a configured app. It does not inspect or modify the app repository.

## Adding An App

1. Install the Single Server GitHub App on the repository owner, if it is not already installed.
2. Add the repository to `/etc/singleserver/apps.yml`.
3. Make sure the repository contains a `Dockerfile`.
4. Run a manual deploy once:

```sh
singleserver deploy owner/repo
```

Future pushes to the configured branch deploy automatically.

## Logs

```sh
journalctl -u singleserver.service -f
singleserver logs
singleserver logs app-name
```
