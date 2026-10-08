package executor

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// xaiVersionAtLeast compares dotted numeric version strings.
func xaiVersionAtLeast(got, floor string) bool {
	gotParts := strings.Split(got, ".")
	floorParts := strings.Split(floor, ".")
	for i := 0; i < len(gotParts) || i < len(floorParts); i++ {
		var g, f int
		if i < len(gotParts) {
			g, _ = strconv.Atoi(gotParts[i])
		}
		if i < len(floorParts) {
			f, _ = strconv.Atoi(floorParts[i])
		}
		if g != f {
			return g > f
		}
	}
	return true
}

func TestXAIChatProxyClientVersionMeetsServerFloor(t *testing.T) {
	// cli-chat-proxy.grok.com started rejecting client versions older than
	// 1.0.13 with HTTP 426 ("Your Grok CLI version is outdated"); see #6249.
	const serverFloor = "1.0.13"
	if !xaiVersionAtLeast(xaiClientVersion(), serverFloor) {
		t.Fatalf("xaiClientVersion() = %q, cli-chat-proxy rejects clients older than %s with 426", xaiClientVersion(), serverFloor)
	}
}

func TestXAIChatProxyIdentityHeadersDeriveFromOneConstant(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"auth_kind": "oauth"},
	}
	req, err := http.NewRequest(http.MethodPost, xaiChatBaseURL(auth)+"/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	applyXAIChatHeaders(req, auth, "token", false, "")

	if got := req.Header.Get(xaiClientVersionHeader); got != xaiClientVersion() {
		t.Fatalf("%s = %q, want %q", xaiClientVersionHeader, got, xaiClientVersion())
	}
	if got, want := req.Header.Get("User-Agent"), "xai-grok-workspace/"+xaiClientVersion(); got != want {
		t.Fatalf("User-Agent = %q, want %q (must derive from xaiClientVersion())", got, want)
	}
}

func TestXAIChatProxyCustomHeadersOverridePinnedVersion(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind":                    "oauth",
			"header:x-grok-client-version": "9.9.9",
		},
	}
	req, err := http.NewRequest(http.MethodPost, xaiChatBaseURL(auth)+"/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	applyXAIChatHeaders(req, auth, "token", false, "")

	if got := req.Header.Get(xaiClientVersionHeader); got != "9.9.9" {
		t.Fatalf("%s = %q, want the per-auth custom header to override the pin", xaiClientVersionHeader, got)
	}
}

func TestXAIChatProxyIdentityHeadersFollowDynamicVersion(t *testing.T) {
	restore := helps.SetXAIClientVersionForTest("1.2.34")
	defer restore()

	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"auth_kind": "oauth"},
	}
	req, err := http.NewRequest(http.MethodPost, xaiChatBaseURL(auth)+"/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	applyXAIChatHeaders(req, auth, "token", false, "")

	if got := req.Header.Get(xaiClientVersionHeader); got != "1.2.34" {
		t.Fatalf("%s = %q, want dynamic version 1.2.34", xaiClientVersionHeader, got)
	}
	if got, want := req.Header.Get("User-Agent"), "xai-grok-workspace/1.2.34"; got != want {
		t.Fatalf("User-Agent = %q, want %q", got, want)
	}
}
