//go:build windows

package helps

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

func cursorCheckpointPersistenceAvailable() bool { return true }

func protectCursorCheckpointKey(key []byte) ([]byte, error) {
	return cursorCheckpointDPAPI(key, true)
}

func unprotectCursorCheckpointKey(wrapped []byte) ([]byte, error) {
	return cursorCheckpointDPAPI(wrapped, false)
}

func cursorCheckpointDPAPI(input []byte, protect bool) ([]byte, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("empty cursor checkpoint key material")
	}
	in := windows.DataBlob{Size: uint32(len(input)), Data: &input[0]}
	var out windows.DataBlob
	var err error
	if protect {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, err
	}
	if out.Data == nil || out.Size == 0 {
		return nil, fmt.Errorf("DPAPI returned empty cursor checkpoint key material")
	}
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) }()
	return append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...), nil
}
