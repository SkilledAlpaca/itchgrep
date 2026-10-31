# CI/CD

This document explains itchgrep's continuous integration (CI) pipeline: what it checks, how to read its results, and enough about the underlying GitHub Actions YAML syntax to edit it safely. It also states plainly what is **not** automated.

## What runs, and when

The workflow file is `.github/workflows/ci.yml`. It triggers on:

```yaml
on:
  push:
    branches: [main]
  pull_request:
  workflow_dispatch:
  schedule:
    - cron: "23 6 * * *"
```

In plain terms: every push to the `main` branch, and every pull request against any branch (opening one, or pushing a new commit to one already open). Pushes to any other branch on their own do **not** trigger it — only opening or updating a pull request does. It also runs daily at 06:23 UTC on `main` (see "Vulnerability scan" below for why), and can be started by hand from the Actions tab (`workflow_dispatch`).

There is one job, `test`, running on a GitHub-hosted `ubuntu-latest` runner (a temporary virtual machine GitHub provisions for the run and destroys afterward — nothing about it persists between runs).

## Step by step

The first step uses `actions/checkout` to check the repository's code out onto the runner. Every workflow needs this as its first step, or there's no code to test.

The second step uses `actions/setup-go` to install the Go toolchain. Its `go-version-file: go.mod` input means it reads the exact Go version from `go.mod`'s own `go` line rather than a version hardcoded into the workflow — so bumping the Go version in `go.mod` is the only change needed to also change what CI builds with. Its `cache: true` input caches downloaded modules between runs, so CI doesn't re-download the same dependencies on every single run.

Dependabot bumps the major versions of both actions, so this document deliberately doesn't quote them. Read the current `uses:` lines and inputs in `.github/workflows/ci.yml`.

```yaml
  - name: Install templ (pinned to go.mod's version)
    run: |
      templ_version="$(go list -m -f '{{.Version}}' github.com/a-h/templ)"
      go install "github.com/a-h/templ/cmd/templ@${templ_version}"
```
Installs the `templ` code-generation CLI (see [docs/dependencies.md](dependencies.md#the-templ-cli)), pinned to whatever version of the `github.com/a-h/templ` *library* `go.mod` currently requires — read with `go list`, not hardcoded — so the generator can never silently drift out of sync with the library version the code actually builds against.

```yaml
  - name: Generate templates
    run: templ generate
```
Runs the code generator, turning every `internal/web/templates/*.templ` source file into its corresponding `*_templ.go` file. The generated files are what the build (and this workflow's own formatting check, next) actually operate on.

```yaml
  - name: Check formatting (excludes generated *_templ.go files)
    run: |
      unformatted="$(gofmt -l . | grep -v '_templ\.go$' || true)"
      echo "$unformatted"
      test -z "$unformatted"
```
`gofmt -l .` lists any Go source file that isn't formatted the way `gofmt` would format it, without changing anything. `grep -v '_templ\.go$'` excludes generated files from that list — `templ generate`'s own output formatting is `templ`'s concern, not this project's. `test -z "$unformatted"` fails the step (a non-zero exit code) if the (filtered) list isn't empty. **If this step fails:** run `gofmt -l .` locally to see which file(s), then `gofmt -w <file>` to fix them, or simply `gofmt -w .` to fix everything at once.

```yaml
  - name: Vet
    run: go vet ./...
```
Runs Go's own static analyzer across every package (`./...` means "this module and everything below it"), catching a class of bugs the compiler itself doesn't — for example, a `Printf`-style call whose format string doesn't match its arguments.

```yaml
  - name: Test
    run: go test ./... -count=1
```
Runs every `*_test.go` file's tests across every package. `-count=1` disables Go's test-result cache, which would otherwise skip re-running a test whose inputs (as Go understands them) haven't changed — forcing every test to actually execute on every CI run rather than potentially reporting a stale cached pass.

```yaml
  - name: Vulnerability scan
    run: cd tools && go tool govulncheck -C .. ./...
```
Runs `govulncheck` — a tool that cross-references this module's dependency tree (and, more precisely, which vulnerable *functions* the code actually calls, not just which vulnerable modules are merely present) against a public database of known Go vulnerabilities.

The checker's version is pinned in `tools/go.mod`, a separate module so its own dependencies never join the application's module graph. Pinning the checker does not freeze the database: `govulncheck` downloads the current vulnerability database on every run, whatever its own version. Dependabot (`.github/dependabot.yml`) proposes new checker versions weekly, in the same pull request as the application's own module updates.

Because the database is live, this step's result can change between two runs of the exact same commit when a new vulnerability affecting a function this code calls is published — that's expected, not a flake. To surface those on `main` first rather than on whichever unrelated pull request runs next, the workflow also runs daily on a schedule (`cron: "23 6 * * *"`, 06:23 UTC). GitHub emails a failed scheduled run to the user who last changed the schedule.

## Reading results

Go to the repository's **Actions** tab on GitHub (`https://github.com/SkilledAlpaca/itchgrep/actions`). Each run is listed with a green check (all steps passed), a red X (at least one step failed), or a yellow dot (still running). Click into a run, then click the `test` job, to see every step's own log output — a failed step is expanded automatically and highlighted.

On a pull request, the same pass/fail status appears directly on the PR page, near the bottom, before the merge button.

## What each kind of failure means

| Step that failed | What it means |
|---|---|
| Checkout / setup-go / install templ | Infrastructure-level — rare, usually a transient GitHub Actions or module-proxy issue. Re-run the job before investigating further. |
| Generate templates | A `.templ` file has a syntax error `templ generate` can't parse. |
| Check formatting | Some Go source file isn't `gofmt`-formatted. Not a logic bug — purely cosmetic, fixed with `gofmt -w`. |
| Vet | A real, likely bug — go vet only flags patterns it's confident are wrong. |
| Test | A test failed — either the code regressed, or (less commonly) the test itself needs updating for an intentional behavior change. |
| Vulnerability scan | A dependency has a known, reachable vulnerability. Read the `govulncheck` output for which module and which call path; usually fixed by upgrading that dependency (see [docs/dependencies.md](dependencies.md#upgrading-a-dependency)). |

## Permissions and secrets

The workflow declares no `permissions:` block and references no `secrets:` anywhere in `ci.yml`. Every step either runs a public action (`actions/checkout`, `actions/setup-go`) or a plain `go`/`gofmt` command against the checked-out code — nothing in this pipeline authenticates to anything, uploads anything, or needs a credential. **This is explicitly verified, not assumed:** a full read of `.github/workflows/ci.yml` shows no `secrets.` reference and no `permissions:` override anywhere in the file, so the job runs with GitHub Actions' own default (read-only) token permissions.

## Editing the workflow safely

A few things worth knowing if you've never touched a GitHub Actions YAML file before:

- **Indentation is meaningful**, exactly like Python. A step that isn't indented under `steps:` at the right level simply won't run, usually with a YAML parse error rather than a helpful message.
- **`uses:`** runs someone else's pre-built action (here, always pinned to a major version tag, which Dependabot keeps current) — you're not going to write one of these from scratch. **`run:`** runs a literal shell command (or, with the `|` block-scalar syntax used above, several lines of shell script) on the runner.
- **`with:`** passes named inputs to a `uses:` action — it's specific to whichever action it's attached to; there's no way to know what inputs are valid without checking that action's own documentation.
- To add a new check (for example, a linter), add a new step under `steps:` with a `name:` and either a `uses:` or `run:` — order matters, since steps run sequentially and a later step doesn't run if an earlier one fails.
- To test a workflow change without waiting on a real push, open a draft pull request — the `pull_request:` trigger covers it the same as any other PR.

## What's NOT automated

**There is no continuous deployment.** This workflow only validates code — it never builds a container image, pushes anywhere, or touches the production host in any way. Deploying a change to production is a **manual** step: an operator runs `scripts/update.sh` on the Unraid host itself, which pulls `main`, rebuilds, and restarts the stack. See [docs/deployment.md](deployment.md) for that full procedure. A green CI check on `main` means the code is safe to deploy — it does not mean it has been.
