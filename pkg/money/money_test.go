package money

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsesThePricesItchActuallyShows(t *testing.T) {
	// These four are copied from real listings; the rest of the table is the
	// formats that follow from itch.io pricing in the seller's own currency.
	cases := map[string]Money{
		"$4.95":     {Minor: 495, Currency: "USD"},
		"9.97€":     {Minor: 997, Currency: "EUR"},
		"$150":      {Minor: 15000, Currency: "USD"},
		"$1":        {Minor: 100, Currency: "USD"},
		"£3.50":     {Minor: 350, Currency: "GBP"},
		"CA$12.00":  {Minor: 1200, Currency: "CAD"},
		"A$7.50":    {Minor: 750, Currency: "AUD"},
		"R$19,90":   {Minor: 1990, Currency: "BRL"},
		"20 zł":     {Minor: 2000, Currency: "PLN"},
		"$1,299.00": {Minor: 129900, Currency: "USD"},
	}
	for display, want := range cases {
		got, ok := Parse(display)
		require.True(t, ok, "should parse %q", display)
		assert.Equal(t, want.Minor, got.Minor, "amount of %q", display)
		assert.Equal(t, want.Currency, got.Currency, "currency of %q", display)
		assert.Equal(t, display, got.Display, "the original must survive verbatim")
	}
}

func TestYenHasNoCents(t *testing.T) {
	// Scaling a zero-decimal currency like a two-decimal one inflates every
	// price by a hundred, which would put every Japanese asset at the expensive
	// end of a cheapest-first sort.
	got, ok := Parse("¥500")
	require.True(t, ok)
	assert.Equal(t, int64(500), got.Minor)
	assert.InDelta(t, 500.0, got.Amount(), 0.001)
}

func TestSeparatorAmbiguityResolvesTheUsualWay(t *testing.T) {
	// "1,50" is one-fifty in Europe; "1,500" is fifteen hundred anywhere. The
	// length of the trailing group is the only thing that distinguishes them.
	half, ok := Parse("€1,50")
	require.True(t, ok)
	assert.Equal(t, int64(150), half.Minor)

	thousand, ok := Parse("€1,500")
	require.True(t, ok)
	assert.Equal(t, int64(150000), thousand.Minor)
}

func TestUnreadablePricesAreNotSilentlyFree(t *testing.T) {
	// The distinction decides whether an asset appears under a "free" filter.
	// Better absent from both halves than wrongly in one.
	for _, s := range []string{"", "   ", "ask me", "???", "100"} {
		_, ok := Parse(s)
		assert.False(t, ok, "%q is not a readable price", s)
	}
}

func TestLongerSymbolsWinOverShorterOnes(t *testing.T) {
	// "CA$12" read as USD would be a 35% error in the wrong direction.
	got, ok := Parse("CA$12.00")
	require.True(t, ok)
	assert.Equal(t, "CAD", got.Currency)
}

func TestConversionRoundTripsApproximately(t *testing.T) {
	r := Fallback()
	usd, ok := Parse("$10.90")
	require.True(t, ok)

	eur, ok := r.Convert(usd, "EUR")
	require.True(t, ok)
	assert.InDelta(t, 10.0, eur.Amount(), 0.05, "10.90 USD at 1.09/EUR is about 10 EUR")

	back, ok := r.Convert(eur, "USD")
	require.True(t, ok)
	assert.InDelta(t, 10.90, back.Amount(), 0.05)
}

func TestConversionRefusesRatherThanGuesses(t *testing.T) {
	r := Fallback()
	_, ok := r.Convert(Money{Minor: 100, Currency: "XYZ"}, "USD")
	assert.False(t, ok, "an unknown source currency has no rate")

	_, ok = r.Convert(Money{Minor: 100, Currency: "USD"}, "XYZ")
	assert.False(t, ok, "an unknown target currency has no rate")
}

func TestUsdIsAvailableForEveryKnownCurrency(t *testing.T) {
	// The price filters and the cheapest-first sort both need one comparable
	// number per asset. A currency in the table with no dollar value would be
	// an asset that silently drops out of both.
	r := Fallback()
	for _, c := range r.Currencies() {
		_, ok := r.USD(Money{Minor: 1000, Currency: c})
		assert.True(t, ok, "no USD conversion for %s", c)
	}
}

func TestFormatMatchesTheConventionsItchUses(t *testing.T) {
	assert.Equal(t, "$4.95", Format(495, "USD"))
	assert.Equal(t, "9.97€", Format(997, "EUR"))
	assert.Equal(t, "¥500", Format(500, "JPY"))
	assert.Equal(t, "20.00 zł", Format(2000, "PLN"))
	assert.Equal(t, "12.34 SGD", Format(1234, "SGD"), "unlisted currencies fall back to the code")
}

const ecbSample = `<?xml version="1.0" encoding="UTF-8"?>
<gesmes:Envelope xmlns:gesmes="http://www.gesmes.org/xml/2002-08-01">
 <Cube>
  <Cube time="2026-08-07">
   <Cube currency="USD" rate="1.0912"/>
   <Cube currency="JPY" rate="164.85"/>
   <Cube currency="GBP" rate="0.84730"/>
  </Cube>
 </Cube>
</gesmes:Envelope>`

func TestEcbSnapshotIsReadWithItsDate(t *testing.T) {
	// The date is the point: a converted figure without one is a claim about
	// today that nothing is keeping true.
	r, err := ParseECB([]byte(ecbSample))
	require.NoError(t, err)

	assert.Equal(t, "2026-08-07", r.Date)
	assert.Equal(t, ECBSource, r.Source)
	assert.InDelta(t, 1.0912, r.PerEUR["USD"], 0.0001)
	assert.InDelta(t, 1.0, r.PerEUR["EUR"], 0.0001, "the base currency is implicit in the file")
	assert.True(t, r.Valid())
}

func TestAGarbledSnapshotIsRejectedNotHalfUsed(t *testing.T) {
	// Half a rates table converts some prices and silently mangles others.
	_, err := ParseECB([]byte(`<Envelope><Cube time="2026-08-07"/></Envelope>`))
	assert.Error(t, err)

	_, err = ParseECB([]byte(`<Envelope><Cube currency="USD" rate="1.09"/></Envelope>`))
	assert.Error(t, err, "rates without a date cannot be presented honestly")

	assert.False(t, Rates{}.Valid())
}
