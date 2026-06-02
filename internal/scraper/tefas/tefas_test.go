package tefas

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"
)

func TestScrape(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Session seed GET
		if r.Method == "GET" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected content type: %s", r.Header.Get("Content-Type"))
		}
		resp := priceResponse{
			ResultList: []priceItem{
				{FonKodu: "YAC", Tarih: "2024-01-01", Fiyat: 1.23},
				{FonKodu: "YAC", Tarih: "2024-01-02", Fiyat: 1.24},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	jar, _ := cookiejar.New(nil)
	s := New(
		WithWorkers(1),
		WithClient(&http.Client{Jar: jar}),
		WithBaseURL(ts.URL),
	)

	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)

	prices, err := s.Scrape(context.Background(), "YAC", from, to)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(prices) != 2 {
		t.Fatalf("expected 2 prices, got %d", len(prices))
	}

	if prices[0].ClosePrice != 1.23 {
		t.Errorf("expected price 1.23, got %f", prices[0].ClosePrice)
	}
}

func TestScrape_EmptySymbol(t *testing.T) {
	s := New()
	_, err := s.Scrape(context.Background(), "", time.Now(), time.Now())
	if err == nil {
		t.Fatal("expected error for empty symbol")
	}
}

func TestParseDate(t *testing.T) {
	got := parseDate("2024-01-01T00:00:00")
	want := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseDate_Invalid(t *testing.T) {
	got := parseDate("invalid")
	if !got.IsZero() {
		t.Errorf("expected zero time for invalid date, got %v", got)
	}
}
