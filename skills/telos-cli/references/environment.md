---
title: The Telos Cloud Environment
description: Everything you need to know about the Telos cloud environment - networking, secrets, resources, developer toolchain, and more
group: Platform
---

The Telos Cloud environment is the place where your goals are translated into running software.

## Overview

Each goal gets its own persistent VM that runs a lightweight Kubernetes cluster.

The Telos agent harness lives inside this VM and has access to a full `git` workspace, developer toolchain, and permissions to launch and manage services (inside the VM). This lets the agent own the full build, test, and deploy lifecycle.

<sketch-of-cell-architecture-in-an-agent-friendly-way>

## Toolchain

Any agent's capability is bounded by the list of tools that are accessible to it. Here is a list of the tools that are baked into the Telos cloud environment. Tools are packaged via [Nix](https://nixos.org) to ensure hermetic and consistent deployments.

A compact overview of the available tools is below:

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

The full list of tools is available in the appendix of this file (TODO(grohan): add this in)

## Networking

Telos follows a strict networking model, with limited ingress and egress by default.

### Ingress

The only entry points into a goal's runtime environment are:

- the product API or UI, accessed through the managed hostname (`spec-name-<sha>.usetelos.ai`)
- the administration dashboard, accessed via the web UI and restricted to authenticated Telos operators. TODO(grohan): read more about dashboard, where?

### Egress

By default, the Telos system has a limited set of egress points out of its runtime environment:

| Ecosystem | Description | Hostnames |
|---|---|---|
| Docker Hub | Download public container images | `auth.docker.io`, `registry-1.docker.io`, `production.cloudfront.docker.com` |
| PyPI | Download Python packages | `pypi.org`, `files.pythonhosted.org` |
| npm | Download `npm` packages | `registry.npmjs.org` |

Access is limited to the read-only HTTPS methods and paths needed for these downloads - it does not allow arbitrary requests to these hosts.

For most real-world use cases, this is insufficient, so Telos also supports configuring additional outbound HTTPS access in your goal's YAML frontmatter, for example:

```yaml
---
name: a-noble-goal
platform: cloud
egress:
  - host: public.example.com
  - host: api.example.com
    credentials: sec-example
    methods: [GET]
    paths: ["/reports/*"]
---
```

Each entry in `egress` specifies a host and optionally restricts HTTP methods and URL paths on that host. A host without `methods` or `paths` allows traffic via all methods and all paths.

For authenticated requests, it is also possible to securely inject credentials. The workflow for this is as follows:

1. Create a credential entry (name and secret) on the web app (TODO(grohan): link to the webpage where you can add in the credential)
2. Add the credential's destination host to `egress` in your Goal's frontmatter.
3. Associate that host with the credential ID that you just created using `credentials`.

A few additional details on this feature:

- Outbound access is only for HTTPS (no raw TCP/UDP)
- Credentialed entries require an exact hostname, and paths must be exact or end in `/*`
- Network-only entries can use wildcard hosts
- One credential can serve multiple declared hosts


TODO(grohan): some notes on credential impl and why its safe and invisible to the agent and shit

TODO(grohan) fit in the quote: "An agent is nothing without its environment"


TODO(grohan): need to add in the tools appendix
