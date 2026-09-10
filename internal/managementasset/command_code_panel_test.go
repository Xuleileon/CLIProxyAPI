package managementasset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandCodePanelCurrentBundle(t *testing.T) {
	path := os.Getenv("MANAGEMENT_PANEL_TEST_PATH")
	if path == "" {
		path = filepath.Join("..", "..", "static", "management.html")
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skip("local management asset unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	input := string(patchManagementHTMLForOpenCodeGo(data))
	patched, ok := patchManagementHTMLForCommandCode(input)
	if !ok {
		t.Fatal("current management bundle not supported")
	}
	for _, want := range []string{"createCommandCodeConfig", "updateCommandCodeConfig", "deleteCommandCodeConfig", "commandCode:{id:`commandCode`", "commandCodeKeys?.length??0", "https://api.commandcode.ai", "command-code-api-key", "createOpenCodeGoConfig"} {
		if !strings.Contains(patched, want) {
			t.Errorf("missing %s", want)
		}
	}
	second, ok := patchManagementHTMLForCommandCode(patched)
	if !ok || second != patched {
		t.Error("patch not idempotent")
	}
	if output := os.Getenv("COMMAND_CODE_PANEL_OUTPUT"); output != "" {
		if err = os.WriteFile(output, []byte(patched), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCommandCodePanelUnknownBundleUnchanged(t *testing.T) {
	input := "<html>unknown</html>"
	out, ok := patchManagementHTMLForCommandCode(input)
	if ok || out != input {
		t.Fatal("modified unknown bundle")
	}
}
