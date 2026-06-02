// Package tefas scrapes fund price data from tefas.gov.tr.
//
// The new TEFAS API (2026) requires:
//   - A session cookie obtained by a GET to the root URL first
//   - JSON POST to /api/funds/fonFiyatBilgiGetir
//   - Fixed periyod parameter: 1|3|6|12|36|60 (months)
//
// Response uses "tarih"/"fiyat" field names.
package tefas

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ahmethakanbesel/finance-api/internal/scraper"
)

const (
	defaultBaseURL    = "https://www.tefas.gov.tr"
	priceEndpointPath = "/api/funds/fonFiyatBilgiGetir"
	defaultUserAgent  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

// validPeriods are the only periyod values accepted by the TEFAS API.
var validPeriods = []int{1, 3, 6, 12, 36, 60}

func monthsBack(from time.Time) int {
	days := int(time.Since(from).Hours() / 24)
	needed := days/30 + 2
	for _, p := range validPeriods {
		if p >= needed {
			return p
		}
	}
	return 60
}

// priceItem matches the new TEFAS API response fields.
type priceItem struct {
	FonKodu  string  `json:"fonKodu"`
	Tarih    string  `json:"tarih"`  // "2026-05-01"
	Fiyat    float64 `json:"fiyat"`
}

type priceResponse struct {
	ResultList []priceItem `json:"resultList"`
}

type Scraper struct {
	workers int
	client  *http.Client
	baseURL string
	seeded  bool // true once session cookie has been obtained
}

func New(opts ...Option) *Scraper {
	jar, _ := cookiejar.New(nil)
	s := &Scraper{
		workers: 5,
		client: &http.Client{
			Jar:     jar,
			Timeout: 30 * time.Second,
		},
		baseURL: defaultBaseURL,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

type Option func(*Scraper)

func WithWorkers(n int) Option        { return func(s *Scraper) { s.workers = n } }
func WithClient(c *http.Client) Option { return func(s *Scraper) { s.client = c } }
func WithBaseURL(u string) Option      { return func(s *Scraper) { s.baseURL = u } }

// Legacy options — no-ops, kept for backwards compatibility.
func WithHistoryEndpoint(_ string) Option { return func(_ *Scraper) {} }
func WithReferer(_ string) Option         { return func(_ *Scraper) {} }

func (s *Scraper) Source() string                    { return "tefas" }
func (s *Scraper) NativeCurrency(_ string) string    { return "TRY" }

// seedSession visits the TEFAS homepage once to obtain the required session cookies.
func (s *Scraper) seedSession(ctx context.Context) error {
	if s.seeded {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", s.baseURL+"/", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	s.seeded = true
	slog.Debug("tefas session seeded")
	return nil
}

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

	if err := s.seedSession(ctx); err != nil {
		slog.Warn("tefas: session seed failed, continuing anyway", "error", err)
	}

	period := monthsBack(from)
	items, err := s.fetchPrices(ctx, symbol, period)
	if err != nil {
		return nil, err
	}

	var prices []scraper.ScrapedPrice
	for _, item := range items {
		t := parseDate(item.Tarih)
		if t.IsZero() || item.Fiyat <= 0 {
			continue
		}
		if t.Before(from.Truncate(24*time.Hour)) || t.After(to.Add(24*time.Hour)) {
			continue
		}
		prices = append(prices, scraper.ScrapedPrice{
			Date:       t,
			ClosePrice: item.Fiyat,
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

	req, err := http.NewRequestWithContext(ctx, "POST", s.baseURL+priceEndpointPath, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Origin", s.baseURL)
	req.Header.Set("Referer", s.baseURL+"/TarihselVeriler.aspx")

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

// keep errgroup import used in other scraper packages
var _ = errgroup.Group{}

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
