// Package metrics is an in-process, no-PII tally of traffic, built to answer
// the public /stats page without ever reading a log line.
//
// logging.Init("", true) sends everything to stdout, so in a container there
// is no log file to read - the lines live in Docker's json-file driver,
// reachable only via the Docker socket, which the distroless image has no
// client for and must never be given. Parsing would also mean re-reading text
// containing client IPs and raw query strings on every page view, one bug
// away from rendering PII on a public page. Counters cannot leak what they
// never store.
//
// It is a separate package from internal/web, rather than living alongside
// the handlers, so the dependency stays one-directional: cmd -> web ->
// metrics -> storage. web and its templates read a Counters; nothing here
// reads back into web.
package metrics

import (
	"sync"
	"sync/atomic"
	"time"

	"itchgrep/internal/logging"
	"itchgrep/internal/storage"
	"itchgrep/pkg/models"
)

// hoursInDay is the width of the ring the histogram walks.
const hoursInDay = 24

// The route patterns RecordRequest classifies by, matching chi's own
// registration in cmd/webserver/main.go exactly - anything else, including an
// empty pattern for an unmatched path, falls through uncounted-by-route
// though it still adds to Total.
const (
	routeIndex   = "/"
	routeResults = "/results"
	routeAbout   = "/about"
	routeStats   = "/stats"
	routeStatic  = "/static/*"
)

// Counters is the live, concurrently-updated state. sync/atomic for the
// scalars, since the request path is the one genuinely concurrent place in
// this server; one small mutex for the 24-slot ring alone, because a bucket
// rollover is a read-compare-write that atomics cannot express.
type Counters struct {
	started   time.Time // process start, for uptime
	firstSeen time.Time // when counting began - can predate this process

	// base holds the totals restored from disk at startup. Kept separate from
	// the atomics below, rather than seeded into them, so a restart is never
	// mistaken for the moment counting began: firstSeen and base come from the
	// same restore and stay consistent with each other.
	base Snapshot

	total, index, results, about, stats, static atomic.Uint64
	searches                                    atomic.Uint64
	probes                                      atomic.Uint64 // internal only: feeds the aggregate log line, never the page

	mu    sync.Mutex // guards hours only
	hours [hoursInDay]models.HourBucket
}

// Snapshot is a point-in-time, immutable read of the counters - what the
// template layer and the persistence layer both work from, so neither has to
// touch an atomic or take the ring's mutex directly.
type Snapshot struct {
	Started   time.Time
	FirstSeen time.Time

	Total, Index, Results, About, Stats, Static uint64
	Searches                                    uint64

	Hours [hoursInDay]models.HourBucket
}

// New starts a Counters with nothing restored: counting begins now.
func New() *Counters {
	now := time.Now()
	return &Counters{started: now, firstSeen: now}
}

// Restore reads traffic.json and resumes counting from it, falling back to
// New on any error - a missing file is normal on first boot and on any
// deploy before this existed, not a fault worth logging as one.
func Restore() *Counters {
	c := New()

	t, err := storage.GetTraffic()
	if err != nil {
		return c
	}

	if !t.FirstSeen.IsZero() {
		c.firstSeen = t.FirstSeen
	}
	c.base = Snapshot{
		Total: t.Total, Index: t.Index, Results: t.Results,
		About: t.About, Stats: t.Stats, Static: t.Static,
		Searches: t.Searches,
	}

	// Only within the last 24h: a server down for a week must not draw a
	// week-old histogram back onto the current one.
	cutoff := time.Now().UTC().Unix()/3600 - hoursInDay
	for _, hb := range t.Hours {
		if hb.EpochHour == 0 || hb.EpochHour < cutoff {
			continue
		}
		c.hours[hb.EpochHour%hoursInDay] = hb
	}
	return c
}

// RecordRequest tallies one completed request. routePattern comes from chi's
// route table (chi.RouteContext(r.Context()).RoutePattern(), read after
// dispatch), so an attacker cannot mint a label by requesting an arbitrary
// path - anything that did not match a route arrives here as "". filtered is
// r.URL.RawQuery != "", a boolean derived and discarded; the query text
// itself is never stored anywhere in this package.
//
// A nil receiver is a no-op, so the request path can call this unconditionally
// even where a *Counters is optional.
func (c *Counters) RecordRequest(routePattern string, filtered bool, status int) {
	if c == nil {
		return
	}
	c.total.Add(1)
	switch routePattern {
	case routeIndex:
		c.index.Add(1)
	case routeResults:
		c.results.Add(1)
	case routeAbout:
		c.about.Add(1)
	case routeStats:
		c.stats.Add(1)
	case routeStatic:
		c.static.Add(1)
	}

	// A search is a query actually answered against the catalogue, not merely
	// a hit on a route that can carry one - "/" and "/results" both render
	// results, and a failed lookup (503 while the index loads, 400 on a bad
	// page) served nothing to count.
	if filtered && status < 400 && (routePattern == routeIndex || routePattern == routeResults) {
		c.searches.Add(1)
		c.bumpHour(time.Now())
	}
}

// RecordProbe tallies one request the probe filter turned away before it ever
// reached routing. Internal only: it feeds the aggregate log line in
// StartSnapshotter, never the public page - see stats.templ.
func (c *Counters) RecordProbe() {
	if c == nil {
		return
	}
	c.probes.Add(1)
}

// bumpHour records one search against the bucket for now's hour, resetting
// that slot first if it belongs to a different hour than what is stored there
// - the ring is 24 slots wide but time keeps moving, so slot N belongs to
// whichever hour last wrote it.
func (c *Counters) bumpHour(now time.Time) {
	epochHour := now.UTC().Unix() / 3600
	c.mu.Lock()
	defer c.mu.Unlock()
	b := &c.hours[epochHour%hoursInDay]
	if b.EpochHour != epochHour {
		*b = models.HourBucket{EpochHour: epochHour}
	}
	b.Searches++
}

// Snapshot returns the current totals: restored history plus everything
// tallied since. A nil receiver reads as the zero Snapshot, so a handler with
// no metrics configured (see internal/web/handlers_test.go) renders a page
// with nothing on it rather than panicking.
func (c *Counters) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.mu.Lock()
	hours := c.hours
	c.mu.Unlock()
	return Snapshot{
		Started:   c.started,
		FirstSeen: c.firstSeen,
		Total:     c.base.Total + c.total.Load(),
		Index:     c.base.Index + c.index.Load(),
		Results:   c.base.Results + c.results.Load(),
		About:     c.base.About + c.about.Load(),
		Stats:     c.base.Stats + c.stats.Load(),
		Static:    c.base.Static + c.static.Load(),
		Searches:  c.base.Searches + c.searches.Load(),
		Hours:     hours,
	}
}

func (c *Counters) toTraffic() models.Traffic {
	s := c.Snapshot()
	return models.Traffic{
		FirstSeen: s.FirstSeen,
		Total:     s.Total, Index: s.Index, Results: s.Results,
		About: s.About, Stats: s.Stats, Static: s.Static,
		Searches: s.Searches,
		Hours:    s.Hours,
	}
}

// StartSnapshotter persists the counters every interval and, when the probe
// count moved since the last tick, logs one aggregate line for it - the
// backstop's only visible trace, since ProbeFilter itself logs nothing per
// request. There is no graceful shutdown today, so a restart loses up to one
// interval of counts; worth noting here rather than adding signal handling
// for this alone.
func (c *Counters) StartSnapshotter(every time.Duration) {
	go func() {
		var lastProbes uint64
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for range ticker.C {
			if err := storage.PutTraffic(c.toTraffic()); err != nil {
				logging.Error("Failed to persist traffic counters: %v", err)
			}
			if p := c.probes.Load(); p != lastProbes {
				logging.Info("blocked %d scanner probes", p-lastProbes)
				lastProbes = p
			}
		}
	}()
}
