package fetcher

import (
	"encoding/json"
	"fmt"
	"itchgrep/internal/logging"
	"itchgrep/pkg/models"
	"math"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PuerkitoBio/goquery"
)

type itchResponse struct {
	NumItems int64  `json:"num_items"`
	Page     int64  `json:"page"`
	Content  string `json:"content"`
}

// userAgent identifies the crawler to itch.io and links back to the repo, so
// operators there can see who is hitting them and why.
const userAgent = "itchgrep/1.0 (+https://github.com/wintermute-cell/itchgrep)"

// httpClient is shared by all fetcher requests. http.DefaultClient has no
// timeout, so a hung connection would leak a goroutine permanently; this
// client bounds every request to 30s.
//
// The cookie jar is load-bearing, not incidental. itch.io sets an
// itchio_token cookie on every response and refuses cookieless clients with
// 429 about half the time no matter how slowly they ask - measured at 8/16
// refused without the jar versus 0/16 with it, at the same one request per
// second. Dropping the jar looks exactly like an aggressive rate limit and
// cannot be fixed by slowing down.
var httpClient = newHTTPClient()

func newHTTPClient() *http.Client {
	jar, err := cookiejar.New(nil) // documented never to fail for nil options
	if err != nil {
		panic(fmt.Sprintf("fetcher: building cookie jar: %v", err))
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Jar:     jar,
	}
}

// defaultScrapeRPS is the outbound request rate used when SCRAPE_RPS is
// unset, unparseable, or non-positive. itch.io returns 429 aggressively
// above a low single-digit rate, so this is deliberately conservative.
const defaultScrapeRPS = 2

// maxScrapeRPS bounds SCRAPE_RPS. Beyond this the request spacing would
// round down to zero and the limiter would stop pacing at all.
const maxScrapeRPS = 1000

// rateLimiter paces every outbound request across all workers at a fixed
// rate, and provides a fleet-wide pause so that a 429 holds back every worker
// rather than only the one that saw it.
//
// A plain ticker is not enough for that second part. The scrape runs several
// workers against one shared limiter, so a worker that sleeps in a
// per-request backoff just hands its slot to another worker: the aggregate
// rate offered to itch.io stays pinned no matter how many 429s come back, and
// a Retry-After honoured by one worker is ignored by the rest. A pause has to
// be global to mean anything.
//
// The rate itself is deliberately NOT adaptive. An earlier version halved its
// own rate on each 429 and crept back up; measurement then showed itch.io's
// 429s are not rate-driven at all - they come of sending no itchio_token
// cookie, see httpClient above - so the adaptation cost throughput without
// reducing refusals, and could ratchet itself down to a floor that took
// minutes of clean traffic to escape. Refusals are surfaced through Refusals
// instead, where they are visible rather than silently throttling the scrape.
//
// next is the earliest wall-clock time the next request may start. acquire
// reserves a slot by advancing it, so concurrent callers queue up in a
// stable order rather than bunching.
type rateLimiter struct {
	mu       sync.Mutex
	interval time.Duration // fixed spacing between requests, from SCRAPE_RPS
	next     time.Time     // earliest start time for the next request

	// epoch identifies the current refusal episode. Every request carries the
	// epoch it was issued under, and only a 429 from the current epoch opens
	// a new pause. Without this, the several requests already in flight when
	// itch.io starts refusing all report 429 within moments of each other,
	// and a single page retrying with a growing backoff could keep pushing
	// the global pause out and strangle every other worker with it.
	epoch uint64

	// pausedUntil is a hard fleet-wide gate, kept separate from the queue in
	// next because a pause has to be able to hold back workers that already
	// took a slot before the 429 landed.
	pausedUntil time.Time
}

// acquire blocks until this caller's reserved slot comes up. It returns the
// refusal epoch the request is being issued under, which must be handed back
// to pause if the request comes back 429.
func (l *rateLimiter) acquire() uint64 {
	l.mu.Lock()
	start := l.next
	if now := time.Now(); start.Before(now) {
		start = now
	}
	l.next = start.Add(l.interval)
	l.mu.Unlock()

	if d := time.Until(start); d > 0 {
		time.Sleep(d)
	}

	// The slot above was reserved before this request's turn came round. A
	// 429 reported in the meantime may have opened a cooldown that this
	// worker must respect too, so re-check rather than firing on the strength
	// of a stale reservation. The epoch is read here, after the wait, so a
	// request that sat out a cooldown is correctly issued under the new one.
	for {
		l.mu.Lock()
		wait := time.Until(l.pausedUntil)
		epoch := l.epoch
		l.mu.Unlock()

		if wait <= 0 {
			return epoch
		}
		time.Sleep(wait)
	}
}

// pause holds the whole fleet off until wait has elapsed, so a 429's
// Retry-After applies to all workers rather than only the one that saw it.
// It takes effect at most once per refusal episode.
func (l *rateLimiter) pause(epoch uint64, wait time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Requests issued before the last pause were already in flight when we
	// backed off; their 429s are echoes of that same episode, not fresh cause
	// to hold everyone back again.
	if epoch != l.epoch {
		return
	}
	l.epoch++

	if resume := time.Now().Add(wait); l.pausedUntil.Before(resume) {
		l.pausedUntil = resume
		// Push the queue out too, so the fleet does not all fire the instant
		// the pause lifts.
		if l.next.Before(resume) {
			l.next = resume
		}
	}
}

// refusals counts the 429s itch.io has returned since the process started.
var refusals atomic.Int64

// Refusals reports that count. It is surfaced in the scrape's progress log so
// that refusals show up as a number rather than as unexplained slow progress.
func Refusals() int64 {
	return refusals.Load()
}

var (
	rateLimiterOnce sync.Once
	sharedLimiter   *rateLimiter
)

// newRateLimiter builds a limiter that spaces requests at 1/rps.
func newRateLimiter(rps int) *rateLimiter {
	return &rateLimiter{interval: time.Second / time.Duration(rps)}
}

// initRateLimiter reads SCRAPE_RPS (once, via rateLimiterOnce) and builds the
// package-level limiter.
func initRateLimiter() {
	rps := defaultScrapeRPS
	if v := os.Getenv("SCRAPE_RPS"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			rps = parsed
		}
	}
	// Clamped so an absurd value can't make the spacing round down to zero,
	// which would disable pacing entirely.
	if rps > maxScrapeRPS {
		logging.Warning("SCRAPE_RPS=%d exceeds the maximum of %d, clamping", rps, maxScrapeRPS)
		rps = maxScrapeRPS
	}
	logging.Info("Fetcher rate limit: %d requests/second", rps)
	sharedLimiter = newRateLimiter(rps)
}

// limiter returns the package-level limiter, building it on first use.
func limiter() *rateLimiter {
	rateLimiterOnce.Do(initRateLimiter)
	return sharedLimiter
}

// doGet issues a GET request to url via the shared httpClient, setting the
// crawler's identifying User-Agent header. Every call is paced by the
// package-level limiter, including calls made from inside retry loops, so no
// code path can bypass the configured request rate.
// It returns the refusal epoch the request went out under, which the caller
// must pass to pause on a 429.
func doGet(url string) (*http.Response, uint64, error) {
	epoch := limiter().acquire()

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, epoch, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	return resp, epoch, err
}

func ParseAssetPage(respData itchResponse, pageNum int64) ([]models.Asset, error) {
	// parse html
	queryDoc, err := goquery.NewDocumentFromReader(strings.NewReader(respData.Content))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	// iterate over each asset
	assets := make([]models.Asset, 0)
	queryDoc.Find(".game_cell").Each(func(i int, s *goquery.Selection) {
		// For each item found, get the band and title
		gameId, _ := s.Attr("data-game_id")
		title := s.Find(".title").Text()
		author := s.Find(".game_author").Children().First().Text()
		description := s.Find(".game_text").Text()
		linkNode := s.Find(".thumb_link")
		link, _ := linkNode.Attr("href")
		thumbUrl, _ := linkNode.Children().First().Attr("data-lazy_src")
		assets = append(assets, models.Asset{
			GameId:        gameId,
			Title:         title,
			Author:        author,
			Description:   description,
			Link:          link,
			ThumbUrl:      thumbUrl,
			InvPopularity: pageNum,
		})
	})
	return assets, nil
}

// FetchOutcome distinguishes the three ways fetching a page can end. A 404 is
// not a failure: itch.io serves at most MaxPagesPerView pages per view, and a
// view holding fewer than that 404s past its end. Folding that into the error
// path would make a slice's normal termination indistinguishable from a real
// problem, and would log an error for every page past the end.
type FetchOutcome int

const (
	FetchOK FetchOutcome = iota
	FetchExhausted
	FetchFailed
)

func FetchAssetPage(slice Slice, pageNum int64) (itchResponse, FetchOutcome) {
	maxAttempts := 21
	baseDelay := 1 * time.Second

	for attempt := 0; attempt < maxAttempts; attempt++ {
		url := slice.PageURL(pageNum)
		resp, epoch, err := doGet(url)
		if err != nil {
			logging.Warning("Failed to fetch data at attempt %d: %v", attempt, err)
			if attempt < maxAttempts-1 {
				time.Sleep(calculateBackoff(attempt, baseDelay))
			}
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfterHeader := resp.Header.Get("Retry-After")
			resp.Body.Close()
			wait := calculateBackoff(attempt, baseDelay)
			source := "backoff"
			if d, ok := parseRetryAfter(retryAfterHeader); ok {
				wait = d
				source = "Retry-After"
			}
			// The pause is applied to the limiter rather than slept here, so
			// it holds back every worker instead of just this one.
			refusals.Add(1)
			limiter().pause(epoch, wait)
			logging.Warning("429 on %s page %d (attempt %d/%d): pausing all fetches for %s (%s)",
				slice.Label(), pageNum, attempt+1, maxAttempts, wait.Round(time.Millisecond), source)
			continue
		}

		// Past the last page of this view. Normal termination, not an error.
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			return itchResponse{}, FetchExhausted
		}

		if resp.StatusCode != http.StatusOK {
			logging.Error("Unexpected status code %d %s for %s page %d",
				resp.StatusCode, resp.Status, slice.Label(), pageNum)
			resp.Body.Close()
			return itchResponse{}, FetchFailed
		}

		var respData itchResponse
		err = json.NewDecoder(resp.Body).Decode(&respData)
		resp.Body.Close()
		if err != nil {
			logging.Error("Failed to decode response: %v", err)
			return itchResponse{}, FetchFailed
		}
		return respData, FetchOK
	}

	logging.Error("Failed to fetch %s page %d after %d attempts", slice.Label(), pageNum, maxAttempts)
	return itchResponse{}, FetchFailed
}

// maxBackoff caps the delay calculateBackoff can return, so a large attempt
// number can't stall the scrape indefinitely.
const maxBackoff = 60 * time.Second

// calculateBackoff calculates the delay for the next retry attempt using
// exponential backoff with bounded jitter. The jitter factor is drawn from
// [0.5, 1.5), so it can only ever scale the delay up or down by half - it
// can never collapse the delay toward zero the way a multiplicative
// rand.Float64() jitter (which ranges over [0, 1)) can.
func calculateBackoff(attempt int, baseDelay time.Duration) time.Duration {
	expFactor := math.Pow(1.36, float64(attempt))
	delay := expFactor * float64(baseDelay)

	jitterFactor := 0.5 + rand.Float64() // [0.5, 1.5)
	result := time.Duration(delay * jitterFactor)

	if result > maxBackoff {
		return maxBackoff
	}
	return result
}

// maxRetryAfter bounds how long we'll honour a server-supplied Retry-After
// value, so a hostile or misconfigured response can't stall the scrape.
const maxRetryAfter = 120 * time.Second

// parseRetryAfter parses an HTTP Retry-After header value, which per RFC
// 9110 is either delta-seconds (an integer) or an HTTP-date. It returns the
// duration to wait and whether the header parsed to a usable, positive
// duration. The result is clamped to maxRetryAfter.
func parseRetryAfter(value string) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}

	if secs, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
		d := time.Duration(secs) * time.Second
		if d <= 0 {
			return 0, false
		}
		if d > maxRetryAfter {
			d = maxRetryAfter
		}
		return d, true
	}

	if t, err := http.ParseTime(value); err == nil {
		d := time.Until(t)
		if d <= 0 {
			return 0, false
		}
		if d > maxRetryAfter {
			d = maxRetryAfter
		}
		return d, true
	}

	return 0, false
}

// assetCountURL is the itch.io page GetAssetCount scrapes for the total
// asset count. It is a package-level var (rather than inlined) so tests can
// point it at an httptest.Server.
var assetCountURL = "https://itch.io/game-assets"

func GetAssetCount() (int64, error) {
	maxAttempts := 21
	baseDelay := 1 * time.Second

	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, epoch, err := doGet(assetCountURL)
		if err != nil {
			logging.Warning("Failed to fetch asset count at attempt %d: %v", attempt, err)
			if attempt < maxAttempts-1 {
				time.Sleep(calculateBackoff(attempt, baseDelay))
			}
			continue
		}

		if resp.StatusCode == http.StatusOK {
			queryDoc, err := goquery.NewDocumentFromReader(resp.Body)
			resp.Body.Close()
			if err != nil {
				return 0, fmt.Errorf("failed to parse HTML: %w", err)
			}

			// parse "(53,665 results)" -> 53665
			resultCountStr := queryDoc.Find(".game_count").Text()
			re := regexp.MustCompile(`[\d,]+`)
			match := re.FindString(resultCountStr)
			numberStr := strings.ReplaceAll(match, ",", "")
			number, err := strconv.ParseInt(numberStr, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("failed to parse result count: %w", err)
			}
			return number, nil
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfterHeader := resp.Header.Get("Retry-After")
			resp.Body.Close()
			wait := calculateBackoff(attempt, baseDelay)
			source := "backoff"
			if d, ok := parseRetryAfter(retryAfterHeader); ok {
				wait = d
				source = "Retry-After"
			}
			refusals.Add(1)
			limiter().pause(epoch, wait)
			logging.Warning("429 on asset count (attempt %d/%d): pausing all fetches for %s (%s)",
				attempt+1, maxAttempts, wait.Round(time.Millisecond), source)
			continue
		}

		resp.Body.Close() // Ensure the response body is closed before returning
		return 0, fmt.Errorf("unexpected status code: %d %s", resp.StatusCode, resp.Status)
	}

	return 0, fmt.Errorf("failed to fetch asset count after %d attempts", maxAttempts)
}
