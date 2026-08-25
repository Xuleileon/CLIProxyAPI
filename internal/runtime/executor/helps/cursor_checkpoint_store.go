package helps

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	cursorCheckpointStoreDir     = ".cursor-checkpoints"
	cursorCheckpointStoreMagic   = "CPACKPT2"
	cursorCheckpointStoreKeyFile = "store.key"
	cursorCheckpointStoreMaxSize = 256 << 20
	cursorCheckpointLockShards   = 64
)

// CursorCheckpointSnapshot is the opaque Cursor conversation state and the KV
// blobs referenced by it. The payload must not be decoded or rewritten locally.
type CursorCheckpointSnapshot struct {
	Data      []byte
	Blobs     map[string][]byte
	Pending   []CursorCheckpointPendingTool
	UpdatedAt time.Time
}

// CursorCheckpointPendingTool is the minimum non-sensitive lineage needed to
// tell whether an opaque checkpoint still waits for external tool results.
type CursorCheckpointPendingTool struct {
	ToolCallID string
	ToolName   string
}

type cursorCheckpointDiskSnapshot struct {
	Version       uint8
	UpdatedAtUnix int64
	Data          []byte
	Blobs         map[string][]byte
	Pending       []CursorCheckpointPendingTool
}

// CursorCheckpointStore keeps the latest opaque checkpoint across proxy
// restarts. Files are scoped by a hash of auth and conversation identity and
// are retained only for the configured checkpoint TTL.
type CursorCheckpointStore struct {
	root    string
	ttl     time.Duration
	mu      sync.Mutex // protects latest metadata only
	locks   [cursorCheckpointLockShards]sync.Mutex
	latest  map[string]int64
	keyOnce sync.Once
	key     []byte
	keyErr  error
}

// NewCursorCheckpointStore creates a disabled store when authDir is empty.
func NewCursorCheckpointStore(authDir string, ttl time.Duration) *CursorCheckpointStore {
	authDir = strings.TrimSpace(authDir)
	if authDir == "" || ttl <= 0 || !cursorCheckpointPersistenceAvailable() {
		return &CursorCheckpointStore{}
	}
	return &CursorCheckpointStore{root: filepath.Join(authDir, cursorCheckpointStoreDir), ttl: ttl, latest: make(map[string]int64)}
}

// Load returns the newest valid primary or backup snapshot.
func (s *CursorCheckpointStore) Load(conversationID, authID string) (CursorCheckpointSnapshot, bool, error) {
	if s == nil || s.root == "" {
		return CursorCheckpointSnapshot{}, false, nil
	}
	path := s.snapshotPath(conversationID, authID)
	pathLock := s.lockFor(path)
	pathLock.Lock()
	defer pathLock.Unlock()
	var loadErrors error
	for _, candidate := range []string{path, path + ".bak"} {
		snapshot, ok, err := s.loadFile(candidate)
		if err == nil && ok {
			s.setLatest(path, snapshot.UpdatedAt.UnixNano())
			return snapshot, true, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			loadErrors = errors.Join(loadErrors, err)
		}
	}
	if loadErrors != nil {
		return CursorCheckpointSnapshot{}, false, fmt.Errorf("load cursor checkpoint: %w", loadErrors)
	}
	return CursorCheckpointSnapshot{}, false, nil
}

// Save atomically replaces the latest checkpoint while retaining a recovery
// backup until the new file is durable.
func (s *CursorCheckpointStore) Save(conversationID, authID string, snapshot CursorCheckpointSnapshot) error {
	if s == nil || s.root == "" || len(snapshot.Data) == 0 {
		return nil
	}
	if snapshot.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = time.Now()
	}
	if cursorCheckpointSnapshotSize(snapshot) > cursorCheckpointStoreMaxSize {
		return fmt.Errorf("cursor checkpoint snapshot exceeds %d bytes", cursorCheckpointStoreMaxSize)
	}

	disk := cursorCheckpointDiskSnapshot{
		Version:       2,
		UpdatedAtUnix: snapshot.UpdatedAt.UnixNano(),
		Data:          snapshot.Data,
		Blobs:         snapshot.Blobs,
		Pending:       snapshot.Pending,
	}
	var plaintext bytes.Buffer
	if err := gob.NewEncoder(&plaintext).Encode(&disk); err != nil {
		return fmt.Errorf("encode cursor checkpoint: %w", err)
	}
	key, err := s.encryptionKey()
	if err != nil {
		return err
	}
	path := s.snapshotPath(conversationID, authID)
	sealed, err := sealCursorCheckpoint(key, filepath.Base(path), plaintext.Bytes())
	if err != nil {
		return err
	}
	encoded := append([]byte(cursorCheckpointStoreMagic), sealed...)

	pathLock := s.lockFor(path)
	pathLock.Lock()
	defer pathLock.Unlock()
	if latest := s.latestAt(path); latest >= snapshot.UpdatedAt.UnixNano() {
		return nil
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("create cursor checkpoint directory: %w", err)
	}

	tmp, err := os.CreateTemp(s.root, filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("create cursor checkpoint temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(encoded)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write cursor checkpoint temp file: %w", err)
	}

	backup := path + ".bak"
	_ = os.Remove(backup)
	if renameErr := os.Rename(path, backup); renameErr != nil && !errors.Is(renameErr, os.ErrNotExist) {
		return fmt.Errorf("backup cursor checkpoint: %w", renameErr)
	}
	if err = os.Rename(tmpPath, path); err != nil {
		_ = os.Rename(backup, path)
		return fmt.Errorf("install cursor checkpoint: %w", err)
	}
	s.setLatest(path, snapshot.UpdatedAt.UnixNano())
	_ = os.Remove(backup)
	return nil
}

// Delete removes only the snapshot for the specified auth and conversation.
func (s *CursorCheckpointStore) Delete(conversationID, authID string) error {
	if s == nil || s.root == "" {
		return nil
	}
	path := s.snapshotPath(conversationID, authID)
	pathLock := s.lockFor(path)
	pathLock.Lock()
	defer pathLock.Unlock()
	s.deleteLatest(path)
	var result error
	for _, candidate := range []string{path, path + ".bak"} {
		if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}
	return result
}

// Cleanup removes expired snapshots and abandoned temporary files.
func (s *CursorCheckpointStore) Cleanup(now time.Time) error {
	if s == nil || s.root == "" {
		return nil
	}
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var result error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if entry.Name() == cursorCheckpointStoreKeyFile {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			result = errors.Join(result, infoErr)
			continue
		}
		maxAge := s.ttl
		if strings.Contains(entry.Name(), ".tmp-") {
			maxAge = 10 * time.Minute
		}
		if now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		entryPath := filepath.Join(s.root, entry.Name())
		lockPath := strings.TrimSuffix(entryPath, ".bak")
		entryLock := s.lockFor(lockPath)
		entryLock.Lock()
		removeErr := os.Remove(entryPath)
		if removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
			s.deleteLatest(lockPath)
		}
		entryLock.Unlock()
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			result = errors.Join(result, removeErr)
		}
	}
	return result
}

func (s *CursorCheckpointStore) lockFor(path string) *sync.Mutex {
	digest := sha256.Sum256([]byte(path))
	return &s.locks[int(digest[0])%len(s.locks)]
}

func (s *CursorCheckpointStore) latestAt(path string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest[path]
}

func (s *CursorCheckpointStore) setLatest(path string, value int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest == nil {
		s.latest = make(map[string]int64)
	}
	if value > s.latest[path] {
		s.latest[path] = value
	}
}

func (s *CursorCheckpointStore) deleteLatest(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.latest, path)
}

func (s *CursorCheckpointStore) snapshotPath(conversationID, authID string) string {
	digest := sha256.Sum256([]byte(authID + "\x00" + conversationID))
	return filepath.Join(s.root, hex.EncodeToString(digest[:])+".bin")
}

func (s *CursorCheckpointStore) loadFile(path string) (CursorCheckpointSnapshot, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return CursorCheckpointSnapshot{}, false, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return CursorCheckpointSnapshot{}, false, err
	}
	if info.Size() <= int64(len(cursorCheckpointStoreMagic)) || info.Size() > cursorCheckpointStoreMaxSize {
		return CursorCheckpointSnapshot{}, false, fmt.Errorf("invalid cursor checkpoint size %d", info.Size())
	}
	limited := io.LimitReader(file, cursorCheckpointStoreMaxSize+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return CursorCheckpointSnapshot{}, false, err
	}
	if len(raw) <= len(cursorCheckpointStoreMagic) || string(raw[:len(cursorCheckpointStoreMagic)]) != cursorCheckpointStoreMagic {
		return CursorCheckpointSnapshot{}, false, fmt.Errorf("invalid cursor checkpoint header")
	}
	key, err := s.encryptionKey()
	if err != nil {
		return CursorCheckpointSnapshot{}, false, err
	}
	plaintext, err := openCursorCheckpoint(key, filepath.Base(strings.TrimSuffix(path, ".bak")), raw[len(cursorCheckpointStoreMagic):])
	if err != nil {
		return CursorCheckpointSnapshot{}, false, fmt.Errorf("decrypt cursor checkpoint: %w", err)
	}
	var disk cursorCheckpointDiskSnapshot
	if err = gob.NewDecoder(bytes.NewReader(plaintext)).Decode(&disk); err != nil {
		return CursorCheckpointSnapshot{}, false, fmt.Errorf("decode cursor checkpoint: %w", err)
	}
	updatedAt := time.Unix(0, disk.UpdatedAtUnix)
	// Version 1 did not record whether the checkpoint still waited for a tool
	// result, so reusing it after restart is ambiguous and therefore unsafe.
	if disk.Version != 2 || len(disk.Data) == 0 || time.Since(updatedAt) > s.ttl {
		return CursorCheckpointSnapshot{}, false, nil
	}
	return CursorCheckpointSnapshot{
		Data:      append([]byte(nil), disk.Data...),
		Blobs:     cloneCursorCheckpointBlobs(disk.Blobs),
		Pending:   append([]CursorCheckpointPendingTool(nil), disk.Pending...),
		UpdatedAt: updatedAt,
	}, true, nil
}

func (s *CursorCheckpointStore) encryptionKey() ([]byte, error) {
	if s == nil || s.root == "" {
		return nil, fmt.Errorf("cursor checkpoint store is disabled")
	}
	s.keyOnce.Do(func() {
		if err := os.MkdirAll(s.root, 0o700); err != nil {
			s.keyErr = fmt.Errorf("create cursor checkpoint directory: %w", err)
			return
		}
		keyPath := filepath.Join(s.root, cursorCheckpointStoreKeyFile)
		wrapped, err := os.ReadFile(keyPath)
		if err == nil {
			s.key, s.keyErr = unprotectCursorCheckpointKey(wrapped)
			if s.keyErr == nil && len(s.key) != 32 {
				s.keyErr = fmt.Errorf("invalid cursor checkpoint key size %d", len(s.key))
			}
			return
		}
		if !errors.Is(err, os.ErrNotExist) {
			s.keyErr = fmt.Errorf("read cursor checkpoint key: %w", err)
			return
		}
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			s.keyErr = fmt.Errorf("generate cursor checkpoint key: %w", err)
			return
		}
		wrapped, err = protectCursorCheckpointKey(key)
		if err != nil {
			s.keyErr = fmt.Errorf("protect cursor checkpoint key: %w", err)
			return
		}
		file, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			// Another process initialized the same auth directory first. Reuse
			// that durable key instead of failing or replacing it.
			existing, readErr := os.ReadFile(keyPath)
			if readErr != nil {
				s.keyErr = fmt.Errorf("read concurrently created cursor checkpoint key: %w", readErr)
				return
			}
			s.key, s.keyErr = unprotectCursorCheckpointKey(existing)
			if s.keyErr == nil && len(s.key) != 32 {
				s.keyErr = fmt.Errorf("invalid cursor checkpoint key size %d", len(s.key))
			}
			return
		}
		if err != nil {
			s.keyErr = fmt.Errorf("create cursor checkpoint key: %w", err)
			return
		}
		if _, err = file.Write(wrapped); err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(keyPath)
			s.keyErr = fmt.Errorf("write cursor checkpoint key: %w", err)
			return
		}
		s.key = key
	})
	return append([]byte(nil), s.key...), s.keyErr
}

func sealCursorCheckpoint(key []byte, associatedName string, plaintext []byte) ([]byte, error) {
	aead, err := cursorCheckpointAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate cursor checkpoint nonce: %w", err)
	}
	return append(nonce, aead.Seal(nil, nonce, plaintext, []byte(associatedName))...), nil
}

func openCursorCheckpoint(key []byte, associatedName string, sealed []byte) ([]byte, error) {
	aead, err := cursorCheckpointAEAD(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < aead.NonceSize()+aead.Overhead() {
		return nil, fmt.Errorf("encrypted cursor checkpoint is truncated")
	}
	nonce := sealed[:aead.NonceSize()]
	return aead.Open(nil, nonce, sealed[aead.NonceSize():], []byte(associatedName))
}

func cursorCheckpointAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cursor checkpoint cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create cursor checkpoint AEAD: %w", err)
	}
	return aead, nil
}

func cursorCheckpointSnapshotSize(snapshot CursorCheckpointSnapshot) int {
	size := len(snapshot.Data)
	for key, value := range snapshot.Blobs {
		size += len(key) + len(value)
	}
	for _, pending := range snapshot.Pending {
		size += len(pending.ToolCallID) + len(pending.ToolName)
	}
	return size
}

func cloneCursorCheckpointBlobs(src map[string][]byte) map[string][]byte {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string][]byte, len(src))
	for key, value := range src {
		dst[key] = append([]byte(nil), value...)
	}
	return dst
}
