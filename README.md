<!-- LTeX: language=en-US -->

<div align="center">

# itchgrep.com

### _Discover the Perfect Assets for Your Games_

🔍 search through itch.io assets using text queries; find what you need without relying solely on tags.

🌐 Visit [itchgrep.com](https://itchgrep.com/) to start exploring.

</div>

![Searching the itch.io asset catalogue by text](.github/screenshot-search.png)

<sub>Cover art and creator names are blurred in these screenshots; they belong to
the asset authors, not to this project. Everything else is the real UI against a
real index.</sub>

<div align="left">

### Credit

itchgrep was created by [winterveil](https://github.com/wintermute-cell); the
original lives at
[wintermute-cell/itchgrep](https://github.com/wintermute-cell/itchgrep). This
repository is a fork, licensed as the original under GPL-3.0, and is not
operated by the original author.

</div>

## 🛠 Techstack

- **[Go](https://go.dev/)**
- **[Templ](https://github.com/a-h/templ)**
- **[Bleve](https://github.com/blevesearch/bleve)**
- **[HTMX](https://htmx.org/)**
- **[Docker](https://www.docker.com/)**

These tools and technologies were chosen with care to provide a seamless and efficient experience for both developers and users of itchgrep.

## 🗼 Architecture
![An architectural diagram of itchgrep.com](.github/itchgrep-architecture-diagram.png)

## What you can search, and why it differs from itch.io

itch.io has no text search scoped to the asset catalogue. `/game-assets` is
*navigation*: you pick tags and page through what comes back. The site-wide
search box is a different thing — it spans games, devlogs, collections and
users, it is not restricted to assets, and it is `Disallowed` in itch.io's
`robots.txt`, so this project does not touch it.

So the comparison is not "our search versus their search". It is a search
against a browse tree.

### What the index holds per asset

Four fields carry free text, and a query hits all four at once, weighted by how
much a match in each is worth:

| Field | Weight | Why |
| --- | --- | --- |
| `Tags` | ×4 | Curated classification — a hit here is a deliberate label, not a coincidence of prose |
| `Title` | ×3 | What the author chose to call it |
| `Description` | ×2 | itch.io's one-line blurb; **not searchable on itch.io at all** |
| `Author` | ×1 | So a remembered creator name finds their work |

Every query runs three passes over those fields and unions the results: exact
(×6), fuzzy with a 4-character prefix (×4), and fuzzy with a 2-character prefix
(×2). That is what makes `tilset` find tilesets and `paralax` find parallax
backgrounds, while still ranking the correct spelling first. Quoted text is
pulled out and matched as an adjacent phrase instead, because someone who typed
quotation marks has said the word order matters.

Seven more fields exist to filter, sort and count rather than to be searched —
tag slugs and author keys as indivisible keywords (so `pixel-art` is one term,
not `pixel` + `art`), a free/paid flag, a dollar-normalised price, itch.io's
popularity rank, and its newest-view rank.

### What that buys you over browsing itch.io

- **No 200-page wall.** itch.io serves at most 200 pages per browse view — about
  7,200 assets — so a popular tag has a hard floor you simply cannot page below.
  Every indexed asset here is reachable by query.
- **Unlimited tags, combined with AND.** itch.io accepts two; a third is a
  `403`.
- **Tag exclusion.** `?not=3d` has no equivalent on itch.io — there is no "NOT
  tag" facet in its URL grammar at all.
- **Filters compose.** On itch.io a sort plus two tags is a `403`, and a sort
  plus a price filter is a `403`. Here sort, tags, exclusions, price, author and
  currency are independent and stack freely.
- **Prices compare across currencies.** Sellers price in whatever currency they
  chose, so itch.io cannot answer "under $5" across them. Each asset carries a
  dollar value computed at index time, which is what makes `under-5` and
  cheapest-first mean anything.
- **Live facet counts.** The sidebar counts tags *within your current results*,
  so it tells you where the remaining matches are before you click. itch.io
  shows no counts.
- **Description text is searchable.** itch.io's browse pages display the blurb
  but never match against it.

What itch.io does better, and this cannot: it is authoritative and current to
the second, it knows about assets published since the last crawl, and it can
show anything the crawl did not reach.

![Tag filters, exclusions and live facet counts](.github/screenshot-filters.png)

## Searching and filtering

Everything the UI can do is expressed in the URL, so any view is a link you can
share, bookmark or reload:

```
/                                   the catalogue, most popular first
/?q=pixel+art                       full-text search
/?q="pixel art"                     quoted terms match as a phrase
/?tags=2d,pixel-art                 require tags  (AND)
/?not=3d                            exclude tags
/?author=ana                        one creator
/?price=free                        free  (or paid, under-5, under-20)
/?sort=title                        A-Z  (or popular, relevance, price, recent)
/?cur=EUR                           show prices converted to euros
/?q=tileset&tags=2d&not=3d&price=under-5    all of it at once
```

**Tags combine with AND.** Adding a second tag narrows the results rather than
widening them, which is why you would add one. The sidebar shows the tags
carried by the *current* results with their counts, so it is a map of where the
rest of the matches are rather than a description of the 36 on screen. Each row
also offers a `−` that excludes the tag instead — "2d but not pixel-art" has no
positive phrasing.

**Free versus paid is read from the listing, not inferred.** Every browse page
carries the price in its markup, so the crawl records it directly. The rule is
the presence of a price element: a pay-what-you-want asset with a "-35%" badge
and a minimum of $0 carries a price *tag* but no price *value*, and itch.io
counts it as free — which matches `/game-assets/free` exactly. Pay-what-you-want
is recorded separately, from the price tag's `title`, and shown as "Name your
price" rather than folded into "Free": an author asking for a voluntary payment
is doing something different from giving the asset away.

**Prices compare across currencies.** Sellers price in whichever currency they
chose, so `under-5` and `sort=price` work on a dollar value converted at index
time rather than on the raw number. `?cur=XXX` restates the displayed prices in
one currency, marked `≈` with the source and date on hover — see *Exchange
rates* below.

![An applied tag, an excluded tag, and prices converted to euros](.github/screenshot-currency.png)

**"Recently added" is not a sort by date.** itch.io's listing markup carries no
publication date. What it does have is a newest-first browse view, and the crawl
already fetches it as one of four orderings — so an asset's position in that
view is recorded as a recency rank, at no extra request cost. It only reaches
the ~7,200 assets that view exposes; everything else sorts after them, and the
control is hidden entirely when the loaded index carries no ranks at all.

Every control on the page is a plain link with the htmx attributes layered on
top, so the whole thing — search, filters, sorting, paging — works with
JavaScript disabled. The currency picker is a plain GET form for the same
reason.

### Exchange rates

At the end of every crawl the dataservice fetches the [ECB daily euro reference
rates](https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml) and stores
them as `rates.json` beside the index. A central bank publishing its own numbers
beats a free FX API that may disappear or start charging, and the file needs no
key.

The snapshot is fetched *with* the index, not on its own schedule, because the
dollar value used for `under-5` and `sort=price` is baked into each document —
so the rates the index was built with have to be the rates the site explains
itself with. If the fetch fails the last stored snapshot is reused, and failing
that a table baked into the binary, which is stale but dated and says so.

Converted figures are approximate by construction and presented that way: a `≈`
on the badge, the original price and the rate date on hover, and a link to the
source under the picker. Nothing else in the site uses these numbers.

## Running Locally

If you want to [contribute](#contributing), or just run the project locally for your own use,
follow the instructions below.

> This project is built and maintained on Linux. While I don't think it's
> generally impossible to run on Windows, but the
> [Taskfile](https://taskfile.dev/) is written using Linux commands.

### Quick Start (Docker Compose)
If you just want the whole thing running locally, `docker` is the only
dependency:

```bash
docker compose up --build
```

> Use `--build` after any change to the Go source. Plain `docker compose up`
> reuses the previously built image and will silently run stale code.

This builds and runs the `dataservice`, triggers a full scrape + index of
itch.io, and brings up the `webserver` on
[localhost:8080](http://localhost:8080) straight away — it does not wait for the
scrape.

> The first run crawls the whole asset catalogue (~108,000 assets) at a
> deliberately polite 2 requests/second, so expect around six hours. Progress is
> visible in the `dataservice` logs. The result is kept in the `itchgrep-data`
> docker volume, so subsequent `docker compose up` runs skip the crawl and come
> up immediately.

#### Storage is a directory

Both services share one volume, mounted at `/data` (`DATA_DIR`). That directory
*is* the storage layer: `assets.json`, `tags.json`, `checkpoint.json` and
`index.bleve/`. There is no object store and no emulator.

This used to be Google Cloud Storage, with `fake-gcs-server` standing in
locally. The workload never needed it — one writer, no ACLs, no signed URLs, no
listing, no lifecycle rules — and it cost a container, a network hop, and a
tar+gzip round-trip on every publish to move a 598 MB index across a boundary
that does not exist when both processes share a volume. Opening the index at
webserver startup went from ~3.5s to ~30ms when that went away.

The one property GCS did provide is that a reader never sees a half-written
object. Writes preserve it by landing in a temporary file in the same directory
and being renamed into place; the index is published the same way, by renaming a
staged directory over the live one. A webserver holding the old index open is
unaffected — its file handles follow the inode, not the path.

#### Why the crawl is not just "fetch every page"

itch.io serves **at most 200 pages per browse view** — about 7,200 assets —
while the catalogue holds ~108,000. Paging `/game-assets` alone can therefore
never reach more than 7% of it, and requesting the pages the total count
implies just produces thousands of 404s.

The crawl gets past that by fetching many *filtered* views and deduplicating by
`GameId`. The URL grammar itch.io accepts is narrow, and violating it fails
loudly rather than degrading:

```
/game-assets                    root
/game-assets/<sort>             newest | new-and-popular | top-rated
/game-assets/tag-A              any single tag
/game-assets/<sort>/tag-A       sort plus one tag
/game-assets/tag-A/tag-B        two tags, default sort only, A < B
/game-assets/<filter>           free | store | last-30-days | genre-*
/game-assets/<filter>/tag-A     filter plus one tag
```

Three tags is a **403**. A sort together with two tags is a **403**. A sort
together with a filter is a **403**. A filter with two tags is a **403**. The
filter must precede the tag (`/tag-pixel-art/free` is a **301**) and tags out of
lexicographic order is a **301** too. So views cannot be subdivided arbitrarily,
and with one exception there is no "NOT tag-X" facet to partition with —
coverage is the *union* of views, which is why it is measured rather than
assumed.

The exception is price: `free` and `store` are a true partition (measured
53,021 + 55,907 against a catalogue of 108,585, so every asset is in exactly
one), which is why big tags are crawled under both.

The plan rests on one observation: an asset is fully reachable if it carries at
least one tag small enough to page through. Assets carry ~5 tags each and most
tags are small, so a view per small tag covers the large majority; big tags are
taken under all four orderings and every filter, and the remainder is reached by
pairing big tags with each other. Each filter also gets one untagged view of its
own — the only shape that can reach an asset carrying no tag at all.

- Re-scrape and rebuild the index: `FORCE_REINDEX=true docker compose up indexer`
  (or hit `curl http://localhost:8081/trigger-fetch` directly).
- Throw away the scraped data: `docker compose down -v`.
- Tune the crawl rate with `SCRAPE_RPS` (default `2`). The rate is fixed: the
  fetcher does not speed up or slow down on its own. A 429 pauses every worker
  for the length of any `Retry-After` the server sent, then the crawl resumes
  at the same rate.
- If you see a flood of 429s, **check the cookie jar before touching
  `SCRAPE_RPS`**. itch.io sets an `itchio_token` cookie and refuses cookieless
  clients about half the time no matter how slowly they ask — measured at 8/16
  requests refused without the jar versus 0/16 with it, at one request per
  second. That failure looks exactly like an aggressive rate limit and cannot
  be fixed by slowing down. The `429s:` counter in the scrape progress log is
  there to make it visible.
- Smoke-test the whole pipeline quickly with `SCRAPE_MAX_PAGES`, which caps
  total pages across every view. It still exercises tag discovery, crawling,
  indexing, publishing and webserver startup end to end. Because such a run is
  deliberately partial, it also disarms `COVERAGE_FLOOR` and shrinks tag
  discovery — otherwise the run would refuse to publish, and would spend longer
  discovering tags than fetching assets.
- A full crawl reaches about **89%** of the catalogue (measured: 96,903 of
  108,808, over 40,465 pages in ~6 hours, with zero rate-limit responses). The
  remainder is assets that rank below the 200-page cutoff in *every* view they
  appear in, so no reachable URL exposes them.
  Applying `free`/`store` — a *true* partition — to every oversized tag moved
  coverage by 0.2 points, which is what rules out the more obvious explanation
  that the residual is assets whose every tag is too big to page through.
- A crawl checkpoints every 3 minutes, so a run killed part way through resumes
  instead of restarting. `CHECKPOINT_MAX_AGE` (default `24h`) bounds how stale a
  checkpoint may be before it is ignored; one is also discarded if the catalogue
  size moved underneath it, since coverage arithmetic and the slice plan were
  both computed against the old size.
- Coverage knobs: `COVERAGE_TARGET` (default `0.95`) stops the crawl once that
  fraction is collected. It does not trigger in practice — the crawl exhausts
  every slice first — and since it is an upper bound, lowering it only makes
  the run shorter and the index smaller. `COVERAGE_FLOOR` (default `0.70`)
  refuses to publish below that, leaving the previous index in place so a
  stalled run cannot replace a good index with a worse one. Keep the floor
  below ~89% or it will reject every otherwise-healthy run.
- `SLICE_MIN_YIELD` (default `0.05`) abandons a view once it stops yielding new
  assets. Assets appear in ~5 views each, so without it the overlap dominates
  the crawl.
- Tag discovery seeds from itch.io's own asset tag directory at `/tags/assets`,
  which lists ~607 tags in one document, then walks the co-tag links each facet
  page offers. It costs one request per tag and is cached as
  `tags.json`, refreshed when older than `TAG_CACHE_MAX_AGE` (default `168h`).
  `MAX_TAGS` (default `1000`) bounds the total.
- The seed source matters more than it looks. Seeding instead from the browse
  sitemap (~39 facets) and walking co-tags converges at ~135 tags — a clique of
  popular tags that link to each other — and the long tail never appears. Since
  an asset is only fully pageable if it carries a tag small enough to fit under
  the 200-page cap, losing the long tail loses coverage directly. If you see
  `falling back to the browse sitemap` in the logs, expect a much worse result.

### Configuring the local stack

Settings are passed as environment variables. The portable way — identical in
bash, PowerShell and cmd — is a `.env` file next to `docker-compose.yml`, which
Compose reads automatically:

```
cp .env.example .env      # `copy` on Windows
```

then edit it and run `docker compose up --build` as normal. `.env` is
gitignored.

If you'd rather set them inline, the syntax differs per shell:

```bash
# bash / zsh
SCRAPE_MAX_PAGES=20 docker compose up --build
```

```powershell
# PowerShell - the bash `VAR=value cmd` prefix form is NOT valid here
$env:SCRAPE_MAX_PAGES=20; docker compose up --build
```

> The webserver starts immediately and stays up during a scrape. On the very
> first run there is nothing to serve yet, so it answers `503 Cache not ready
> yet` until the crawl publishes, then picks the index up on its own within
> about a minute. On later runs it serves the *previous* index throughout the
> re-scrape and swaps to the new one atomically when it lands, so there is no
> downtime, and the swap is picked up within a few seconds.

### Tooling Dependencies
- [Golang](https://go.dev/)
- [Task](https://taskfile.dev/)
- [Docker](https://www.docker.com/)

### Running
The project is split up into two services:
- The `dataservice`, responsible for fetching the list of assets from [itch.io](https://itch.io/)
- The `webserver`, presenting the stored data with search tools.

`docker compose up --build` above is the supported path and runs both. To run
them directly instead, set `DATA_DIR` to a directory both can see:

```bash
DATA_DIR=./data go run ./cmd/dataservice   # then curl localhost:8080/trigger-fetch
DATA_DIR=./data PAGE_SIZE=36 go run ./cmd/webserver
```

- `task templ` will generate `.go` files from any `.templ` files. This is not
    required for building/running, but to provide code completion and stop the
    language server from complaining.

## Hosting it publicly

Both services are meant to run on one box behind a reverse proxy. The webserver
listens on 8080 and is the only thing that should ever be exposed.

> **Never proxy port 8081.** That is the dataservice, and `/trigger-fetch`
> takes no authentication. A public route to it lets anyone start a multi-hour
> crawl.

### Behind Cloudflare

Use a Tunnel (`cloudflared`) rather than opening a port: the connection is
outbound, so there is no port forward, no static IP, and your origin address
never appears in DNS. Better still, a tunnelled origin is not reachable except
through Cloudflare — a stronger property than a firewall rule, which is only as
good as its ruleset.

`docker-compose.yml` carries the tunnel as an opt-in `public` profile, so a
local run never needs a Cloudflare account:

1. In the Cloudflare dashboard, add the domain and let it serve DNS
   (nameserver change at the registrar; propagation is usually under an hour).
2. Zero Trust → Networks → Tunnels → create a tunnel, and copy its token.
3. Put the token in `.env` as `CLOUDFLARE_TUNNEL_TOKEN=…`, and set
   `TRUST_PROXY_HEADERS=true` in the same file.
4. Add a public hostname on the tunnel: `itchgrep.com` → `HTTP` →
   `webserver:8080`. The **service name**, not `localhost` — cloudflared
   resolves it over the compose network, and `localhost` inside that container
   is the container itself. Add `www` as a second hostname or a redirect rule.
5. `docker compose --profile public up -d`

Two settings matter on the origin:

```
TRUST_PROXY_HEADERS=true      # or every visitor shares one rate-limit bucket
RATE_LIMIT_RPS=5              # per client; 0 disables
```

`TRUST_PROXY_HEADERS` makes the rate limiter read `CF-Connecting-IP`. Behind a
tunnel every request arrives from the same address, so without it the whole
internet is one client. Leave it **off** if the server is directly exposed —
the header is attacker-controlled there, and honouring it would let any client
mint a new identity per request.

### Why not a Cloudflare Worker

A Worker is a V8 isolate: no filesystem, ~128 MB of memory, and a bundle
measured in single-digit megabytes. This webserver is a Go binary that memory
maps a bleve index — **739 MB at 89% of the catalogue** — and holds the asset
list in RAM besides. Those are not numbers that
shrink with tuning; they are three orders of magnitude apart, and Go compiled to
WASM does not change any of it.

The version of this that *would* work is a different program: publish to D1
(SQLite, so FTS5 instead of bleve) or Vectorize, and rewrite the query layer and
the templates in TypeScript. That trades the tag facets — which currently come
free from one bleve search — for `GROUP BY` over a join table, and it costs a
rewrite of everything in `internal/cache` and `internal/web`. Worth it only if
the goal is having no origin at all.

Cloudflare Containers can run this image as-is, but the index would have to be
pulled from R2 on every cold start, which is the tunnel with extra steps and a
bill. The tunnel is the right answer: the origin is a machine you already have,
and the CDN in front of it already absorbs the repeat traffic.

### What makes this cheap to serve

Search and browse are `GET`s that send `Cache-Control: public, max-age=300`, so
a proxy can serve repeats of a popular query without touching the origin. This
is why search is not a `POST`: no shared cache will ever store one, so every
search — including the same query a thousand times — would run a full bleve
pass on your machine.

Thumbnails are hotlinked to `img.itch.zone`, so image bandwidth never crosses
your connection. The flip side is that itch.io bears that cost and could block
by `Referer`, which would break every thumbnail at once.

### Before going public

- The webserver holds the whole dataset in memory and both the old and new
  index are open during a swap, so peak memory is roughly double steady state.
  With a ~740 MB index, give the container real headroom.
- You would be republishing scraped itch.io metadata. itchgrep ran publicly for
  years, so there is precedent, but personal use and a public service are
  different postures. Nothing is mirrored — every result links back to itch.io —
  which is the fact that makes the case defensible.
- Thumbnails hotlink `img.itch.zone`. A public site is a lot more traffic to
  somebody else's CDN than a personal one, and `Referer` blocking would break
  every thumbnail at once.

## Deploying in the Cloud

There is no cloud deployment path any more. `dataservice` and `webserver` used
to run on Cloud Run against Google Cloud Storage; storage is now a shared
directory, so both services expect a filesystem they can both see.

Running it on a server is still just `docker compose up -d` — put it behind a
reverse proxy and keep the `itchgrep-data` volume on real disk. A serverless
split would mean reintroducing an object store, which is the thing that was
removed.

## Testing

```bash
go test ./...
```

No external services are required. The `storage` tests used to need a running
`fake-gcs-server`; they now run against `t.TempDir()`.

## Contributing
- before posting a pull request, please use [`go fmt`](https://go.dev/blog/gofmt) to format your code.
- beginners to open source are welcome. if you'd like to contribute, but don't
    understand something, you're welcome to ask using an issue.
- please post feature requests as one issue per feature.
- before working on a larger contribution, please open an issue to ask if the
    feature you want to implement would be welcome.
- to maintain a transparent workflow, please keep all discourse regarding work
    on this repository in the github issues, don't message me through other
    channels to discuss this.
