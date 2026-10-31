# Documentation index

This is the full documentation set for itchgrep, grouped by task. If you know
which page you want, use this table. If you're new to the project, start with
[Getting started](getting-started.md).

For what itchgrep is and a quick start, see the [project README](../README.md).
For maintaining the running site day to day, see [../MAINTAINER.md](../MAINTAINER.md).

## Getting started

| Page | Open this when |
| --- | --- |
| [getting-started.md](getting-started.md) | You're setting up a local copy for the first time, or need the exact prerequisites, build, and run steps. |

## Architecture and code

| Page | Open this when |
| --- | --- |
| [architecture.md](architecture.md) | You need to understand how the two binaries fit together, how a search request or a crawl flows, or what each data file on disk is for. |
| [patterns.md](patterns.md) | You're about to make a common kind of change (add a filter, add a route, change a config value, deploy, roll back) and want the established recipe. |
| [languages/go.md](languages/go.md) | You're new to Go, or want this codebase's specific conventions (error handling, testing style, package layout). |
| [glossary.md](glossary.md) | You've hit a project-specific term (slice, epoch, doc values, self-heal) and want a precise definition. |
| [architecture/decisions/](architecture/decisions/) | You want to know *why* a specific design decision was made, not just what it does — one ADR per decision, numbered 0001 onward. |

## Configuration

| Page | Open this when |
| --- | --- |
| [configuration.md](configuration.md) | You need to look up any environment variable: what it does, its default, and what happens if you change it. |

## Deployment and infrastructure

| Page | Open this when |
| --- | --- |
| [deployment.md](deployment.md) | You're deploying for the first time, running an update, or rolling back a bad deploy. |
| [infrastructure.md](infrastructure.md) | You need to understand the Unraid host, Docker Compose setup, Cloudflare Tunnel, or the GitHub/itch.io dependencies the site relies on. |

## Operations and observability

| Page | Open this when |
| --- | --- |
| [observability.md](observability.md) | You need to read logs, understand the metrics on `/stats`, interpret a health check, or tell normal operation from a real problem. |

## Troubleshooting and recovery

| Page | Open this when |
| --- | --- |
| [troubleshooting.md](troubleshooting.md) | Something is broken right now and you need to diagnose it — `503`s, an unhealthy container, a stuck crawl, heavy bot traffic. |
| [disaster-recovery.md](disaster-recovery.md) | You've lost data, lost a credential, lost the host, or want to take a backup before something goes wrong. |

## Security

| Page | Open this when |
| --- | --- |
| [../SECURITY.md](../SECURITY.md) | You're reporting a vulnerability, or want to understand the site's trust boundaries and what's done to protect visitor privacy. |

## Accessibility

| Page | Open this when |
| --- | --- |
| [../ACCESSIBILITY.md](../ACCESSIBILITY.md) | You want the site's WCAG 2.2 conformance status, how it was evaluated, its known limitations, or how to report an accessibility barrier. |

## Testing and CI

| Page | Open this when |
| --- | --- |
| [testing.md](testing.md) | You want to know what the test suite covers, what it honestly doesn't, or how to run and add tests. |
| [ci-cd.md](ci-cd.md) | You need to understand what GitHub Actions checks (see the `on:` block in `.github/workflows/ci.yml`), or why a CI run failed. |

## Reference

| Page | Open this when |
| --- | --- |
| [dependencies.md](dependencies.md) | You need to upgrade a Go module, a base container image, or understand what external service (itch.io, the ECB, Cloudflare, GitHub) the project relies on and what breaks if it's unavailable. |
| [open-questions.md](open-questions.md) | You want the honest list of facts that couldn't be confirmed from the repository alone and still need a human to verify (account access, registrar details, and similar). |
