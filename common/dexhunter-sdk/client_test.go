package dexhunter

import (
	"net/http"
	"testing"
)

func TestNew(t *testing.T) {
	c := New("test-partner-id")
	if c.base != DefaultBase {
		t.Errorf("base = %q, want %q", c.base, DefaultBase)
	}
	if c.chartsBase != DefaultChartsBase {
		t.Errorf("chartsBase = %q, want %q", c.chartsBase, DefaultChartsBase)
	}
	if c.partnerID != "test-partner-id" {
		t.Errorf("partnerID = %q", c.partnerID)
	}
	if c.http == nil {
		t.Fatal("http client should not be nil")
	}
}

func TestWithBase(t *testing.T) {
	c := New("key").WithBase("https://staging.example.com")
	if c.base != "https://staging.example.com" {
		t.Errorf("base = %q", c.base)
	}
}

func TestWithChartsBase(t *testing.T) {
	c := New("key").WithChartsBase("https://charts.example.com")
	if c.chartsBase != "https://charts.example.com" {
		t.Errorf("chartsBase = %q", c.chartsBase)
	}
}

func TestWithHTTPClient(t *testing.T) {
	custom := &http.Client{}
	c := New("key").WithHTTPClient(custom)
	if c.http != custom {
		t.Error("http client should be the custom one")
	}
}

func TestChaining(t *testing.T) {
	c := New("key").
		WithBase("https://a.example.com").
		WithChartsBase("https://b.example.com").
		WithHTTPClient(&http.Client{})
	if c.base != "https://a.example.com" {
		t.Errorf("base = %q", c.base)
	}
	if c.chartsBase != "https://b.example.com" {
		t.Errorf("chartsBase = %q", c.chartsBase)
	}
}

func TestAPIError(t *testing.T) {
	err := &APIError{Status: 429, Body: "rate limited", URL: "https://api.example.com/swap"}
	msg := err.Error()
	if msg == "" {
		t.Fatal("error message should not be empty")
	}
	// Should contain the URL, status and body
	for _, want := range []string{"429", "rate limited", "swap"} {
		found := false
		for i := 0; i+len(want) <= len(msg); i++ {
			if msg[i:i+len(want)] == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("error message %q should contain %q", msg, want)
		}
	}
}
