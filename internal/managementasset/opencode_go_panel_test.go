package managementasset

import (
	"os"
	"os/exec"
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

func TestPatchManagementHTMLForOpenCodeGoSeptemberBundle(t *testing.T) {
	t.Setenv("MANAGEMENT_PANEL_TEST_PATH", filepath.Join("testdata", "opencode_go_september_bundle.txt"))
	t.Setenv("MANAGEMENT_PANEL_PATCHED_OUTPUT", "")
	TestPatchManagementHTMLForOpenCodeGoCurrentBundle(t)
}

func TestOpenCodeGoSeptemberProviderPatchIsAtomic(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "opencode_go_september_bundle.txt"))
	if err != nil {
		t.Fatal(err)
	}
	input := strings.ReplaceAll(string(data), "NE=[`kimi`,`gemini`", "NE=[`changed`,`gemini`")
	got, ok := patchManagementHTMLForOpenCodeGoProvider(input)
	if ok || got != input {
		t.Fatal("unsupported provider bundle must remain unchanged")
	}
}

// Execute the injected handler to catch scope collisions in minified bundles.
func TestOpenCodeGoSeptemberSaveHandler(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required for the injected JavaScript behavioral check")
	}
	data, err := os.ReadFile(filepath.Join("testdata", "opencode_go_september_bundle.txt"))
	if err != nil {
		t.Fatal(err)
	}
	patched := patchSeptemberOpenCodeGoOAuth(string(data))
	start := strings.Index(patched, "saveOpenCodeGo=async")
	if start < 0 {
		t.Fatal("missing save handler")
	}
	end := strings.Index(patched[start:], "},P=(r,i=!1)=>{")
	if end < 0 {
		t.Fatal("missing handler boundary")
	}
	script := `const assert = require('node:assert/strict');
let entries = [{'api-key':'existing',name:'Keep', 'auth-index':'runtime-only'}], writes=0, clears=0, fail=false;
let notices=[], openCodeGoState={apiKey:'new-key'}, saveOpenCodeGo;
const e=x=>x, i=(message,type)=>notices.push({message,type}), Dc=e=>e.message;
const setOpenCodeGoState=f=>{openCodeGoState=f(openCodeGoState)};
const Xp={getState:()=>({clearCache:()=>clears++})};
const Tp={get:async()=>({'opencode-go-api-key':entries}),put:async(path,value)=>{if(fail)throw Error('test failure'); entries=value;writes++}};
` + patched[start:start+end+1] + `;
(async()=>{
 await saveOpenCodeGo();
 assert.equal(openCodeGoState.status,'success');
 assert.equal(notices.at(-1).type,'success');
 assert.equal(entries.length,2); assert.equal(entries[0].name,'Keep');
 assert.equal(entries[0]['auth-index'],undefined); assert.equal(clears,1);
 openCodeGoState.apiKey='new-key'; await saveOpenCodeGo(); assert.equal(entries.length,2);
 openCodeGoState.apiKey=''; await saveOpenCodeGo(); assert.equal(notices.at(-1).type,'warning'); assert.equal(writes,2);
 fail=true; openCodeGoState.apiKey='failed-key'; await saveOpenCodeGo();
 assert.equal(openCodeGoState.status,'error'); assert.equal(openCodeGoState.error,'test failure');
 assert.equal(notices.at(-1).type,'error'); assert.equal(writes,2);
})().catch(err=>{console.error(err);process.exitCode=1});`
	cmd := exec.Command(node)
	cmd.Stdin = strings.NewReader(script)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("save handler: %v\n%s", err, output)
	}
}
