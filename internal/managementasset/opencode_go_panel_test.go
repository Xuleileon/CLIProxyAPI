package managementasset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchManagementHTMLForOpenCodeGo(t *testing.T) {
	src := []byte("cursor_oauth_polling_error:`Failed to check authentication status:`,plugin_oauth_title:`Plugin`,function ej(){let{t:e}=ss(),[l,u]=(0,y.useState)({fileName:``,location:``,loading:!1}),d=x;upload=()=>{}},N=(n,r=!1)=>{return n};return (0,H.jsx)(jE,{title:(0,H.jsxs)(`span`,{className:FA.cardTitle,children:[(0,H.jsx)(`img`,{src:bx,alt:``,className:FA.cardTitleIcon}),e(`vertex_import.title`)]})}function tj(){")

	out := patchManagementHTMLForOpenCodeGo(src)
	text := string(out)
	for _, want := range []string{
		"saveOpenCodeGo=async",
		"/opencode-go-api-key",
		"type:`password`",
		"auth_login.opencode_go_key_title",
		"opencode_go_key_title:`OpenCode Go API Key`",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("patch missing %q in: %s", want, text)
		}
	}

	second := patchManagementHTMLForOpenCodeGo(out)
	if string(second) != text {
		t.Fatal("second patch changed content")
	}
}

func TestPatchManagementHTMLForOpenCodeGoRequiresOAuthPageAnchors(t *testing.T) {
	src := []byte("<html><body>stock panel without expected bundle anchors</body></html>")
	if got := patchManagementHTMLForOpenCodeGo(src); string(got) != string(src) {
		t.Fatalf("unexpected patch: %s", got)
	}
}

func TestPatchManagementHTMLForOpenCodeGoCurrentBundle(t *testing.T) {
	panelPath := os.Getenv("MANAGEMENT_PANEL_TEST_PATH")
	if panelPath == "" {
		panelPath = filepath.Join("..", "..", "static", "management.html")
	}
	data, errRead := os.ReadFile(panelPath)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			t.Skip("static management panel is not present in this checkout")
		}
		t.Fatalf("read current management panel: %v", errRead)
	}
	patched := patchManagementHTMLForOpenCodeGo(data)
	if !strings.Contains(string(patched), "createOpenCodeGoConfig") {
		for i, patch := range currentOpenCodeGoBundlePatches() {
			t.Logf("current bundle patch %d anchor count = %d, want %d", i, strings.Count(string(data), patch.old), patch.count)
		}
	}
	for _, want := range []string{
		"saveOpenCodeGo=async",
		"/opencode-go-api-key",
		"opencode_go_key_title",
		"openCodeGoKeys",
		"createOpenCodeGoConfig",
		"deleteOpenCodeGoConfig",
		"opencodeGo:{id:`opencodeGo`",
		"case`opencodeGo`",
		"/auth-files/status",
		"providerNames:{gemini:`Gemini`,opencodeGo:`OpenCode Go`",
		"getState().clearCache()",
		"data-opencode-go-account",
		"openCodeGoKeys?.length??0",
	} {
		if !strings.Contains(string(patched), want) {
			t.Fatalf("current panel patch missing %q", want)
		}
	}
	second := patchManagementHTMLForOpenCodeGo(patched)
	if string(second) != string(patched) {
		t.Fatal("second current bundle patch changed content")
	}
	if outputPath := os.Getenv("MANAGEMENT_PANEL_PATCHED_OUTPUT"); outputPath != "" {
		if errWrite := os.WriteFile(outputPath, patched, 0o644); errWrite != nil {
			t.Fatalf("write patched management panel: %v", errWrite)
		}
	}
}
