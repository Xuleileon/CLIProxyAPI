package proto

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestEncodeExecShellStreamResultUsesNativeStreamLifecycle(t *testing.T) {
	messages := EncodeExecShellStreamResult(12, "exec-shell", `D:\workspace`, "ok\n", false)
	if len(messages) != 3 {
		t.Fatalf("shell stream messages = %d, want 3", len(messages))
	}

	wantStreamFields := []protowire.Number{4, 1, 3}
	for index, raw := range messages {
		exec := mustFindBytesField(t, raw, ACM_ExecClientMessage)
		stream := mustFindBytesField(t, exec, ECM_ShellStream)
		mustFindBytesField(t, stream, wantStreamFields[index])
	}
}

func TestEncodeExecNativeResultsUseExpectedOneofs(t *testing.T) {
	tests := []struct {
		name  string
		raw   []byte
		field protowire.Number
	}{
		{name: "shell", raw: EncodeExecShellResult(1, "exec", "pwd", "", "ok", false), field: ECM_ShellResult},
		{name: "read error", raw: EncodeExecReadResult(2, "exec", "README.md", "missing", true), field: ECM_ReadResult},
		{name: "grep files", raw: EncodeExecGrepResult(3, "exec", "", "", "files_with_matches", "README.md\n", false), field: ECM_GrepResult},
		{name: "ls", raw: EncodeExecLsResult(4, "exec", ".", "main.go\n", false), field: ECM_LsResult},
		{name: "fetch", raw: EncodeExecFetchResult(5, "exec", "https://example.com", "body", false), field: ECM_FetchResult},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exec := mustFindBytesField(t, test.raw, ACM_ExecClientMessage)
			mustFindBytesField(t, exec, test.field)
		})
	}
}
