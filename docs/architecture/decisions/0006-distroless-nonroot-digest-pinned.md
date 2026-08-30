# 0006: Distroless, nonroot, digest-pinned container images

## Context

Both `Dockerfile.webserver` and `Dockerfile.dataservice` build a static Go binary (`CGO_ENABLED=0`) and copy it into a minimal runtime image. Two independent risks were worth closing off in the runtime image itself, beyond whatever the application code does: what the process can do if compromised, and whether a rebuild of "the same" Dockerfile could silently produce a different image tomorrow.

## Decision

**Distroless, `:nonroot` base image.** Both Dockerfiles' final stage is `gcr.io/distroless/base-debian12:nonroot`. "Distroless" means the runtime image ships no shell, no package manager, and nothing executable besides the application binary itself — there is no `sh`, `curl`, or `bash` for an attacker (or a debugging session) to reach for. The `:nonroot` variant additionally bakes in `USER nonroot:nonroot` (uid/gid `65532:65532`), so the process cannot run as root even by omission — no `USER` line is needed in either Dockerfile, because the base image itself refuses to start as root.

This has a real, spelled-out consequence for the `/data` volume both binaries need to write to: both Dockerfiles ship an empty `/data` directory owned by `65532:65532` (a `COPY --chown` from a placeholder seeded in the builder stage), so Docker seeds a *fresh, named* volume's ownership from that mount point on first use — a named volume created by an older image build, before that seed existed, is still root-owned regardless. A bind-mounted host path is never `chown`'d automatically either way. Both Dockerfiles' comments, and the matching comment on `docker-compose.yml`'s `volumes:` entries, state the one-time fix explicitly: `chown -R 65532:65532 <path>` on a bind-mounted `DATA_PATH`, or every write (and the webserver's `bleve` lock, needed even for search-only use) fails outright.

**Digest-pinned images, not tag-only.** Every `FROM` line in both Dockerfiles pins an exact digest (`golang:1.27@sha256:...`, `gcr.io/distroless/base-debian12:nonroot@sha256:...`) alongside the human-readable tag. A tag like `golang:1.27` is a moving target — it gets repushed on every point release and base-image rebuild — so a tag-only `FROM` is not reproducible: the identical Dockerfile can build a materially different image tomorrow than it did today, with no diff to review. The tag is kept alongside the digest purely so a human reading the line can still see which version it names without resolving the hash by hand.

The `cloudflared` image in `docker-compose.yml` is pinned the same way, and for an additional reason specific to it: it holds the tunnel credential (`TUNNEL_TOKEN`) and dials out to Cloudflare on every start, so an unpinned `:latest` could swap in a new build under the stack with no review and no way to tell which version is actually running.

## Alternatives considered

- **A general-purpose base image (e.g. plain `debian:12-slim`) with an explicit `USER` line.** Works, but leaves a shell and package manager present in the runtime image — a strictly larger attack surface for no functional benefit, since nothing in either binary needs a shell at runtime. (This is also *why* the webserver's Docker healthcheck runs the binary's own `-healthcheck` flag rather than `curl` — see `cmd/webserver/main.go`'s `checkHealthz` and the matching comment in `docker-compose.yml` — there is no `curl` in this image to call.)
- **Tag-only `FROM` lines, refreshed by Dependabot or similar on a schedule.** Simpler to keep current automatically, but reintroduces exactly the non-reproducibility this decision closes: what image actually gets built depends on when, not only on what's committed. Digest pinning trades that automatic freshness for an explicit, reviewable "refresh deliberately, never as a drive-by" step (each Dockerfile's comment gives the exact `docker buildx imagetools inspect` command to run) — see [patterns.md#add-or-upgrade-a-dependency](../../patterns.md#add-or-upgrade-a-dependency).

## Consequences

- A bind-mounted `DATA_PATH` deployment (rather than a Docker-managed named volume) requires that one-time `chown` step, both on first setup and again after upgrading an existing deployment onto this nonroot image — skipping it is not a degraded mode, it is a hard failure for both containers.
- Base images are never bumped by dependency-update tooling automatically; refreshing them is a deliberate, manual step (see [patterns.md](../../patterns.md#add-or-upgrade-a-dependency)) that has to happen periodically or the images will eventually carry known-fixed vulnerabilities from an old base layer, invisible to `govulncheck` (which scans the Go module graph, not the base image).
- Nothing in either runtime container can be debugged by `docker exec`-ing a shell into it — there isn't one. Diagnosis has to go through the application's own logs (see [observability.md](../../observability.md)) or by attaching a separate debugging container to the same volume/network.
