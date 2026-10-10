package serial

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Supported wire encodings for tool payloads.
const (
	EncodingUTF8   = "utf8"
	EncodingHex    = "hex"
	EncodingBase64 = "base64"
)

// NormalizeEncoding validates an encoding name and accepts common
// spellings (utf-8, UTF8, ...).
func NormalizeEncoding(encoding string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", EncodingUTF8, "utf-8":
		return EncodingUTF8, nil
	case EncodingHex:
		return EncodingHex, nil
	case EncodingBase64:
		return EncodingBase64, nil
	default:
		return "", fmt.Errorf("unsupported encoding %q (use utf8, hex or base64)", encoding)
	}
}

// EncodeData renders bytes in the given encoding. Hex is lowercase with
// byte separators for readability. UTF-8 is strict: use
// hex or base64 for arbitrary binary data.
func EncodeData(data []byte, encoding string) (string, error) {
	switch encoding {
	case EncodingUTF8:
		if !utf8.Valid(data) {
			// Losing console output to an encoding error is worse than
			// mangling a few stray bytes, so substitute U+FFFD.
			return strings.ToValidUTF8(string(data), "\ufffd"), nil
		}
		return string(data), nil
	case EncodingHex:
		var sb strings.Builder
		for i, b := range data {
			if i > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(hex.EncodeToString([]byte{b}))
		}
		return sb.String(), nil
	case EncodingBase64:
		return base64.StdEncoding.EncodeToString(data), nil
	default:
		return "", fmt.Errorf("unsupported encoding %q", encoding)
	}
}

// DecodeData parses a tool payload into bytes.
func DecodeData(data, encoding string) ([]byte, error) {
	switch encoding {
	case EncodingUTF8:
		return []byte(data), nil
	case EncodingHex:
		clean := strings.Map(func(r rune) rune {
			switch r {
			case ' ', '\t', '\n', '\r':
				return -1
			}
			return r
		}, data)
		out, err := hex.DecodeString(clean)
		if err != nil {
			return nil, fmt.Errorf("invalid hex: %w", err)
		}
		return out, nil
	case EncodingBase64:
		out, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data))
		if err != nil {
			return nil, fmt.Errorf("invalid base64: %w", err)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported encoding %q", encoding)
	}
}
