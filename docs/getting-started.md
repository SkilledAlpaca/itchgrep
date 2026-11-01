# Getting started

This is the path from a clean machine to a working local copy of itchgrep,
searching a small real slice of the itch.io asset catalog. It assumes
nothing already installed and no prior familiarity with Git, Go, containers,
or the command line beyond typing a command and reading its output.

A few terms used throughout, defined once here:

- **Command line (or terminal, or shell):** a text-based window where you
  type commands and read their output, instead of clicking things. On
  Windows this is PowerShell or Command Prompt; on macOS and Linux, Terminal.
- **Repository (or repo):** the folder of source code and its full edit
  history, tracked by Git.
- **Container:** a lightweight, isolated bundle of a program plus everything
  it needs to run, so it behaves the same regardless of what else is
  installed on the machine running it. Docker is the tool that builds and
  runs containers.
- **Environment variable:** a named value the operating system supplies to a
  running program, kept outside the program's source code. Every
  configuration knob in this project is one — see
  [docs/configuration.md](configuration.md) for the full list.

## What you'll have at the end

A search page at `http://localhost:8081`, running against a small,
deliberately partial copy of the itch.io asset catalog that your own
machine crawled in a couple of minutes — not the full ~108,000-asset index
the production site serves, but the same code, doing the same thing, at a
scale that doesn't take six hours.

## Prerequisites

Install each of these, then run its verify command and compare the output.

| Tool | Why you need it | Verify command | Expected output |
| --- | --- | --- | --- |
| The Go version in `go.mod`'s `go` directive, or later | Compiles and runs both binaries | `go version` | `go version go1.X.Y`, where `X.Y` is at least what the `go` line in `go.mod` says |
| The `templ` CLI, pinned to the exact version this repo uses | Generates Go code from the `.templ` template files — the build fails without this step, see below | `templ version` | Matches the version in `go.mod`'s `github.com/a-h/templ` line |
| Docker Engine and the Docker Compose plugin | Runs the project the same way production does, in containers | `docker --version` and `docker compose version` | A version number for each, no error |
| [Task](https://taskfile.dev) (the `go-task` CLI) | Optional, but the fastest way to run the two binaries locally without containers — this guide uses it | `task --version` | A version number, no error |

Installing the pinned `templ` CLI needs `go.mod` to read the version from,
so that step is folded into "2. Generate the templates" below, right after
the clone — see there for the exact command.

Task installs the same way, pinned to whatever the latest release is (it has
no version this project depends on):

COMMAND: `go install github.com/go-task/task/v3/cmd/task@latest`
Purpose: Installs the Task CLI, the convenience wrapper this guide uses to
run the two binaries locally.
When to use: Once, during initial setup.
Prerequisites: Go installed and `go` on your `PATH`.
Expected result: No output on success; `task --version` afterward prints a
version number.
Common failures: Same `PATH` issue as `templ`'s install step in "2. Generate
the templates" below.
How to undo or recover: Re-run the same command.

## 1. Clone the repository

COMMAND: `git clone https://github.com/SkilledAlpaca/itchgrep.git`
Purpose: Downloads the full source and history to a new `itchgrep/` folder
in your current directory.
When to use: Once, the first time you set this up on a machine.
Prerequisites: Git installed (`git --version` prints a version with no
error).
Expected result: A progress message ending in something like
`Resolving deltas: 100% (…/…), done.`, and a new `itchgrep/` folder.
Common failures: `command not found: git` means Git isn't installed.
`Could not resolve host` means no internet access.
How to undo or recover: Delete the `itchgrep/` folder and clone again.

Every command from here on assumes your terminal's current directory is
that `itchgrep/` folder (`cd itchgrep`).

## 2. Generate the templates

The user interface is built from `.templ` files (in
`internal/web/templates/`), which are not valid Go on their own — a
generator turns each one into a matching `_templ.go` file the rest of the
program actually compiles against. Those generated files are deliberately
excluded from Git (see `.gitignore`), so a fresh clone doesn't have them yet.

First, install the pinned `templ` CLI (the version must match `go.mod`, or
generated code can silently disagree with what the rest of the build
expects), from inside the cloned repository so `go list` can read `go.mod`:

COMMAND:
```sh
templ_version="$(go list -m -f '{{.Version}}' github.com/a-h/templ)"
go install "github.com/a-h/templ/cmd/templ@${templ_version}"
```
Purpose: Installs the `templ` code generator at exactly the version this
repository's `go.mod` requires — the same derivation `Dockerfile.webserver`
and CI use, so it can never drift out of sync with what the rest of the
build expects (see [docs/dependencies.md](dependencies.md#the-templ-cli)).
When to use: Once, right after cloning, or again any time `templ version`
reports a version different from `go.mod`'s.
Prerequisites: Go installed and `go` on your `PATH`; run from inside the
cloned repository (step 1).
Expected result: No output on success; `templ version` afterward matches
`go list -m -f '{{.Version}}' github.com/a-h/templ`. The binary lands in
`$(go env GOPATH)/bin` — make sure that directory is on your `PATH`, or
`templ` won't be found as a command.
Common failures: `command not found: templ` after install means
`$(go env GOPATH)/bin` isn't on your `PATH` — add it and open a new
terminal. A network error means no internet access to `proxy.golang.org`.
How to undo or recover: Re-run the same command; it's safe to repeat.

Then generate the templates themselves:

COMMAND: `task templ`
Purpose: Runs `templ generate`, producing the `_templ.go` files the build
needs.
When to use: Immediately after cloning, and again any time you edit a
`.templ` file.
Prerequisites: The `templ` CLI installed (previous step).
Expected result:
```
task: [templ] templ generate
(✓) Post-generation event received, processing... [ updates=N needsRestart=true needsBrowserReload=true ]
(✓) Complete [ updates=N duration=…ms ]
```
Common failures: `command not found: templ` — see the `templ` install step
above. A version mismatch between the installed `templ` and the one pinned
in `go.mod` can produce generated code that doesn't compile — reinstall the
pinned version if `go build` fails with strange template-related errors.
How to undo or recover: Safe to re-run any time; it only overwrites the
generated files.

If you don't have Task installed, run `templ generate` directly instead —
it's the same command Task runs for you.

**What it looks like if you skip this step.** `go build ./...` (or `go
test ./...`) fails with errors like:

```
internal/web/templates/feed.go:150:25: undefined: priceFilterLabel
internal/web/templates/feed.go:166:11: undefined: priceLabel
```

Those two names are defined in a generated `_templ.go` file that doesn't
exist yet. The fix is always the same: run `task templ` (or `templ
generate`), then build again.

## 3. Build and test

COMMAND: `go build ./...`
Purpose: Compiles every package in the repository, without running
anything — the fastest way to confirm the source is valid.
When to use: After generating templates, and any time you want to confirm
your changes compile.
Prerequisites: Step 2 completed.
Expected result: No output, and the command exits successfully.
Common failures: `undefined: …` errors in `internal/web/templates/` — see
step 2. Any other compile error is a real problem in the code you're
looking at.
How to undo or recover: Nothing to undo — this step has no side effects.

COMMAND: `task test` (or, without Task, `go test ./...`)
Purpose: Runs the full automated test suite. No external services, network
access, or running containers are needed — every test is self-contained.
When to use: After building, and before trusting any change you make.
Prerequisites: Step 2 completed.
Expected result: A line per package, each starting with `ok`, for example:
```
ok  	itchgrep/internal/cache	0.157s
ok  	itchgrep/internal/config	0.010s
...
ok  	itchgrep/pkg/money	0.010s
```
Common failures: The same `undefined: …` compile errors as step 2 if
templates weren't generated. A genuine test failure prints `FAIL` and the
specific assertion that didn't hold — read the test name and the file it's
in.
How to undo or recover: Nothing to undo — tests don't modify the
repository.

## 4. Run it locally

This project has two binaries that work together: `dataservice` crawls
itch.io and builds the search index, and `webserver` serves the search page
from whatever index `dataservice` has published. Run each in its own
terminal window — you'll want to watch both.

**Terminal 1 — start the crawler with a small page limit,** so it finishes
in a couple of minutes instead of the roughly six hours a full crawl of the
whole catalog takes:

COMMAND: `SCRAPE_MAX_PAGES=40 task dataservice`
Purpose: Starts the dataservice binary. Because no data has ever been
published in this checkout yet, it begins crawling immediately, capped at
40 pages total by `SCRAPE_MAX_PAGES` — see
[docs/configuration.md](configuration.md#scrape_max_pages) for exactly what
that variable does.
When to use: The first time you run this project locally, or any time you
want fresh local data without waiting for a full crawl.
Prerequisites: Steps 2–3 completed.
Expected result: Log lines ending, after roughly a minute or two, with
`Successfully stored assets`. Along the way you'll see a warning
confirming the page cap is active:
```
WARN: ... SCRAPE_MAX_PAGES=40 set: fetching at most 40 pages in total. This produces a PARTIAL index and is intended for testing only.
...
INFO: ... Successfully stored assets
```
Common failures: See "Errors you might hit" below.
How to undo or recover: Press Ctrl-C to stop it at any point; nothing it
already wrote is lost, but a stopped crawl is incomplete until you run it
again.

**Terminal 2 — start the webserver,** any time before or after Terminal 1
finishes — it tolerates having no data yet (see the next section):

COMMAND: `task webserver`
Purpose: Starts the webserver binary and serves the search UI at
`http://localhost:8081`.
When to use: Alongside Terminal 1, to browse whatever data has been
crawled so far.
Prerequisites: Steps 2–3 completed. Not strictly dependent on Terminal 1
having finished.
Expected result: `Server started at port :8081`, and the page loads in a
browser at `http://localhost:8081`.
Common failures: See "Errors you might hit" below.
How to undo or recover: Press Ctrl-C to stop it. Restarting is always
safe — it re-reads whatever data is currently published.

Both tasks share the same data directory (`./data`, by default — see
`Taskfile.dist.yml` and [docs/configuration.md](configuration.md#data_dir)),
which is why running them in the order above, against the same checkout,
lets the webserver pick up what the dataservice just crawled.

### The alternative: Docker Compose

If you'd rather run the containerized version — closer to how production
actually runs, but slower to iterate with while editing code — a single
command replaces steps 2 through 4 entirely:

COMMAND: `docker compose up --build`
Purpose: Builds both container images and starts `dataservice` and
`webserver` together, sharing a Docker-managed data volume.
When to use: To see the project run the way production does, or if you'd
rather not install Go/templ/Task locally at all.
Prerequisites: Docker and Docker Compose installed (see Prerequisites
above). `templ generate` is **not** a separate step here — the container
build runs it internally (see `Dockerfile.webserver`).
Expected result: Interleaved logs from both services; the webserver
answers at `http://localhost:62686` immediately (serving `503` until data
exists — see below), and the dataservice starts a **full, unbounded crawl**
by default — expect roughly six hours before real results appear, unless
you set `SCRAPE_MAX_PAGES` first (see
[docs/configuration.md](configuration.md#scrape_max_pages) — for this path,
put it in a `.env` file next to `docker-compose.yml`, copied from
`.env.example`).
Common failures: `Bind for 127.0.0.1:62686 failed: port is already allocated`
means something else (maybe a previous `docker compose up`) already holds
that port — stop it first with `docker compose down`.
How to undo or recover: `docker compose down` stops the containers, keeping
data; `docker compose down -v` also deletes the crawled data volume,
returning to a clean slate.

**Always rebuild after a source change.** Plain `docker compose up` (without
`--build`) reuses the previously built image and silently runs stale code.

Full production usage — the `--profile public` flag, environment setup, and
so on — is covered in [docs/deployment.md](deployment.md); this section is
only about running it on your own machine.

## What you see when it works

Right after starting, before any crawl has published data, the webserver
answers the search pages (`/`, `/results`, `/tag/{slug}`, `/new`, `/feed`)
with HTTP status `503 Service Unavailable` — `/about`, `/saved`, `/stats`,
and `/healthz` still answer `200`, and `/readyz` answers `503 no data` — and
logs:

```
ERRO: ... Initial cache load failed, will retry in background every 15s: storage.GetAssetsUpdateTime: os.Stat: stat data/assets.json: no such file or directory
```

**This is designed behavior, not a bug.** The webserver starts immediately
rather than waiting on the crawl (which can take hours in production), and
retries loading data every 15 seconds in the background. The moment
`dataservice` publishes anything — even a small, partial crawl — the
webserver notices within about 5 seconds and starts serving it, with no
restart needed. [docs/troubleshooting.md](troubleshooting.md) covers how to
tell this apart from an actually broken index if it doesn't self-resolve.

Once data lands, reloading `http://localhost:8081` shows a real, searchable
page of itch.io game assets, a masthead stating how much of the catalog is
indexed and how old it is, and working search, tags, and filters — all
running against the roughly 40-page slice from step 4, not the full
catalog.

## Errors you might hit

**Build fails with `undefined: priceFilterLabel` (or similar names in
`internal/web/templates/`).** You skipped (or need to re-run) `task templ` —
see step 2.

**`bind: address already in use` when starting a service.** Two processes
are trying to listen on the same port. This is the exact failure mode in an
older version of this project's `README.md`, which showed running both
binaries with only `DATA_DIR` set and no `PORT` for the webserver — since
both binaries default to port `8080` when `PORT` is unset, that combination
makes the second one fail to start. `Taskfile.dist.yml` avoids this by
setting `PORT=8080` for `dataservice` and `PORT=8081` for `webserver`
explicitly — use `task dataservice` / `task webserver` as shown above rather
than bare `go run` commands, or set `PORT` yourself if you run them
directly.

**The page never leaves `503`, more than a few minutes after Terminal 1
logged `Successfully stored assets`.** See
[docs/troubleshooting.md](troubleshooting.md) — this is the "503 that
doesn't self-heal" case, not the designed startup state above.

**A crawl in Terminal 1 seems to hang with no new log lines for a long
time.** With `SCRAPE_MAX_PAGES=40` it should finish in a couple of minutes.
If it doesn't, see [docs/troubleshooting.md](troubleshooting.md)'s "crawl
seems stuck" section before assuming something is broken — itch.io
occasionally responds slowly, and the crawler's own rate limiting
(`SCRAPE_RPS`, see [docs/configuration.md](configuration.md#scrape_rps))
makes progress look slower than it is.

## Resetting and cleaning up

To throw away local data and start over:

COMMAND: `rm -rf ./data`
Purpose: Deletes the local crawl database (`assets.json`, the search
index, and everything else `task dataservice` / `task webserver` share),
so the next run starts from nothing, exactly like a fresh clone.
When to use: To discard a partial or corrupted local crawl, or to re-run
step 4 from a clean state.
Prerequisites: Both `task dataservice` and `task webserver` stopped
(Ctrl-C in both terminals) — deleting the directory while a service holds
it open can leave a half-deleted state on some filesystems.
Expected result: The `./data` folder is gone; nothing else in the
repository is affected.
Common failures: `Permission denied` — a stopped process may not have
fully released the directory yet; wait a moment and retry.
How to undo or recover: Not reversible — re-run step 4 to regenerate local
data. Nothing here is production data; the real site is unaffected.

If you used `docker compose up` instead, `docker compose down -v` is the
equivalent — it removes the containers and the data volume together.

## Next steps

- Every environment variable, in one place: [docs/configuration.md](configuration.md)
- Diagnosing something that isn't working: [docs/troubleshooting.md](troubleshooting.md)
- How this deploys to production: [docs/deployment.md](deployment.md)
- How the system fits together: [docs/architecture.md](architecture.md)
