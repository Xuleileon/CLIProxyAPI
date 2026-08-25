//go:build !windows

package helps

import "fmt"

func cursorCheckpointPersistenceAvailable() bool { return false }

func protectCursorCheckpointKey(key []byte) ([]byte, error) {
	return nil, fmt.Errorf("cursor checkpoint persistence requires an OS-protected key store")
}

func unprotectCursorCheckpointKey(wrapped []byte) ([]byte, error) {
	return nil, fmt.Errorf("cursor checkpoint persistence requires an OS-protected key store")
}
