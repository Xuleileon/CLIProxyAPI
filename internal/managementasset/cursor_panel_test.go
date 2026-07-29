package managementasset

import (
	"strings"
	"testing"
)

func TestPatchManagementHTMLForCursorOAuth(t *testing.T) {
	src := []byte("Vk=[{kind:`builtin`,id:`xai`,titleKey:`auth_login.xai_oauth_title`,icon:{light:$b,dark:ex}}],Hk=new Set(Vk.map(e=>e.id)),Uk=new Set([`codex`,`anthropic`,`antigravity`,`xai`]),bv=new Set([`codex`,`anthropic`,`antigravity`,`qoder`,`xai`]),xai_oauth_polling_error:`Failed to check authentication status:`,plugin_oauth_title:`Plugin`,")

	out := patchManagementHTMLForCursorOAuth(src)
	s := string(out)
	for _, need := range []string{"id:`cursor`", "cursor_oauth_title", "cursor_oauth_button", "`cursor`"} {
		if !strings.Contains(s, need) {
			t.Fatalf("patch missing %q in: %s", need, s)
		}
	}
	// Idempotent
	out2 := patchManagementHTMLForCursorOAuth(out)
	if string(out2) != s {
		t.Fatal("second patch changed content")
	}
}
