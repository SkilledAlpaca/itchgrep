package main

import (
	"io"
	"net/http"
	"time"

	"itchgrep/internal/fetcher"
	"itchgrep/internal/logging"
	"itchgrep/internal/storage"
	"itchgrep/pkg/money"
)

// ratesTimeout bounds the exchange-rate fetch. It is one small file from one
// central bank, and the crawl it is attached to has already taken hours - so
// there is no reason to wait long, and every reason not to let a hung
// connection hold up publishing an otherwise finished index.
const ratesTimeout = 15 * time.Second

// currentRates obtains the exchange-rate snapshot to build the index with, in
// descending order of preference: today's from the ECB, the last one this
// installation successfully fetched, and finally the table baked into the
// binary.
//
// Never fatal. Prices convert approximately or not at all; neither is worth
// discarding a completed crawl over. Whatever is chosen is republished so the
// webserver reads exactly the numbers the index was built with - the stored
// PriceUSD values and the rates shown beside converted prices have to describe
// the same day, or the site would sort by one set of rates and explain itself
// with another.
func currentRates() money.Rates {
	rates, err := fetchECBRates()
	if err == nil {
		logging.Info("Fetched exchange rates for %s from %s", rates.Date, rates.Source)
	} else {
		logging.Warning("Could not fetch exchange rates (%v); falling back", err)
		if stored, storedErr := storage.GetRates(); storedErr == nil {
			logging.Info("Reusing stored exchange rates from %s", stored.Date)
			rates = stored
		} else {
			rates = money.Fallback()
			logging.Warning("Using the built-in exchange rates from %s; converted prices will be further off than usual", rates.Date)
		}
	}

	if err := storage.PutRates(rates); err != nil {
		logging.Warning("Failed to store exchange rates: %v", err)
	}
	return rates
}

func fetchECBRates() (money.Rates, error) {
	client := &http.Client{Timeout: ratesTimeout}
	req, err := http.NewRequest(http.MethodGet, money.ECBSource, nil)
	if err != nil {
		return money.Rates{}, err
	}
	req.Header.Set("User-Agent", fetcher.UserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return money.Rates{}, err
	}
	defer resp.Body.Close()

	// Capped: this is a document of a few kilobytes, and an unbounded read from
	// a host that starts misbehaving would be a way to exhaust memory at the
	// very end of a successful crawl.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return money.Rates{}, err
	}
	return money.ParseECB(body)
}
