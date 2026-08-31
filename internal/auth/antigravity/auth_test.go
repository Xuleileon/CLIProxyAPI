package antigravity

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFetchUserInfoRetriesWithOIDCFallback(t *testing.T) {
	attempts := 0
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			if req.URL.String() != UserInfoEndpoint {
				t.Fatalf("first endpoint = %s", req.URL)
			}
			return nil, io.EOF
		}
		if req.URL.String() != userInfoFallbackEndpoint {
			t.Fatalf("fallback endpoint = %s", req.URL)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("Authorization = %q", got)
		}
		return jsonResponse(`{"email":"user@example.com"}`), nil
	})})

	email, err := auth.FetchUserInfo(context.Background(), "access-token")
	if err != nil {
		t.Fatalf("FetchUserInfo error: %v", err)
	}
	if email != "user@example.com" {
		t.Fatalf("email = %q", email)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestFetchUserInfoDoesNotRetryUnauthorized(t *testing.T) {
	attempts := 0
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return jsonResponseStatus(http.StatusUnauthorized, `{"error":"invalid_token"}`), nil
	})})

	_, err := auth.FetchUserInfo(context.Background(), "access-token")
	if err == nil {
		t.Fatal("FetchUserInfo error = nil")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestFetchProjectIDMarksUnprovisionedAccount(t *testing.T) {
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
			return jsonResponse(`{"allowedTiers":[{"id":"standard-tier","isDefault":true}]}`), nil
		case "https://daily-cloudcode-pa.googleapis.com/v1internal:onboardUser":
			return jsonResponse(`{"done":true,"response":{}}`), nil
		default:
			t.Fatalf("unexpected request URL: %s", req.URL.String())
			return nil, nil
		}
	})})

	_, err := auth.FetchProjectID(context.Background(), "access-token")
	if !errors.Is(err, ErrProjectUnavailable) {
		t.Fatalf("error = %v, want ErrProjectUnavailable", err)
	}
}

func TestFetchProjectIDFromLoadCodeAssist(t *testing.T) {
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist" {
			t.Fatalf("unexpected request URL: %s", req.URL.String())
		}
		assertLoadCodeAssistHeaders(t, req)
		assertJSONContains(t, req, `"ideType":"ANTIGRAVITY"`)
		return jsonResponse(`{"cloudaicompanionProject":"cogent-snow-4mnnp"}`), nil
	})})

	projectID, err := auth.FetchProjectID(context.Background(), "access-token")
	if err != nil {
		t.Fatalf("FetchProjectID error: %v", err)
	}
	if projectID != "cogent-snow-4mnnp" {
		t.Fatalf("projectID = %q", projectID)
	}
}

func TestFetchProjectIDFallsBackToDailyOnboardUser(t *testing.T) {
	var sawOnboard bool
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
			assertLoadCodeAssistHeaders(t, req)
			return jsonResponse(`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`), nil
		case "https://daily-cloudcode-pa.googleapis.com/v1internal:onboardUser":
			sawOnboard = true
			assertOnboardUserHeaders(t, req)
			assertJSONContains(t, req, `"tier_id":"free-tier"`)
			assertJSONContains(t, req, `"ide_type":"ANTIGRAVITY"`)
			return jsonResponse(`{
				"done": true,
				"response": {
					"cloudaicompanionProject": {
						"id": "cogent-snow-4mnnp",
						"name": "cogent-snow-4mnnp",
						"projectNumber": "22597072101"
					}
				}
			}`), nil
		default:
			t.Fatalf("unexpected request URL: %s", req.URL.String())
			return nil, nil
		}
	})})

	projectID, err := auth.FetchProjectID(context.Background(), "access-token")
	if err != nil {
		t.Fatalf("FetchProjectID error: %v", err)
	}
	if !sawOnboard {
		t.Fatalf("expected onboardUser fallback")
	}
	if projectID != "cogent-snow-4mnnp" {
		t.Fatalf("projectID = %q", projectID)
	}
}

func TestFetchProjectIDRetriesTransientTransportFailure(t *testing.T) {
	attempts := 0
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, io.EOF
		}
		return jsonResponse(`{"cloudaicompanionProject":"recovered-project"}`), nil
	})})

	projectID, err := auth.FetchProjectID(context.Background(), "access-token")
	if err != nil {
		t.Fatalf("FetchProjectID error: %v", err)
	}
	if projectID != "recovered-project" {
		t.Fatalf("projectID = %q", projectID)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestFetchProjectIDDoesNotRetryPermissionDenied(t *testing.T) {
	attempts := 0
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return jsonResponseStatus(http.StatusForbidden, `{"error":{"status":"PERMISSION_DENIED","details":[{"reason":"TOS_VIOLATION"}]}}`), nil
	})})

	projectID, err := auth.FetchProjectID(context.Background(), "access-token")
	if err == nil {
		t.Fatal("FetchProjectID error = nil")
	}
	if projectID != "" {
		t.Fatalf("projectID = %q, want empty", projectID)
	}
	if !strings.Contains(err.Error(), "TOS_VIOLATION") {
		t.Fatalf("error = %q, want TOS_VIOLATION", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func assertLoadCodeAssistHeaders(t *testing.T, req *http.Request) {
	t.Helper()
	if got := req.Header.Get("Authorization"); got != "Bearer access-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("Accept"); got != "*/*" {
		t.Fatalf("Accept = %q", got)
	}
	if got := req.Header.Get("X-Goog-Api-Client"); got != "" {
		t.Fatalf("X-Goog-Api-Client = %q, want empty", got)
	}
	userAgent := req.Header.Get("User-Agent")
	if !strings.HasPrefix(userAgent, "antigravity/hub/") {
		t.Fatalf("User-Agent = %q", userAgent)
	}
	if strings.Contains(userAgent, "google-api-nodejs-client/") {
		t.Fatalf("User-Agent = %q", userAgent)
	}
}

func assertOnboardUserHeaders(t *testing.T, req *http.Request) {
	t.Helper()
	if got := req.Header.Get("Authorization"); got != "Bearer access-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("Accept"); got != "*/*" {
		t.Fatalf("Accept = %q", got)
	}
	if got := req.Header.Get("X-Goog-Api-Client"); got != "gl-node/22.21.1" {
		t.Fatalf("X-Goog-Api-Client = %q", got)
	}
	userAgent := req.Header.Get("User-Agent")
	if !strings.HasPrefix(userAgent, "antigravity/hub/") {
		t.Fatalf("User-Agent = %q", userAgent)
	}
	if !strings.Contains(userAgent, "google-api-nodejs-client/10.3.0") {
		t.Fatalf("User-Agent = %q", userAgent)
	}
}

func assertJSONContains(t *testing.T, req *http.Request, want string) {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	bodyText := string(body)
	req.Body = io.NopCloser(strings.NewReader(bodyText))
	if !strings.Contains(bodyText, want) {
		t.Fatalf("body missing %s: %s", want, bodyText)
	}
}

func jsonResponse(body string) *http.Response {
	return jsonResponseStatus(http.StatusOK, body)
}

func jsonResponseStatus(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
