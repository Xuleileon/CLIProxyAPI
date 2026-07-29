package proto

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// ConnectEndStreamFlag marks the end-of-stream frame (trailers).
	ConnectEndStreamFlag byte = 0x02
	// ConnectCompressionFlag indicates the payload is compressed (not supported).
	ConnectCompressionFlag byte = 0x01
	// ConnectFrameHeaderSize is the fixed 5-byte frame header.
	ConnectFrameHeaderSize = 5
)

// FrameConnectMessage wraps a protobuf payload in a Connect frame.
// Frame format: [1 byte flags][4 bytes payload length (big-endian)][payload]
func FrameConnectMessage(data []byte, flags byte) []byte {
	frame := make([]byte, ConnectFrameHeaderSize+len(data))
	frame[0] = flags
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(data)))
	copy(frame[5:], data)
	return frame
}

// ParseConnectFrame extracts one frame from a buffer.
// Returns (flags, payload, bytesConsumed, ok).
// ok is false when the buffer is too short for a complete frame.
func ParseConnectFrame(buf []byte) (flags byte, payload []byte, consumed int, ok bool) {
	if len(buf) < ConnectFrameHeaderSize {
		return 0, nil, 0, false
	}
	flags = buf[0]
	length := binary.BigEndian.Uint32(buf[1:5])
	total := ConnectFrameHeaderSize + int(length)
	if len(buf) < total {
		return 0, nil, 0, false
	}
	return flags, buf[5:total], total, true
}

// ConnectError is a structured error from the Connect protocol end-of-stream trailer.
// The Code field contains the server-defined error code (e.g. gRPC standard codes
// like "resource_exhausted", "unauthenticated", "permission_denied", "unavailable").
type ConnectError struct {
	Code    string // server-defined error code
	Message string // human-readable error description
}

func (e *ConnectError) Error() string {
	return fmt.Sprintf("Connect error %s: %s", e.Code, e.Message)
}

// ParseConnectEndStream parses a Connect end-of-stream frame payload (JSON).
// Returns nil if there is no error in the trailer.
// On error, returns a *ConnectError with the server's error code and message.
func ParseConnectEndStream(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	var trailer struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details []struct {
				Debug *struct {
					Error   string `json:"error"`
					Details *struct {
						Title  string `json:"title"`
						Detail string `json:"detail"`
					} `json:"details"`
				} `json:"debug"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &trailer); err != nil {
		return fmt.Errorf("failed to parse Connect end stream: %w", err)
	}
	if trailer.Error != nil {
		code := trailer.Error.Code
		if code == "" {
			code = "unknown"
		}
		msg := trailer.Error.Message
		if msg == "" {
			msg = "Unknown error"
		}
		// Cursor often returns a generic message with the real reason in details[].debug.
		for _, d := range trailer.Error.Details {
			if d.Debug == nil {
				continue
			}
			parts := make([]string, 0, 3)
			if d.Debug.Error != "" {
				parts = append(parts, d.Debug.Error)
			}
			if d.Debug.Details != nil {
				if d.Debug.Details.Title != "" {
					parts = append(parts, d.Debug.Details.Title)
				}
				if d.Debug.Details.Detail != "" {
					parts = append(parts, d.Debug.Details.Detail)
				}
			}
			if len(parts) > 0 {
				detail := strings.Join(parts, ": ")
				if msg == "" || msg == "Error" || msg == "Unknown error" {
					msg = detail
				} else {
					msg = msg + " (" + detail + ")"
				}
				break
			}
		}
		return &ConnectError{Code: code, Message: msg}
	}
	return nil
}
