package helps

import (
	"strings"
	"testing"
)

func TestPrefixCursorACPName(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"Read", "Write", "Bash", "Agent", "WebSearch", "Workflow"} {
		got := PrefixCursorACPName(name)
		want := CursorACPNamePrefix + name
		if got != want {
			t.Fatalf("PrefixCursorACPName(%q) = %q, want %q", name, got, want)
		}
		if restored := UnprefixCursorACPName(got); restored != name {
			t.Fatalf("UnprefixCursorACPName(%q) = %q, want %q", got, restored, name)
		}
	}
}

func TestPrefixCursorACPNamePrefixesClaudeCodeTools(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"Task", "ToolSearch", "TaskOutput", "SendMessage", "mcp__context7__query-docs"} {
		got := PrefixCursorACPName(name)
		want := CursorACPNamePrefix + name
		if got != want {
			t.Fatalf("PrefixCursorACPName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestPrefixCursorACPNamePreservesClaudeMCPNames(t *testing.T) {
	t.Parallel()

	name := "mcp__context7__query-docs"
	got := PrefixCursorACPName(name)
	want := CursorACPNamePrefix + name
	if got != want {
		t.Fatalf("PrefixCursorACPName(%q) = %q, want %q", name, got, want)
	}
	if restored := UnprefixCursorACPName(got); restored != name {
		t.Fatalf("UnprefixCursorACPName(%q) = %q, want %q", got, restored, name)
	}
	if restored := UnprefixCursorACPName(name); restored != name {
		t.Fatalf("UnprefixCursorACPName(%q) = %q, want unchanged", name, restored)
	}
}

func TestAnnotateCursorACPDescription(t *testing.T) {
	t.Parallel()

	got := AnnotateCursorACPDescription("Grep", "search file contents")
	if got != "[Grep] search file contents" {
		t.Fatalf("description = %q, want short original-name tag", got)
	}
	if again := AnnotateCursorACPDescription("Grep", got); again != got {
		t.Fatalf("tagged twice: %q", again)
	}
}

func TestCursorACPAliasInstruction(t *testing.T) {
	t.Parallel()

	got := CursorACPAliasInstruction([]string{"Task", "Bash"})
	if !strings.Contains(got, "acp_Grep") || !strings.Contains(got, "acp_Task") {
		t.Fatalf("instruction = %q, want Grep/Task examples", got)
	}
	if strings.Contains(got, "Bash →") {
		t.Fatalf("instruction listed per-tool mappings: %s", got)
	}
	many := make([]string, 80)
	for i := range many {
		many[i] = "Tool" + strings.Repeat("X", i%3)
	}
	if gotMany := CursorACPAliasInstruction(many); gotMany != got {
		t.Fatalf("instruction grew with tool count: %d vs %d bytes", len(gotMany), len(got))
	}
	if CursorACPAliasInstruction(nil) != "" {
		t.Fatal("empty tool list should not inject an instruction")
	}
}

func TestPrefixCursorACPNameEscapesExistingPrefix(t *testing.T) {
	t.Parallel()

	original := CursorACPNamePrefix + "Read"
	prefixed := PrefixCursorACPName(original)
	want := CursorACPNamePrefix + original
	if prefixed != want {
		t.Fatalf("PrefixCursorACPName(%q) = %q, want %q", original, prefixed, want)
	}
	if restored := UnprefixCursorACPName(prefixed); restored != original {
		t.Fatalf("UnprefixCursorACPName(%q) = %q, want %q", prefixed, restored, original)
	}
}

func TestCursorACPNameEquals(t *testing.T) {
	t.Parallel()

	upstream := PrefixCursorACPName("Bash")
	if !CursorACPNameEquals(upstream, "Bash") {
		t.Fatalf("CursorACPNameEquals(%q, Bash) = false", upstream)
	}
	if !CursorACPNameEquals("Bash", "Bash") {
		t.Fatal("unprefixed Bash should still match Bash")
	}
	if CursorACPNameEquals(upstream, "Read") {
		t.Fatalf("CursorACPNameEquals(%q, Read) = true", upstream)
	}
}
