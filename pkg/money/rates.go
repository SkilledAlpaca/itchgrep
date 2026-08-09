package money

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ECBSource is where the live rates come from: the European Central Bank's
// daily euro reference rates, published as a public XML file with no key and no
// terms to accept. Chosen over the various free FX APIs because it is a
// central bank publishing its own numbers rather than a service that may
// disappear or start charging.
//
// The URL is shown to visitors next to every converted figure, so that a price
// they might act on is traceable to something other than this site's say-so.
const ECBSource = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml"

// Rates is a snapshot of reference rates, expressed the way the ECB publishes
// them: units of each currency per one euro.
//
// Dated on purpose. A rate is only true for the day it was published, and this
// site serves a converted figure to somebody who may be deciding whether to buy
// something - so the date travels with the number all the way to the page,
// rather than being an implementation detail.
type Rates struct {
	Date   string             // ISO date the snapshot was published
	Source string             // where it came from
	PerEUR map[string]float64 // units per 1 EUR; EUR itself is 1
}

// fallbackDate is deliberately the day this table was written, not the day it
// is used. An out-of-date snapshot that says so is honest; one that claims to
// be today's is not.
const fallbackDate = "2026-08-09"

// Fallback is the baked-in snapshot, used until a crawl fetches a live one and
// whenever that fetch fails.
//
// These figures are approximate and will drift. That is tolerable for what they
// are used for - putting a rough local figure beside a price, and ordering
// assets by roughly how much they cost - and intolerable for anything else, so
// nothing else uses them. Every converted amount is rendered with a "≈" and
// this date attached.
func Fallback() Rates {
	return Rates{
		Date:   fallbackDate,
		Source: ECBSource,
		PerEUR: map[string]float64{
			"EUR": 1,
			"USD": 1.09,
			"GBP": 0.85,
			"JPY": 165,
			"CAD": 1.48,
			"AUD": 1.64,
			"NZD": 1.79,
			"CHF": 0.95,
			"SEK": 11.4,
			"PLN": 4.30,
			"CZK": 25.0,
			"HUF": 395,
			"RON": 4.97,
			"BRL": 5.90,
			"MXN": 19.8,
			"INR": 91.0,
			"KRW": 1470,
			"SGD": 1.46,
			"HKD": 8.50,
			"TWD": 35.2,
			"THB": 39.0,
			"ZAR": 20.0,
			"TRY": 35.5,
			"RUB": 98.0,
		},
	}
}

// Valid reports whether a snapshot is usable at all: a rates table with no euro
// in it cannot convert anything, and silently treating that as "all rates are
// zero" would price the whole catalogue at nothing.
func (r Rates) Valid() bool { return r.PerEUR["EUR"] == 1 && len(r.PerEUR) > 1 }

// Has reports whether a currency can be converted to or from.
func (r Rates) Has(currency string) bool {
	_, ok := r.PerEUR[currency]
	return ok
}

// Currencies lists what can be converted to, alphabetically.
func (r Rates) Currencies() []string {
	out := make([]string, 0, len(r.PerEUR))
	for c := range r.PerEUR {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Convert restates an amount in another currency, rounding to that currency's
// own precision. Reports false when either side is missing from the snapshot,
// which callers show as the original price rather than as a guess.
func (r Rates) Convert(m Money, to string) (Money, bool) {
	from, ok := r.PerEUR[m.Currency]
	if !ok || from == 0 {
		return Money{}, false
	}
	rate, ok := r.PerEUR[to]
	if !ok {
		return Money{}, false
	}

	major := m.Amount() / from * rate
	minor := int64(major*float64(pow10(exponent(to))) + 0.5)
	return Money{Minor: minor, Currency: to, Display: Format(minor, to)}, true
}

// USD is the amount in dollars, for the price filters and the cheapest-first
// ordering. Both need one number per asset that compares across currencies, and
// dollars are what itch.io's own thresholds are quoted in.
//
// Returns false rather than zero when the currency is unknown: a document
// stored as costing nothing would be indistinguishable from a free one, and
// would surface under a "free" filter it does not belong in.
func (r Rates) USD(m Money) (float64, bool) {
	usd, ok := r.PerEUR["USD"]
	from, fromOK := r.PerEUR[m.Currency]
	if !ok || !fromOK || from == 0 {
		return 0, false
	}
	return m.Amount() / from * usd, true
}

// layouts says where a currency's symbol goes. Only the ones itch.io actually
// shows are listed; anything else falls back to a suffixed ISO code, which is
// unambiguous if inelegant.
var layouts = map[string]struct{ prefix, suffix string }{
	"USD": {"$", ""},
	"EUR": {"", "€"},
	"GBP": {"£", ""},
	"JPY": {"¥", ""},
	"CAD": {"CA$", ""},
	"AUD": {"A$", ""},
	"NZD": {"NZ$", ""},
	"BRL": {"R$", ""},
	"INR": {"₹", ""},
	"KRW": {"₩", ""},
	"PLN": {"", " zł"},
	"SEK": {"", " kr"},
	"CZK": {"", " Kč"},
	"ZAR": {"R", ""},
	"CHF": {"CHF ", ""},
}

// Format renders an amount the way itch.io would have.
func Format(minor int64, currency string) string {
	exp := exponent(currency)
	whole := minor / pow10(exp)
	text := strconv.FormatInt(whole, 10)
	if exp > 0 {
		frac := minor % pow10(exp)
		if frac < 0 {
			frac = -frac
		}
		text = fmt.Sprintf("%s.%0*d", text, exp, frac)
	}

	if l, ok := layouts[currency]; ok {
		return l.prefix + text + l.suffix
	}
	return text + " " + currency
}

// ParseECB reads the ECB's daily reference XML.
//
// Kept here, taking bytes rather than doing its own HTTP, so the parser is
// testable against a fixture and this package never needs the network. The
// fetch itself belongs to whoever is already making outbound requests.
func ParseECB(xml []byte) (Rates, error) {
	body := string(xml)

	// The document is a nest of <Cube> elements: an outer one carrying
	// time="YYYY-MM-DD" and a row of inner ones carrying currency and rate.
	// Matched by attribute rather than by unmarshalling the whole schema,
	// because the shape of the nesting has changed before and the attributes
	// have not.
	date := attr(body, `time="`)
	if date == "" {
		return Rates{}, fmt.Errorf("money: no time attribute in ECB response")
	}

	perEUR := map[string]float64{"EUR": 1}
	for _, chunk := range strings.Split(body, "<Cube")[1:] {
		currency := attr(chunk, `currency="`)
		raw := attr(chunk, `rate="`)
		if currency == "" || raw == "" {
			continue
		}
		rate, err := strconv.ParseFloat(raw, 64)
		if err != nil || rate <= 0 {
			continue
		}
		perEUR[currency] = rate
	}

	r := Rates{Date: date, Source: ECBSource, PerEUR: perEUR}
	if !r.Valid() {
		return Rates{}, fmt.Errorf("money: ECB response carried no usable rates")
	}
	return r, nil
}

func attr(s, key string) string {
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	rest := s[i+len(key):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}
