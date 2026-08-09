// Package money turns itch.io's displayed prices into something orderable.
//
// itch.io prices in whichever currency the seller chose and gives no exchange
// rate, so a listing is a string like "$4.95" or "9.97€" and nothing more. That
// is enough to say free-or-paid and nothing else: no "under $5", no cheapest
// first, because 9.97 of one thing does not compare to 4.95 of another.
//
// This package does the two jobs that unlock: read the string into an amount
// and a currency, and convert between currencies with a dated snapshot of
// reference rates. The conversions are openly approximate - rates move daily
// and the snapshot does not - so every converted figure is presented as such,
// with the source and date attached. See rates.go.
package money

import (
	"strings"
	"unicode"
)

// Money is an amount in a known currency, alongside exactly what itch.io
// displayed. Display is kept because it is the only value here that is
// certainly correct: the parse could have misread an unfamiliar format, and a
// buyer is going to be charged whatever itch.io says, not what this computed.
type Money struct {
	Minor    int64  // amount in minor units, e.g. cents
	Currency string // ISO 4217
	Display  string // the original string, verbatim
}

// Amount is the value in major units, for arithmetic and comparison.
func (m Money) Amount() float64 {
	return float64(m.Minor) / float64(pow10(exponent(m.Currency)))
}

// Zero reports whether this costs nothing.
func (m Money) Zero() bool { return m.Minor == 0 }

// symbols maps a leading or trailing marker to its currency. Matched
// longest-first, so "CA$" is never read as "$" with a stray "CA".
//
// Ambiguity is resolved by picking the likeliest seller rather than by
// refusing: a bare "kr" is Swedish, Norwegian or Danish and the listing does
// not say which. Getting that wrong moves a converted figure by a few percent,
// which is well inside what a day-old exchange rate already costs.
var symbols = []struct {
	marker   string
	currency string
}{
	{"CA$", "CAD"}, {"NZ$", "NZD"}, {"HK$", "HKD"}, {"NT$", "TWD"},
	{"MX$", "MXN"}, {"US$", "USD"}, {"R$", "BRL"}, {"A$", "AUD"},
	{"S$", "SGD"}, {"CHF", "CHF"}, {"zł", "PLN"}, {"Kč", "CZK"},
	{"Ft", "HUF"}, {"lei", "RON"}, {"kr", "SEK"},
	{"$", "USD"}, {"€", "EUR"}, {"£", "GBP"}, {"¥", "JPY"},
	{"₹", "INR"}, {"₽", "RUB"}, {"₩", "KRW"}, {"฿", "THB"},
	{"₺", "TRY"}, {"R", "ZAR"},
}

// zeroDecimal are the currencies with no minor unit at all: ¥500 is five
// hundred yen, not five yen. Treating them like dollars would inflate every
// such price by a hundred.
var zeroDecimal = map[string]bool{
	"JPY": true, "KRW": true, "CLP": true, "ISK": true, "VND": true, "HUF": true,
}

func exponent(currency string) int {
	if zeroDecimal[currency] {
		return 0
	}
	return 2
}

func pow10(n int) int64 {
	out := int64(1)
	for i := 0; i < n; i++ {
		out *= 10
	}
	return out
}

// Parse reads a displayed price. It reports false for anything it cannot read
// with confidence, which the callers treat as "price unknown" rather than as
// free - an unreadable price is not a zero one, and the difference decides
// whether the asset shows up under a "free" filter.
func Parse(display string) (Money, bool) {
	s := strings.TrimSpace(display)
	if s == "" {
		return Money{}, false
	}

	currency := ""
	for _, sym := range symbols {
		if strings.Contains(s, sym.marker) {
			currency = sym.currency
			s = strings.Replace(s, sym.marker, "", 1)
			break
		}
	}
	if currency == "" {
		return Money{}, false
	}

	digits := strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) || r == '.' || r == ',' {
			return r
		}
		return -1
	}, s)
	if digits == "" {
		return Money{}, false
	}

	minor, ok := toMinor(digits, exponent(currency))
	if !ok {
		return Money{}, false
	}
	return Money{Minor: minor, Currency: currency, Display: strings.TrimSpace(display)}, true
}

// toMinor converts a bare number, in either decimal convention, to minor units.
//
// The separators are genuinely ambiguous in isolation: "1,50" is one euro fifty
// in most of Europe and one thousand five hundred in the US. The rule used is
// the usual one - whichever separator appears last is the decimal point, unless
// it is followed by three digits, which no decimal fraction of a currency has.
func toMinor(digits string, exp int) (int64, bool) {
	lastDot := strings.LastIndex(digits, ".")
	lastComma := strings.LastIndex(digits, ",")

	sep := -1
	if lastDot > lastComma {
		sep = lastDot
	} else if lastComma > -1 {
		sep = lastComma
	}

	whole, frac := digits, ""
	if sep >= 0 {
		tail := digits[sep+1:]
		if len(tail) == 3 || len(tail) == 0 {
			// A group of exactly three is a thousands separator, not a decimal.
			whole = digits[:sep] + tail
		} else {
			whole, frac = digits[:sep], tail
		}
	}
	whole = strings.NewReplacer(".", "", ",", "").Replace(whole)

	value := int64(0)
	if whole != "" {
		v, ok := atoi(whole)
		if !ok {
			return 0, false
		}
		value = v
	}
	value *= pow10(exp)

	if exp > 0 && frac != "" {
		// Pad or truncate the fraction to the currency's own precision, so
		// "1.5" is 150 cents and "1.999" is 199 rather than either overflowing
		// into the whole part or being dropped.
		for len(frac) < exp {
			frac += "0"
		}
		f, ok := atoi(frac[:exp])
		if !ok {
			return 0, false
		}
		value += f
	}
	return value, true
}

func atoi(s string) (int64, bool) {
	out := int64(0)
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		out = out*10 + int64(r-'0')
		if out > 1<<50 {
			return 0, false // a price this large is a misparse, not a bargain
		}
	}
	return out, true
}
