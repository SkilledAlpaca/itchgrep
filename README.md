<!-- LTeX: language=en-US -->

# itchgrep

Full-text search over the itch.io game-asset catalogue, running at
[itchgrep.com](https://itchgrep.com/).

itch.io offers no text search scoped to assets — you browse by tag, and any
one view stops at roughly 7,200 results, so whatever ranks below that is
unreachable however you page. itchgrep crawls the catalogue into its own
search index and searches titles, descriptions, tags, and authors at once,
adding tag exclusion, tag combinations beyond itch.io's two, cross-currency
price comparison, and live facet counts on top.

![Searching the itch.io asset catalogue by text, shown in the light theme on
the left of the diagonal and the dark theme on the right](.github/screenshot-search.png)

<sub>One page, split down the diagonal: light theme left, dark theme right.
Cover art and creator names are blurred in this screenshot; they belong to
the asset authors, not to this project.</sub>

## What it does

- Searches four weighted fields at once (tags, title, description, author),
  with exact and fuzzy matching, plus exact phrase search for quoted text.
- Filters by tag (including exclusion), price, author, and currency, with
  live facet counts as you narrow.
- Reaches the whole catalogue by crawling many tag-filtered views and
  deduplicating, rather than the ~7,200-asset ceiling of any single itch.io
  browse view — itch.io itself also caps a browse view at two combined tags,
  offers no tag exclusion, and cannot compare prices across currencies.
- Works with JavaScript disabled — every control is a plain link or GET form,
  so any search or filter combination is a URL you can share or bookmark:

  ```
  /?q=pixel+art                                full-text search
  /?q="pixel art"                              quoted terms match as a phrase
  /?tags=2d,pixel-art&not=3d                   require tags, exclude one
  /?author=ana&price=under-5&sort=title&cur=EUR
  ```

See [docs/architecture.md](docs/architecture.md) for how the search itself is
built.

## Architecture, in brief

Two Go binaries share one data directory and nothing else:

- **`dataservice`** crawls itch.io on a schedule, builds a search index, and
  publishes it.
- **`webserver`** serves the search page from whatever index `dataservice`
  last published. It never contacts itch.io itself.

In production, a Cloudflare Tunnel sits in front of `webserver` so the origin
is never directly exposed to the internet. See
[docs/architecture.md](docs/architecture.md) for the full picture and
[docs/infrastructure.md](docs/infrastructure.md) for how the pieces are
hosted.

## Requirements

- [Docker Engine and the Docker Compose plugin](https://docs.docker.com/get-docker/) —
  the only two things you need to run itchgrep locally.
- To edit the source: Go 1.27 or later, the `templ` CLI pinned to this
  repository's `go.mod` version, and (recommended) the [Task](https://taskfile.dev)
  CLI. [docs/getting-started.md](docs/getting-started.md) covers installing
  each of these and verifying the install.

## Quick start

```
git clone https://github.com/SkilledAlpaca/itchgrep.git
cd itchgrep
docker compose up --build
```

This builds both images, starts `dataservice` and `webserver` together, and
serves the search page at [localhost:62686](http://localhost:62686)
immediately — it answers `503` until the first crawl publishes, which takes
about six hours for the full catalogue. Set `SCRAPE_MAX_PAGES` first (see
[docs/configuration.md](docs/configuration.md#scrape_max_pages)) for a
small, fast crawl instead.

To edit the source and iterate without a container rebuild each time, see
[docs/getting-started.md](docs/getting-started.md) — the full walkthrough,
including generating templates, running the two binaries directly with
`task`, and what a working page looks like the first time.

## Where everything else lives

| Task | Document |
| --- | --- |
| Set up a local dev environment, build, and run | [docs/getting-started.md](docs/getting-started.md) |
| Understand how the system fits together | [docs/architecture.md](docs/architecture.md) |
| Look up any environment variable | [docs/configuration.md](docs/configuration.md) |
| Deploy or update the production host | [docs/deployment.md](docs/deployment.md) |
| Understand the hosting setup (Unraid, Cloudflare, GitHub) | [docs/infrastructure.md](docs/infrastructure.md) |
| Diagnose something that isn't working | [docs/troubleshooting.md](docs/troubleshooting.md) |
| Back up or restore data, or recover from a lost host or credential | [docs/disaster-recovery.md](docs/disaster-recovery.md) |
| Read logs, metrics, and health checks | [docs/observability.md](docs/observability.md) |
| Check what depends on what, and how to upgrade it | [docs/dependencies.md](docs/dependencies.md) |
| Understand the CI pipeline | [docs/ci-cd.md](docs/ci-cd.md) |
| Understand what's tested and how to run it | [docs/testing.md](docs/testing.md) |
| Find the recipe for a common change | [docs/patterns.md](docs/patterns.md) |
| Look up a project-specific term | [docs/glossary.md](docs/glossary.md) |
| See why a specific design decision was made | [docs/architecture/decisions/](docs/architecture/decisions/) |
| Learn the Go conventions this codebase uses | [docs/languages/go.md](docs/languages/go.md) |
| See what's still unverified and needs a human | [docs/open-questions.md](docs/open-questions.md) |

[docs/README.md](docs/README.md) indexes the same pages grouped by task, if
you'd rather browse than search this table.

For maintaining this project day to day, see [MAINTAINER.md](MAINTAINER.md).
For contributing a change, see [CONTRIBUTING.md](CONTRIBUTING.md). To report
a security issue, see [SECURITY.md](SECURITY.md).

## Project status

Live at [itchgrep.com](https://itchgrep.com/), maintained by one person. A
weekly crawl and CI on every push run unattended; see
[MAINTAINER.md](MAINTAINER.md) for what that maintenance actually involves.

## License and credit

itchgrep was created by [winterveil](https://github.com/wintermute-cell); the
original lives at
[wintermute-cell/itchgrep](https://github.com/wintermute-cell/itchgrep). This
repository is a fork, licensed as the original under
[GPL-3.0](LICENSE), and is not operated by the original author. This fork
rebuilt the search index and its scoring, the crawl planner that reaches the
whole catalogue rather than one browse view's page limit, cross-currency
price comparison, and most of the operational tooling (rate limiting,
security headers, structured logging, container hardening, this
documentation set) on top of winterveil's original interface and crawler
design.

This fork was created, updated, and "enhanced" using
[Claude Code](https://claude.com/claude-code), Anthropic's coding agent,
under human direction and review. That is stated plainly rather than buried:
it is running in production and the tests pass, but it is agent-written
code, and you should read it before you trust it with anything of yours.
