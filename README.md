<!-- LTeX: language=en-US -->

<div align="center">

# itchgrep.com

### _Discover the Perfect Assets for Your Games_

🔍 search through itch.io assets using text queries; find what you need without relying solely on tags.

🌐 Visit [itchgrep.com](https://itchgrep.com/) to start exploring.

</div>

<div align="left">

### 💖 Support itchgrep

Your support fuels our passion and helps keep the servers running! If you appreciate what we do and want to contribute to our journey, consider:

- 🍵 [**Buying me a coffee!** Your generosity is immensely appreciated, and every cup allows me to keep working on cool stuff.](https://www.buymeacoffee.com/winterv)

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
> deliberately polite 2 requests/second, so expect an hour or more. Progress is
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
- A full crawl reaches about **79.5%** of the catalogue (measured: 86,414 of
  108,697, over 9,878 pages in ~90 minutes). The remainder is assets no tag view
  reaches at all, because they carry no tag in the discovered vocabulary.
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
  below ~79% or it will reject every otherwise-healthy run.
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
never appears in DNS. Point it at `http://localhost:8080`.

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
  With a ~600 MB index, give the container real headroom.
- You would be republishing scraped itch.io metadata. Upstream runs
  itchgrep.com publicly, so there is precedent, but personal use and a public
  service are different postures.

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
