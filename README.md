<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="www/server-single-wordmark-dark.png">
    <img src="www/server-single-wordmark.png" alt="single server" width="440">
  </picture>
</p>

<p><strong>Git push to production in &lt;5 seconds.</strong></p>

<p>One Linux box can run every app you've ever shipped. Single Server wires up Cloudflare, Tailscale, Docker, Kamal, and GitHub. From there, every <strong>git push</strong> goes live in under 5 seconds. No build queue, no per-app bills, no pile of YAML.</p>

## The entire setup

Setup is three steps, all on your server. After that it mostly stays out of your way, and the occasional app change is one command on the same box.

SSH into any fresh Linux box:

```sh
ssh root@<server_ip>
```

Run the interactive installer. It sets up everything you would otherwise configure by hand:

```sh
curl -fsSL https://singleserver.com/install.sh | sh
```

Point it at your repos:

```sh
singleserver add https://github.com/you/your-app-1
singleserver add https://github.com/you/your-app-2
singleserver add https://github.com/you/your-app-3
```

## Then, forever

Push to GitHub like you always do. Your server hears about it, builds the commit, and swaps the new version in with zero downtime. No CI queue, no registry upload, no waiting.

```text
git push  →  GitHub pings your server through Tailscale Funnel
build     →  Your server builds the container image itself, from that exact commit
live      →  Kamal flips traffic only when the new container is ready, so nobody sees a blip
```

## How it works

There's no magic in here. Single Server is boring, proven tools, the same ones you'd probably pick yourself, plus one small Go binary that ties them together.

- **A small daemon runs the deploys.** The daemon is a webhook endpoint exposed to GitHub through Tailscale Funnel. When a push event arrives, it verifies the signature, checks that the repo is in your app list, fetches the pushed commit, builds a container image, runs the deploy through Kamal, and reports the result back to GitHub. All this happens in under 5 seconds.

- **A CLI to manage your apps.** The Go binary that runs the daemon doubles as the CLI you use over SSH. Add, edit, and remove apps. Trigger a deploy, or roll back to an earlier commit. Attach domains, set env vars, enable persistent storage, take and restore backups. Check status, tail logs, and run diagnostics when something looks off.

- **Cloudflare fronts your apps.** You were likely going to put Cloudflare in front of your apps anyway. Single Server creates the proxied DNS records and a Cloudflare Tunnel for you, so visitors hit Cloudflare's edge for TLS, CDN, and DDoS protection, and traffic reaches your apps through the tunnel. Your server's IP stays hidden, and your apps never have to deal with certificates.

- **GitHub triggers deploys.** GitHub tells your server when there's something new to deploy. During setup, Single Server creates a GitHub App for you. That's GitHub's own mechanism for giving Single Server limited, revocable access to your repos: you install it once on your account, and it covers every repo you deploy. The app sends signed push events, hands the daemon short-lived tokens to fetch the pushed commit, and marks each commit as pending, deployed, or failed. No deploy keys, no GitHub Actions secrets, no per-repo webhooks.

- **Tailscale keeps access private.** Tailscale SSH gets you into the box without exposing SSH to the public internet or scattering keys across laptops. Tailscale Funnel gives GitHub a public URL to reach the daemon's webhook endpoint, so the server itself never needs a domain name or a DNS record.

- **Docker keeps apps apart.** Every app runs in its own container with its own process, env vars, and storage, so one project's dependency mess can't leak into another. Bring your own Dockerfile, or let Single Server generate one for static, Node, and Bun apps. Images build on the server and ship through a local registry, with no Docker Hub account and no upload wait.

- **Kamal swaps versions without downtime.** Kamal is the deploy tool 37signals built to run their own apps. It starts a container with the new release and flips traffic over once it's healthy. Single Server generates the deploy config Kamal needs, so you get zero-downtime deploys without ever writing the YAML. If your repo has its own Kamal config, it uses that instead.

- **Your app barely has to do anything.** If your app serves HTTP on a port, Single Server can run it.

## What you get

- **Deploys in under 5 seconds.** No CI queue, no cold start, no registry upload. Your server builds the container image and swaps it in with zero downtime.

- **No lock-in.** If you stop using Single Server tomorrow, your apps keep running on Docker, Kamal, Cloudflare, Tailscale, and GitHub.

- **A thin layer of glue.** Docker, Kamal, Tailscale, and Cloudflare do the heavy lifting. Single Server is the open-source glue that wires them together.

- **Every app in its own box.** Containers isolate your projects, while the host stays a plain Linux machine you can SSH into and actually understand.

- **Domains that just work.** Add a domain with one command, and DNS, TLS, CDN, and routing all happen. Your server's IP stays hidden behind Cloudflare.

- **Boring operations, on purpose.** Deploy, roll back, tail logs, set env vars, take backups, run diagnostics. One CLI, on the same machine where everything runs.

## Still skeptical?

Fair enough. Tell Claude Code, Codex, Cursor, or your favorite AI agent to grab a throwaway VPS and try Single Server for you. And if you'd like it to work differently, it's a tiny open-source codebase, MIT licensed, that you can fork and make your own. Or just run it yourself:

```sh
curl -fsSL https://singleserver.com/install.sh | sh
```

---

<p align="center">MIT License · <a href="https://singleserver.com">singleserver.com</a> · <a href="https://singleserver.com/docs/">Docs</a></p>
