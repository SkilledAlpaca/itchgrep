# Contributing

itchgrep is maintained by one person. This document is written for that
reality: it's the checklist the maintainer follows for their own changes,
and the same checklist an outside contributor should follow too.

## Set up a dev environment

See [docs/getting-started.md](docs/getting-started.md) for the full
walkthrough: prerequisites, cloning, generating templates, building, and
running the project locally.

## Code conventions

This codebase's conventions are documented in full in
[docs/languages/go.md](docs/languages/go.md); a few that matter most for a
change to fit in:

- **Comments explain why, not what.** Read a few existing files before
  writing new code — this codebase is unusually dense with the reasoning
  behind a decision, right above the code that makes it. Match that: a
  comment that just restates the line below it isn't pulling its weight, but
  a non-obvious constraint or trade-off is worth writing down.
- **Long, descriptive test names.** Test functions read as the specification
  they verify — for example `TestTagUniverseIsLoadedFromDiscoveryNotFromThisPublishsAssets`
  — rather than a short, generic name. Assertions go through testify's
  `assert` (report and keep going) and `require` (report and stop
  immediately, for a failure that would panic the rest of the test). Several
  packages also use table-driven cases (a slice of `struct { name string;
  ... }` looped with `t.Run`) where many small variations of the same
  behavior are being checked. See
  [docs/languages/go.md](docs/languages/go.md#testing-convention) for the
  full convention.
- **`gofmt` is enforced, not a suggestion.** CI fails on anything `gofmt -l`
  reports. Run it before you commit, not after CI tells you.

## Branch and commit conventions

Check `git log` for the actual style before writing a commit message — it's
more reliable than this list going stale. As observed in this repository:

- One commit is a single logical change, often several related edits at
  once.
- The subject line is imperative, present tense, and describes *why* the
  change matters, not a mechanical diff summary — for example, "Bound the
  container logs, so the request log cannot grow without end", not "Add
  logging limit."
- A commit message stands alone: someone reading only `git log --oneline`
  should understand what changed and why without opening the diff.
- There's no fixed branch-naming scheme enforced by tooling; a short slug
  describing the change (for example, `fix/crawl-backoff` or
  `docs/deploy-runbook`) works well.

## What must pass before a change lands

Run all of this locally before pushing — CI runs the same checks and will
reject the change otherwise:

1. `templ generate` (or `task templ`), if any `.templ` file changed.
2. `go vet ./...`
3. `go test ./... -count=1`
4. `gofmt -l .` (excluding generated `*_templ.go` files) — output must be
   empty.

See [docs/ci-cd.md](docs/ci-cd.md) for exactly what GitHub Actions runs, in
what order, and what each failure means. [docs/testing.md](docs/testing.md)
covers what the suite does and doesn't cover — read it before assuming an
untested path is safe to change casually.

## For outside contributors

This is a fork maintained by one person, not a project with a review team —
outside pull requests are welcome but may take a while to get to, and a
large, unsolicited change is harder to review than a small one. Before
starting anything nontrivial, open an issue first to ask whether it fits the
project's direction; that avoids spending effort on a change that doesn't
get merged. Keep discussion in GitHub issues rather than other channels.

## Security reporting

See [SECURITY.md](SECURITY.md) for how to report a vulnerability. There is
currently no private disclosure channel — the published contact is the
public issue tracker itself — so SECURITY.md is also where that trade-off is
explained.

## Release process

There are no versioned releases. Production deploys directly from `main`:
merging to `main` triggers CI, and the maintainer runs `scripts/update.sh` on
the production host to pull and deploy that commit — see
[docs/deployment.md](docs/deployment.md). There is no staging environment and
no release branch.
