# Open questions

This is the honest list of everything about itchgrep's operation that could not be confirmed from the repository itself, and needs a human — specifically, someone with access to the relevant account (Cloudflare, GitHub, the domain registrar, or the Unraid host itself) — to answer.

## How to work an entry

1. Confirm the fact, using the account or dashboard named in that entry's "How to determine" line.
2. Move the confirmed fact into whichever document it belongs in (each entry says which), replacing any `UNKNOWN — REQUIRES HUMAN CONFIRMATION` placeholder there with the real value.
3. Delete the entry from this file.

If you find one of these questions genuinely can't be answered (an account nobody has access to anymore, a record nobody kept), leave the entry in place but add a line saying so and what you tried — that's still more useful than silence, and the same UNKNOWN placeholder should stay in the document it affects rather than being replaced with a guess.

---

### 1. Exact original fetch URL of the vendored htmx bundle

**What's unknown:** the precise URL `internal/web/static/htmx.min.js` was originally downloaded from. `internal/web/static/VENDOR.md` reconstructs a likely URL (`https://unpkg.com/htmx.org@1.9.10/dist/htmx.min.js`, matching unpkg's standard scheme and the version confirmed from the file itself) but the commit that vendored the file didn't record the exact path fetched.

**Why it matters:** confirms the file wasn't tampered with or fetched from a different, less trustworthy source than intended, and removes the "reconstruction, not a recorded fact" caveat from `VENDOR.md`.

**How to determine:** compare `internal/web/static/htmx.min.js`'s contents byte-for-byte against `https://unpkg.com/htmx.org@1.9.10/dist/htmx.min.js` (or the equivalent file linked from https://htmx.org/docs/#installing for that version). If they match exactly, the reconstructed URL can be treated as confirmed.

**Affects:** `internal/web/static/VENDOR.md` (not owned by this doc set — update it directly once confirmed).

---

### 2. Ownership and monitoring of the security/crawler contact channel

**What's unknown:** who actually monitors `https://github.com/SkilledAlpaca/itchgrep/issues` — the address both `internal/web/robots.go`'s `security.txt` (`Contact:` field) and `internal/fetcher/fetcher.go`'s crawler `User-Agent` header point back to. If a security researcher or an itch.io operator opens an issue there, does anyone see it in a timely way? What's the handover plan if the current maintainer becomes unreachable?

**Why it matters:** a contact channel nobody watches is worse than none at all — it implies responsiveness that doesn't exist, potentially delaying disclosure of a real vulnerability or an itch.io complaint.

**How to determine:** confirm GitHub notification settings for the `SkilledAlpaca/itchgrep` repository (Settings > Notifications, or the watching account's own notification preferences) actually alert someone who checks regularly. Document a fallback contact (email, etc.) if the issue tracker alone isn't reliably watched.

**Affects:** [docs/infrastructure.md](infrastructure.md#github), [docs/dependencies.md](dependencies.md#itchio).

---

### 3. Domain registrar and renewal date for itchgrep.com

**What's unknown:** which registrar `itchgrep.com` is registered through, and when it's next due for renewal.

**Why it matters:** an expired domain registration takes the entire public site down instantly and can be surprisingly hard to reclaim if it lapses long enough for someone else to register it — this is a single point of failure with a hard deadline attached.

**How to determine:** check the registrar account directly if you have access, or run a WHOIS lookup (`whois itchgrep.com`, or any public WHOIS web tool) for the registrar name and expiration date. Confirm auto-renewal is enabled and the payment method on file is current.

**Affects:** [docs/infrastructure.md](infrastructure.md#dns), [docs/dependencies.md](dependencies.md#costs).

---

### 4. Full Cloudflare account inventory

**What's unknown, specifically:**
- Every DNS record configured for `itchgrep.com` (beyond what's inferable from the tunnel setup).
- Every tunnel route configured (this document assumes only the one routing `itchgrep.com` at `webserver:8080` — see [docs/infrastructure.md](infrastructure.md#cloudflare) — but that's inferred from the compose file's comments, not read from the Cloudflare account itself).
- Whether any WAF rule exists beyond the one Managed Challenge rule the operator described (`(http.request.uri.query ne "" and not cf.client.bot)`).
- The TLS/SSL mode set for the zone (Flexible, Full, Full (strict), etc.).
- The account's own email address and 2FA/recovery configuration.

**Why it matters:** any of these could be a silent single point of failure or a security gap that isn't visible from the code or compose file at all — for example, a permissive TLS mode, an unexpected DNS record pointing somewhere stale, or a WAF rule nobody remembers the reason for.

**How to determine:** log into the Cloudflare dashboard for the account that owns `itchgrep.com` and review, in order: **DNS** (the zone's DNS records tab), **Networking > Tunnels** (routes for the itchgrep tunnel), **Security > WAF** (custom rules list), **SSL/TLS > Overview** (the encryption mode), and **My Profile > Authentication** (2FA and recovery methods, account owner only).

**Affects:** [docs/infrastructure.md](infrastructure.md#cloudflare), [docs/disaster-recovery.md](disaster-recovery.md#credential-loss).

---

### 5. Whether any backup of `/data` exists today, anywhere

**What's unknown:** [docs/disaster-recovery.md](disaster-recovery.md) states plainly that no automated backup exists in this repository or its deployment scripts — but that doesn't rule out an ad hoc backup someone took manually and forgot to document (an Unraid "Appdata Backup / Restore" plugin snapshot, a cloud sync, a manual `tar` copied somewhere).

**Why it matters:** if a backup already exists somewhere, the manual procedure in [docs/disaster-recovery.md](disaster-recovery.md#backup) is a second, redundant safety net rather than the only one — and more importantly, if a real disaster happens *before* this question is answered, knowing where to look first could save real time.

**How to determine:** check the Unraid host for the community "Appdata Backup / Restore" plugin (or any other backup plugin) and whether it's configured to include `/mnt/user/appdata/itchgrep/data`. Check any cloud storage, NAS replication, or off-site sync the host might already have configured for other purposes that happens to cover this path too.

**Affects:** [docs/disaster-recovery.md](disaster-recovery.md#backup).

---

### 6. Where Unraid operators conventionally store backup archives on this host

**What's unknown:** [docs/disaster-recovery.md](disaster-recovery.md)'s manual backup procedure writes the archive to `/mnt/user/appdata/itchgrep/` alongside the live data, purely as a placeholder — nothing in this repository specifies (or should specify, without confirmation) a real backup destination, and writing a backup next to the thing it backs up defeats much of the purpose if that whole share is what fails.

**Why it matters:** a backup stored on the same disk/share as the data it protects doesn't protect against a disk failure, only against accidental deletion or corruption of the live files.

**How to determine:** decide on (and document) a destination outside the `appdata` share — a separate array disk, a second Unraid share, or off-host storage — and update the `tar` command's destination path in [docs/disaster-recovery.md](disaster-recovery.md) accordingly.

**Affects:** [docs/disaster-recovery.md](disaster-recovery.md#manual-backup-procedure).

---

### 7. GitHub account recovery and additional admin access

**What's unknown:** who besides the current maintainer has admin access to the `SkilledAlpaca` GitHub account/organization, and what its 2FA recovery setup looks like.

**Why it matters:** losing sole access to this account blocks every deploy (`scripts/update.sh` pulls from it) and orphans the security contact channel — see entry 2 above and [docs/disaster-recovery.md](disaster-recovery.md#credential-loss).

**How to determine:** review the organization's (or account's) member list and their roles in GitHub's own settings, and confirm at least one working 2FA recovery method (recovery codes stored somewhere durable, a backup authenticator).

**Affects:** [docs/disaster-recovery.md](disaster-recovery.md#credential-loss).

---

### 8. Only the webserver's stdout is captured durably — dataservice logs are not

**What's unknown/undecided:** `scripts/export-logs.sh` follows only the `itchgrep-webserver` container's logs (its `CONTAINER_NAME` default). The dataservice's own crawl logs — progress, coverage figures, 429 counts — exist only inside Docker's own 30 MB-capped `json-file` buffer, and are lost whenever that container is recreated (for example, on every `docker compose up -d --build`).

**Why it matters:** crawl-time diagnostics (why a crawl under-collected, how many 429s it hit) can be gone by the time anyone investigates a stale dataset, especially since [docs/observability.md](observability.md) recommends watching for exactly this kind of thing.

**How to determine/decide:** decide whether this gap is worth closing — either by extending `scripts/export-logs.sh` to follow both containers, or by accepting the current buffer as sufficient given how infrequently the dataservice container is actually recreated in practice.

**Affects:** [docs/observability.md](observability.md#log-pipeline-end-to-end).

---

### 9. Cloudflare Tunnel token rotation — exact current dashboard path

**What's partially known:** per Cloudflare's own public documentation as of 2026, tunnel token rotation is done from the main Cloudflare dashboard under **Networking > Tunnels** (tunnel management moved out of the separate Zero Trust dashboard in a February 2026 change) — select the tunnel, choose **Rotate token**. This is documented in [docs/disaster-recovery.md](disaster-recovery.md#rotating-the-cloudflare-tunnel-token) but has not been exercised against this project's actual Cloudflare account, and dashboard layouts do get reorganized again over time.

**Why it matters:** a stale, wrong menu path in an incident runbook costs time exactly when it's least affordable.

**How to determine:** the next time the token is rotated for any reason, follow the documented steps and correct them in [docs/disaster-recovery.md](disaster-recovery.md) if the actual dashboard has moved.

**Affects:** [docs/disaster-recovery.md](disaster-recovery.md#rotating-the-cloudflare-tunnel-token).
