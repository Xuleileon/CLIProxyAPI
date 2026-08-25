package helps

import (
	"bytes"
	"encoding/gob"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCursorCheckpointStoreRoundTripAndIsolation(t *testing.T) {
	store := newTestCursorCheckpointStore(t, time.Hour)
	snapshot := CursorCheckpointSnapshot{
		Data:      []byte("checkpoint"),
		Blobs:     map[string][]byte{"blob": []byte("image")},
		Pending:   []CursorCheckpointPendingTool{{ToolCallID: "tool-1", ToolName: "read"}},
		UpdatedAt: time.Now(),
	}
	if err := store.Save("conversation-a", "auth-a", snapshot); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	raw, err := os.ReadFile(store.snapshotPath("conversation-a", "auth-a"))
	if err != nil {
		t.Fatalf("read encrypted checkpoint: %v", err)
	}
	if bytes.Contains(raw, []byte("checkpoint")) || bytes.Contains(raw, []byte("image")) {
		t.Fatal("checkpoint store contains plaintext conversation data")
	}

	loaded, ok, err := store.Load("conversation-a", "auth-a")
	if err != nil || !ok {
		t.Fatalf("Load() = ok %t, error %v", ok, err)
	}
	if !bytes.Equal(loaded.Data, snapshot.Data) || !bytes.Equal(loaded.Blobs["blob"], snapshot.Blobs["blob"]) {
		t.Fatalf("loaded snapshot = %#v", loaded)
	}
	if len(loaded.Pending) != 1 || loaded.Pending[0] != snapshot.Pending[0] {
		t.Fatalf("loaded pending lineage = %#v", loaded.Pending)
	}
	loaded.Data[0] = 'X'
	loaded.Blobs["blob"][0] = 'X'
	reloaded, ok, err := store.Load("conversation-a", "auth-a")
	if err != nil || !ok || string(reloaded.Data) != "checkpoint" || string(reloaded.Blobs["blob"]) != "image" {
		t.Fatalf("stored data was aliased: ok=%t err=%v snapshot=%#v", ok, err, reloaded)
	}
	if _, ok, err = store.Load("conversation-a", "auth-b"); err != nil || ok {
		t.Fatalf("cross-auth Load() = ok %t, error %v", ok, err)
	}
}

func TestCursorCheckpointStoreFallsBackToBackup(t *testing.T) {
	store := newTestCursorCheckpointStore(t, time.Hour)
	snapshot := CursorCheckpointSnapshot{Data: []byte("checkpoint"), UpdatedAt: time.Now()}
	if err := store.Save("conversation", "auth", snapshot); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	path := store.snapshotPath("conversation", "auth")
	if err := os.Rename(path, path+".bak"); err != nil {
		t.Fatalf("create backup fixture: %v", err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil {
		t.Fatalf("write corrupt primary: %v", err)
	}
	loaded, ok, err := store.Load("conversation", "auth")
	if err != nil || !ok || string(loaded.Data) != "checkpoint" {
		t.Fatalf("backup Load() = ok %t, error %v, data %q", ok, err, loaded.Data)
	}
}

func TestCursorCheckpointStoreExpiresAndCleansFiles(t *testing.T) {
	dir := t.TempDir()
	store := newTestCursorCheckpointStoreAt(t, dir, time.Minute)
	old := time.Now().Add(-2 * time.Minute)
	if err := store.Save("conversation", "auth", CursorCheckpointSnapshot{Data: []byte("old"), UpdatedAt: old}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	path := store.snapshotPath("conversation", "auth")
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	if _, ok, err := store.Load("conversation", "auth"); err != nil || ok {
		t.Fatalf("expired Load() = ok %t, error %v", ok, err)
	}
	if err := store.Cleanup(time.Now()); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, cursorCheckpointStoreDir, filepath.Base(path))); !os.IsNotExist(err) {
		t.Fatalf("expired checkpoint still exists: %v", err)
	}
}

func TestCursorCheckpointStoreDeleteIsScoped(t *testing.T) {
	store := newTestCursorCheckpointStore(t, time.Hour)
	for _, conversation := range []string{"conversation-a", "conversation-b"} {
		if err := store.Save(conversation, "auth", CursorCheckpointSnapshot{Data: []byte(conversation), UpdatedAt: time.Now()}); err != nil {
			t.Fatalf("Save(%s) error = %v", conversation, err)
		}
	}
	if err := store.Delete("conversation-a", "auth"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, ok, err := store.Load("conversation-a", "auth"); err != nil || ok {
		t.Fatalf("deleted Load() = ok %t, error %v", ok, err)
	}
	if loaded, ok, err := store.Load("conversation-b", "auth"); err != nil || !ok || string(loaded.Data) != "conversation-b" {
		t.Fatalf("unrelated Load() = ok %t, error %v, data %q", ok, err, loaded.Data)
	}
}

func TestCursorCheckpointStoreRejectsOutOfOrderSnapshot(t *testing.T) {
	store := newTestCursorCheckpointStore(t, time.Hour)
	now := time.Now()
	if err := store.Save("conversation", "auth", CursorCheckpointSnapshot{Data: []byte("new"), Blobs: map[string][]byte{"late": []byte("blob")}, UpdatedAt: now}); err != nil {
		t.Fatalf("Save(new) error = %v", err)
	}
	if err := store.Save("conversation", "auth", CursorCheckpointSnapshot{Data: []byte("old"), UpdatedAt: now.Add(-time.Second)}); err != nil {
		t.Fatalf("Save(old) error = %v", err)
	}
	loaded, ok, err := store.Load("conversation", "auth")
	if err != nil || !ok || string(loaded.Data) != "new" || string(loaded.Blobs["late"]) != "blob" {
		t.Fatalf("out-of-order snapshot won: ok=%t err=%v snapshot=%#v", ok, err, loaded)
	}
}

func TestCursorCheckpointStoreRejectsTampering(t *testing.T) {
	store := newTestCursorCheckpointStore(t, time.Hour)
	if err := store.Save("conversation", "auth", CursorCheckpointSnapshot{Data: []byte("checkpoint"), UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	path := store.snapshotPath("conversation", "auth")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	raw[len(raw)-1] ^= 0xff
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("tamper checkpoint: %v", err)
	}
	if _, ok, err := store.Load("conversation", "auth"); err == nil || ok {
		t.Fatalf("tampered Load() = ok %t, error %v", ok, err)
	}
}

func TestCursorCheckpointStoreRejectsAmbiguousVersionOne(t *testing.T) {
	store := newTestCursorCheckpointStore(t, time.Hour)
	var plaintext bytes.Buffer
	if err := gob.NewEncoder(&plaintext).Encode(&cursorCheckpointDiskSnapshot{
		Version:       1,
		UpdatedAtUnix: time.Now().UnixNano(),
		Data:          []byte("legacy-without-pending-state"),
	}); err != nil {
		t.Fatalf("encode legacy snapshot: %v", err)
	}
	key, err := store.encryptionKey()
	if err != nil {
		t.Fatalf("encryptionKey() error = %v", err)
	}
	path := store.snapshotPath("conversation", "auth")
	sealed, err := sealCursorCheckpoint(key, filepath.Base(path), plaintext.Bytes())
	if err != nil {
		t.Fatalf("seal legacy snapshot: %v", err)
	}
	if err = os.WriteFile(path, append([]byte(cursorCheckpointStoreMagic), sealed...), 0o600); err != nil {
		t.Fatalf("write legacy snapshot: %v", err)
	}
	if _, ok, err := store.Load("conversation", "auth"); err != nil || ok {
		t.Fatalf("legacy Load() = ok %t, error %v", ok, err)
	}
}

func newTestCursorCheckpointStore(t *testing.T, ttl time.Duration) *CursorCheckpointStore {
	t.Helper()
	return newTestCursorCheckpointStoreAt(t, t.TempDir(), ttl)
}

func newTestCursorCheckpointStoreAt(t *testing.T, authDir string, ttl time.Duration) *CursorCheckpointStore {
	t.Helper()
	store := NewCursorCheckpointStore(authDir, ttl)
	if store.root == "" {
		t.Skip("OS-protected checkpoint persistence is unavailable")
	}
	return store
}
