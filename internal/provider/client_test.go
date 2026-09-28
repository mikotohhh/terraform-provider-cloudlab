package provider

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestNewClientTunesConnectionPool(t *testing.T) {
	t.Parallel()
	client := NewClient("https://example.test", "token")
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.httpClient.Transport)
	}
	if transport.MaxIdleConnsPerHost != 16 || transport.MaxConnsPerHost != 16 {
		t.Fatalf(
			"connection limits = idle %d/max %d, want 16/16",
			transport.MaxIdleConnsPerHost,
			transport.MaxConnsPerHost,
		)
	}
}

func TestJitteredRetryDelayStaysInRange(t *testing.T) {
	t.Parallel()
	base := 750 * time.Millisecond
	for range 20 {
		got := jitteredRetryDelay(base)
		if got < base || got > base+retryJitterMax {
			t.Fatalf("jittered delay = %v, want [%v, %v]", got, base, base+retryJitterMax)
		}
	}
}

func TestIsRetryableClassifiesPortalErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"429", &APIError{StatusCode: http.StatusTooManyRequests}, true},
		{"401 session race", &APIError{StatusCode: http.StatusUnauthorized, Message: `{"detail":"No such user: 1293524"}`}, true},
		{"401 bad token", &APIError{StatusCode: http.StatusUnauthorized, Message: "invalid token"}, false},
		{"400", &APIError{StatusCode: http.StatusBadRequest, Message: "Must supply something to reserve"}, false},
		{"plain error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		if got := isRetryable(tc.err); got != tc.want {
			t.Errorf("%s: isRetryable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsSearchRetryableAddsOnlyServerErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"500", &APIError{StatusCode: http.StatusInternalServerError}, true},
		{"503", &APIError{StatusCode: http.StatusServiceUnavailable}, true},
		{"406 no fit", &APIError{StatusCode: http.StatusNotAcceptable}, false},
		{"401 session race", &APIError{StatusCode: http.StatusUnauthorized, Message: "No such user"}, true},
		{"plain error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		if got := isSearchRetryable(tc.err); got != tc.want {
			t.Errorf("%s: isSearchRetryable = %v, want %v", tc.name, got, tc.want)
		}
	}

	serverError := &APIError{StatusCode: http.StatusInternalServerError}
	if isRetryable(serverError) {
		t.Fatal("generic request policy must not retry HTTP 500; only read-only searches may do so")
	}
}
