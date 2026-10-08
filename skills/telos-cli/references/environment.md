---
title: The Telos Cloud environment
description: Everything you need to know about the Telos Cloud environment — the VM, developer toolchain, networking, and credentials.
group: Platform
---

The Telos Cloud environment is the place where your Goals are translated into running software.

## Overview

Each Goal gets its own persistent VM that runs a lightweight Kubernetes cluster.

The Telos agent harness lives inside this VM and has access to a full `git` workspace, developer toolchain, and permissions to launch and manage services (inside the VM). This lets the agent own the full build, test, and deploy lifecycle.

## Toolchain

Any agent's capability is bounded by the tools it can use. Every Telos Cloud environment includes the tools below, packaged with [Nix](https://nixos.org) for hermetic, consistent deployments.

| Capability | Included tools |
|---|---|
| Languages and builds | Python 3.12, Node.js 22, npm/npx, Clang 21, Bazel 7, Bazelisk |
| Source control | Git |
| Infrastructure | Kubernetes CLI (`kubectl`), Helm, OpenTofu (`tofu`) |
| Browser testing | Playwright, Chromium |
| Databases and services | PostgreSQL (`psql`, `pg_dump`, `pg_restore`), Redis (`redis-cli`), MongoDB (`mongosh`, import/export and backup tools), Kafka command-line tools, ClickHouse (`clickhouse-client`, `clickhouse-local`) |
| Observability | Loki CLI (`logcli`), Prometheus, Alertmanager CLI (`amtool`) |
| Network and process diagnostics | `curl`, `dig`, `nslookup`, `nc`, `openssl`, `ip`, `ss`, `tcpdump`, `ps`, `top`, `strace` |
| Data processing and file inspection | `jq`, `yq`, `grep`, `sed`, `gawk`, `diff`, `find`, `file`, `tree`, `less` |
| Secrets management | OpenBao CLI (`bao`), Vault CLI (`vault`) |

## Networking

Telos follows a strict networking model, with limited ingress and egress by default.

### Ingress

The only entry points into a Goal's runtime environment are:

- the product API or UI, accessed through the managed hostname (`<name>-<session-id>.usetelos.ai`)
- the administration dashboard, accessed via the web UI and restricted to workspace administrators.

### Egress

By default, the Telos system has a limited set of egress points out of its runtime environment:

| Ecosystem | Description | Hostnames |
|---|---|---|
| Docker Hub | Download public container images | `auth.docker.io`, `registry-1.docker.io`, `production.cloudfront.docker.com` |
| PyPI | Download Python packages | `pypi.org`, `files.pythonhosted.org` |
| npm | Download `npm` packages | `registry.npmjs.org` |

Access is limited to the read-only HTTPS methods and paths needed for these downloads — it does not allow arbitrary requests to these hosts.

For most real-world use cases, this is insufficient, so Telos also supports configuring additional outbound HTTPS access in your Goal's YAML frontmatter, for example:

```yaml
---
name: a-noble-goal
version: 0.1.0
network:
  - host: public.example.com
  - host: api.example.com
    credentials: sec-example
    methods: [GET]
    paths: ["/reports/*"]
---
```

Each entry in `network` specifies a host and optionally restricts HTTP methods and URL paths on that host. A host without `methods` or `paths` allows traffic via all methods and all paths.

## Credentials

For authenticated requests, it is also possible to securely inject credentials. The workflow for this is as follows:

1. Create a credential (a name and its secret value) on the [web dashboard](https://usetelos.ai/workspace?tab=credentials). Telos gives it an ID, such as `sec-example`.
2. Add the credential's destination host to `network` in your Goal's frontmatter.
3. Associate that host with the credential's ID using `credentials`.

A few additional details on this feature:

- Outbound access is only for HTTPS (no raw TCP/UDP)
- Credentialed entries require an exact hostname, and paths must be exact or end in `/*`
- Network-only entries can use wildcard hosts
- One credential can serve multiple declared hosts

Credentials are encrypted at rest and never enter your Goal's VM. Instead, a proxy outside the VM adds them to requests that match your `network` rules.

In short, **your Goal can use credentials, but never see them**. It can still make any request your rules allow, so we recommend using a least-privilege key and narrowing access with `methods` and `paths`.
