package management

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestDoManagementAPICallRetriesAntigravityTransientFailure(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: apiCallRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		if string(body) != `{"project":"test-project"}` {
			t.Fatalf("body = %q", body)
		}
		if attempts == 1 {
			return nil, io.EOF
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	})}
	req, errReq := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary", strings.NewReader(`{"project":"test-project"}`))
	if errReq != nil {
		t.Fatalf("new request: %v", errReq)
	}

	resp, errDo := doManagementAPICall(context.Background(), client, req, true)
	if errDo != nil {
		t.Fatalf("doManagementAPICall: %v", errDo)
	}
	defer resp.Body.Close()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestShouldRetryAntigravityAPICall(t *testing.T) {
	tests := []struct {
		name   string
		method string
		rawURL string
		want   bool
	}{
		{name: "daily quota", method: http.MethodPost, rawURL: "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary", want: true},
		{name: "primary load", method: http.MethodPost, rawURL: "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", want: true},
		{name: "different host", method: http.MethodPost, rawURL: "https://example.com/v1internal:loadCodeAssist", want: false},
		{name: "different operation", method: http.MethodPost, rawURL: "https://cloudcode-pa.googleapis.com/v1internal:onboardUser", want: false},
		{name: "different method", method: http.MethodGet, rawURL: "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parsedURL, errParse := url.Parse(tc.rawURL)
			if errParse != nil {
				t.Fatalf("parse URL: %v", errParse)
			}
			if got := shouldRetryAntigravityAPICall(tc.method, parsedURL); got != tc.want {
				t.Fatalf("shouldRetryAntigravityAPICall() = %v, want %v", got, tc.want)
			}
		})
	}
}

type apiCallRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f apiCallRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
