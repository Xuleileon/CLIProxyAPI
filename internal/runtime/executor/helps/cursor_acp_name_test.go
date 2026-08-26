package helps

import "testing"

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

func TestPrefixCursorACPNameDoesNotDoublePrefix(t *testing.T) {
	t.Parallel()

	prefixed := PrefixCursorACPName("Read")
	if got := PrefixCursorACPName(prefixed); got != prefixed {
		t.Fatalf("PrefixCursorACPName(%q) = %q", prefixed, got)
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
