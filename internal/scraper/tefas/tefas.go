// Package tefas scrapes fund price data from tefas.gov.tr.
//
// TEFAS retired the legacy /api/DB/BindHistoryInfo endpoint in 2026.
// The new API is /api/funds/fonFiyatBilgiGetir — JSON POST, per-fund,
// with a fixed periyod (months-back) parameter: 1|3|6|12|36|60.
package tefas

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ahmethakanbesel/finance-api/internal/scraper"
)

const (
	defaultBaseURL      = "https://www.tefas.gov.tr"
	priceEndpointPath   = "/api/funds/fonFiyatBilgiGetir"
	defaultUserAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
)

// validPeriods are the only periyod values accepted by the new TEFAS API.
var validPeriods = []int{1, 3, 6, 12, 36, 60}

// monthsBack snaps the requested lookback (from → today) to the smallest
// valid period that covers the range. Returns 60 for anything older.
func monthsBack(from time.Time) int {
	days := int(time.Since(from).Hours() / 24)
	needed := days/30 + 2 // +2 for safety margin
	for _, p := range validPeriods {
		if p >= needed {
			return p
		}
	}
	return 60
}

// New TEFAS API response structures

type priceItem struct {
	FonKodu   string  `json:"fonKodu"`
	Tarih     string  `json:"tarih"`   // "2026-05-01T00:00:00"
	BirimPay  float64 `json:"birimPay"`
}

type priceResponse struct {
	ResultList []priceItem `json:"resultList"`
}

type Scraper struct {
	workers int
	client  *http.Client
	baseURL string
}

func New(opts ...Option) *Scraper {
	s := &Scraper{
		workers: 5,
		client:  http.DefaultClient,
		baseURL: defaultBaseURL,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

type Option func(*Scraper)

func WithWorkers(n int) Option   { return func(s *Scraper) { s.workers = n } }
func WithClient(c *http.Client) Option { return func(s *Scraper) { s.client = c } }
func WithBaseURL(u string) Option { return func(s *Scraper) { s.baseURL = u } }

// Legacy options — kept for backwards compatibility but no-ops with new API.
func WithHistoryEndpoint(_ string) Option { return func(_ *Scraper) {} }
func WithReferer(_ string) Option         { return func(_ *Scraper) {} }

func (s *Scraper) Source() string              { return "tefas" }
func (s *Scraper) NativeCurrency(_ string) string { return "TRY" }

func (s *Scraper) Scrape(ctx context.Context, symbol string, from, to time.Time) ([]scraper.ScrapedPrice, error) {
	if symbol == "" {
		return nil, fmt.Errorf("symbol cannot be empty")
	}
	if from.IsZero() {
		return nil, fmt.Errorf("start date cannot be empty")
	}
	if to.IsZero() {
		to = time.Now()
	}

	period := monthsBack(from)

	items, err := s.fetchPrices(ctx, symbol, period)
	if err != nil {
		return nil, err
	}

	var prices []scraper.ScrapedPrice
	for _, item := range items {
		t := parseDate(item.Tarih)
		if t.IsZero() || item.BirimPay <= 0 {
			continue
		}
		// Filter to requested [from, to] range (API returns full period).
		if t.Before(from.Truncate(24*time.Hour)) || t.After(to.Add(24*time.Hour)) {
			continue
		}
		prices = append(prices, scraper.ScrapedPrice{
			Date:       t,
			ClosePrice: item.BirimPay,
		})
	}

	slog.Info("retrieved tefas data", "fund", symbol, "period_months", period, "records", len(prices))
	return prices, nil
}

func (s *Scraper) fetchPrices(ctx context.Context, fundCode string, periodMonths int) ([]priceItem, error) {
	payload, err := json.Marshal(map[string]interface{}{
		"fonKodu": fundCode,
		"dil":     "TR",
		"periyod": periodMonths,
	})
	if err != nil {
		return nil, err
	}

	url := s.baseURL + priceEndpointPath
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", defaultUserAgent)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tefas: HTTP %d: %s", resp.StatusCode, string(body[:min(200, len(body))]))
	}

	var pr priceResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("tefas: unmarshal: %w", err)
	}
	return pr.ResultList, nil
}

// Worker pool helper used by Scrape for future parallelism (single chunk now).
var _ = errgroup.Group{} // keep import

// parseDate handles TEFAS ISO-like date strings: "2026-05-01T00:00:00".
func parseDate(s string) time.Time {
	if len(s) >= 10 {
		t, err := time.Parse("2006-01-02", s[:10])
		if err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
