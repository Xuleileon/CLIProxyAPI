package managementasset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchManagementHTMLForCursorOAuth_LegacyVkFormat(t *testing.T) {
	src := []byte("Vk=[{kind:`builtin`,id:`xai`,titleKey:`auth_login.xai_oauth_title`,icon:{light:$b,dark:ex}}],Hk=new Set(Vk.map(e=>e.id)),Uk=new Set([`codex`,`anthropic`,`antigravity`,`xai`]),bv=new Set([`codex`,`anthropic`,`antigravity`,`qoder`,`xai`]),xai_oauth_polling_error:`Failed to check authentication status:`,plugin_oauth_title:`Plugin`,")

	out := patchManagementHTMLForCursorOAuth(src)
	s := string(out)
	for _, need := range []string{"id:`cursor`", "cursor_oauth_title", "cursor_oauth_button", "`cursor`"} {
		if !strings.Contains(s, need) {
			t.Fatalf("patch missing %q in: %s", need, s)
		}
	}
	if !strings.Contains(s, "bv=new Set([`codex`,`anthropic`,`antigravity`,`qoder`,`xai`,`cursor`])") {
		t.Fatalf("legacy bv set not patched: %s", s)
	}
	out2 := patchManagementHTMLForCursorOAuth(out)
	if string(out2) != s {
		t.Fatal("second patch changed content")
	}
}

func TestPatchManagementHTMLForCursorOAuth_NewLAFormat(t *testing.T) {
	src := []byte("LA=[{kind:`builtin`,id:`xai`,titleKey:`auth_login.xai_oauth_title`,icon:{light:Ex,dark:Dx}}],RA=new Set(LA.map(e=>e.id)),zA=new Set([`codex`,`anthropic`,`antigravity`,`xai`]),Yv=new Set([`codex`,`anthropic`,`antigravity`,`qoder`,`xai`]),xai_oauth_polling_error:`Failed to check authentication status:`,plugin_oauth_title:`Plugin`,")

	out := patchManagementHTMLForCursorOAuth(src)
	s := string(out)
	for _, need := range []string{
		"id:`cursor`",
		"cursor_oauth_title",
		"Yv=new Set([`codex`,`anthropic`,`antigravity`,`qoder`,`xai`,`cursor`])",
		"zA=new Set([`codex`,`anthropic`,`antigravity`,`xai`,`cursor`])",
	} {
		if !strings.Contains(s, need) {
			t.Fatalf("patch missing %q in: %s", need, s)
		}
	}
	out2 := patchManagementHTMLForCursorOAuth(out)
	if string(out2) != s {
		t.Fatal("second patch changed content")
	}
}

func TestPatchManagementHTMLForCursorOAuth_NewDMFormat(t *testing.T) {
	src := []byte("DM=[{kind:`builtin`,id:`xai`,titleKey:`auth_login.xai_oauth_title`,icon:{light:Gx,dark:Kx}}],OM=new Set(DM.map(e=>e.id)),kM=new Set([`codex`,`anthropic`,`antigravity`,`xai`]),xai_oauth_polling_error:`Failed to check authentication status:`,plugin_oauth_title:`Plugin`,")

	out := patchManagementHTMLForCursorOAuth(src)
	s := string(out)
	for _, need := range []string{
		"id:`cursor`",
		"icon:{light:Gx,dark:Kx}",
		"cursor_oauth_title",
		"kM=new Set([`codex`,`anthropic`,`antigravity`,`xai`,`cursor`])",
	} {
		if !strings.Contains(s, need) {
			t.Fatalf("patch missing %q in: %s", need, s)
		}
	}
	out2 := patchManagementHTMLForCursorOAuth(out)
	if string(out2) != s {
		t.Fatal("second patch changed content")
	}
}

func TestPatchStaticManagementHTML(t *testing.T) {
	if os.Getenv("PATCH_STATIC_MANAGEMENT_HTML") != "1" {
		t.Skip("set PATCH_STATIC_MANAGEMENT_HTML=1 to rewrite static/management.html")
	}
	panelPath := filepath.Join("..", "..", "static", "management.html")
	data, errRead := os.ReadFile(panelPath)
	if errRead != nil {
		t.Fatalf("read panel: %v", errRead)
	}
	patched := patchManagementHTMLForCursorOAuth(data)
	if string(patched) == string(data) {
		t.Fatal("panel already contains cursor OAuth or patch anchors missing")
	}
	if errWrite := os.WriteFile(panelPath, patched, 0o644); errWrite != nil {
		t.Fatalf("write panel: %v", errWrite)
	}
}
