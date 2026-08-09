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
- **[Google Cloud](https://cloud.google.com/?hl=en)**
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

This starts a local GCS emulator, builds and runs the `dataservice`, triggers a
full scrape + index of itch.io, and only then starts the `webserver` on
[localhost:8080](http://localhost:8080).

> The first run crawls the whole asset catalogue (~108,000 assets) at a
> deliberately polite 2 requests/second, so expect an hour or more. Progress is
> visible in the `dataservice` logs. The result is kept in the `gcs-data`
> docker volume, so subsequent `docker compose up` runs skip the crawl and come
> up immediately.

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
```

Three tags is a **403**. A sort together with two tags is a **403**. Tags out of
lexicographic order is a **301**. So views cannot be subdivided arbitrarily, and
there is no "NOT tag-X" facet to partition with — coverage is the *union* of
views, which is why it is measured rather than assumed.

The plan rests on one observation: an asset is fully reachable if it carries at
least one tag small enough to page through. Assets carry ~9 tags each and most
tags are small, so a view per small tag covers the large majority; big tags are
taken under all four orderings, and the remainder is reached by pairing big tags
with each other.

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
  indexing, archiving, upload and webserver startup end to end. Because such a
  run is deliberately partial, it also disarms `COVERAGE_FLOOR` and shrinks tag
  discovery — otherwise the run would refuse to publish, and would spend longer
  discovering tags than fetching assets.
- A full crawl reaches about **79%** of the catalogue (measured: 86,084 of
  108,585, over 8,931 pages in ~75 minutes). The remainder is assets whose every
  tag is too big to page through, plus untagged ones; there is no view shape
  that reaches them.
- Coverage knobs: `COVERAGE_TARGET` (default `0.95`) stops the crawl once that
  fraction is collected. It does not trigger in practice — the crawl exhausts
  every slice first — and since it is an upper bound, lowering it only makes
  the run shorter and the index smaller. `COVERAGE_FLOOR` (default `0.70`)
  refuses to publish below that, leaving the previous index in place so a
  stalled run cannot replace a good index with a worse one. Keep the floor
  below ~79% or it will reject every otherwise-healthy run.
- `SLICE_MIN_YIELD` (default `0.05`) abandons a view once it stops yielding new
  assets. Assets appear in ~9 views each, so without it the overlap dominates
  the crawl.
- Tag discovery seeds from itch.io's own asset tag directory at `/tags/assets`,
  which lists ~607 tags in one document, then walks the co-tag links each facet
  page offers. It costs one request per tag and is cached in the bucket as
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

> Note that the webserver deliberately does not start until the scrape has
> finished and data exists in the bucket, so port 8080 will refuse connections
> for the duration of the scrape. Port 4443 is the GCS emulator, not the app.

### Tooling Dependencies
- [Golang](https://go.dev/)
- [Task](https://taskfile.dev/)
- [Docker](https://www.docker.com/)
- [gcloud](https://cloud.google.com/sdk/gcloud)

### Running
The project is split up into two services:
- The `dataservice`, responsible for fetching the list of assets from [itch.io](https://itch.io/)
- The `webserver`, presenting the stored data with search tools.

Use the included [Taskfile](https://taskfile.dev/) to run these services.
> - `task local-dataservice` will launch the `dataservice` with a local instance
>     of GCS. Send a `GET` request to its trigger endpoint: 
>     `curl -X GET "localhost:8080/trigger-fetch"`.
>     This will cause the service to scrape the data from itch.io, index it and
>     store both data and index on the local GCS.
- !! The way of running described above is currently not working properly, I am
    looking for assistance on this. Please see [Issue #1](https://github.com/wintermute-cell/itchgrep/issues/1).
    In the meantime use `task local-dataservice-temp-fix`. This runs the
    `dataservice` without docker.
- `task local-webserver` will build and run the web server in a Docker
    container together with the local GCS in a separate container. `Templ`
    templates are not copied during the build, but generated inside the
    container.
- `task templ` will generate `.go` files from any `.templ` files. This is not
    required for building/running, but to provide code completion and stop the
    language server from complaining.

## Deploying in the Cloud
The project was created with the intention of hosting both `dataservice` and
`webserver` on Google Cloud Run. The asset data is intended to be stored in
Google Cloud Store.

> Google Cloud Run can be replaced with any serverless platform, and Google
> Cloud Store can be replaced with any object store, but some work will be
> required if this is your goal, and the following instructions will assume
> Google Cloud services.

To deploy the project on Google Cloud, follow the steps below.

### Setting up `gcloud`
A couple of preparation steps:
- Make sure, you have set up a project in your [Google Cloud Console](https://console.cloud.google.com).
- In your project, create an object store with the name `itchgrep-data`. (You
    can also use another name here, but you must then change the `const` in the
    file `internal/storage/storage.go` accordingly)
- In your project, create a new [service account](https://console.cloud.google.com/iam-admin/serviceaccounts), and give
    it the role of `Cloud Run Invoker`. Later, we will attach this service account
    to a scheduler job, to regularly trigger a run of the dataservice.
- Make sure you have installed the [gcloud CLI](https://cloud.google.com/sdk/gcloud).
- You may use `task gcloud-setup` to configure `gcloud` for use with this
    project. Otherwise, make sure to properly configure manually.
- Adjust all instances of the variables `PROJECT_ID`, `REGION` and `LOCATION`
    found in the `Taskfile` to fit your Google Cloud project configuration.

### Deploying the dataservice and setting up a Scheduler Job
- Run `task deploy-dataservice` to build and deploy the dataservice. At the end
    you will receive a service URL for the newly deployed dataservice.
- Now, to create a scheduler job, run the following command. Notice how we are
    passing security critical information as environment variables:
    ```bash
    DATASERVICE_URL=https://dataservice-ly6n5ozylq-od.a.run.app \
    SERVICE_ACCOUNT_EMAIL=cloud-run-invoker@itchgrep.iam.gserviceaccount.com \
    go-task create-dataservice-scheduler-job
    ```
- At this point, you should manually force a run of the dataservice-job in the
    [cloud scheduler console](https://console.cloud.google.com/cloudscheduler).
    This will ensure that the object store is populated with data, before we
    start the webserver for the first time. You should wait around 5 minutes
    after doing that, before deploying the webserver, so the dataservice has
    time to fetch and store new data.

### Deploying the webserver
Run `task deploy-webserver`. No further work should be required.

## Testing
Tests can be run by using the included [Taskfile](https://taskfile.dev/).

- `task test`: Runs all of the test tasks below.
- `task test-storage`: Tests the `storage` package, requires `Docker` to be running.

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
