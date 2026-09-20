// Package textfile reads text files the way .NET's File.ReadAllText does.
//
// The legacy application read every profile and every config through
// File.ReadAllText, and both StreamReader and the Windows editors that
// operators use write a byte order mark by default. encoding/json rejects the
// mark with "invalid character '\ufeff'", so a project file the old build
// opened happily would fail here. The json format is frozen and existing
// project files must keep running unchanged, which is why the mark is
// consumed rather than reported.
package textfile

import (
	"bytes"
	"encoding/binary"
	"os"
	"unicode/utf16"
)

// Read returns the decoded contents of path.
//
// A leading byte order mark selects the encoding, exactly as .NET's
// StreamReader did with detectEncodingFromByteOrderMarks enabled: UTF-8,
// UTF-16 little endian, or UTF-16 big endian. Without a mark the content is
// UTF-8, which is also .NET's default. UTF-32 marks are not handled; no
// profile or config in the wild is written that way.
func Read(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Decode(raw), nil
}

// Decode applies Read's byte order mark rules to bytes already in hand, so a
// caller that owns the file rather than the path (the API reads an uploaded
// body, tests build one inline) gets the same treatment.
func Decode(raw []byte) []byte {
	switch {
	case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
		return raw[3:]
	case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}):
		return utf16ToUTF8(raw[2:], binary.LittleEndian)
	case bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
		return utf16ToUTF8(raw[2:], binary.BigEndian)
	default:
		return raw
	}
}

// utf16ToUTF8 converts UTF-16 code units to UTF-8. utf16.Decode already turns
// an unpaired surrogate, which a hand-edited file can contain, into the
// replacement character rather than failing the read.
func utf16ToUTF8(raw []byte, order binary.ByteOrder) []byte {
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		units = append(units, order.Uint16(raw[i:i+2]))
	}
	var buf bytes.Buffer
	buf.Grow(len(raw))
	for _, r := range utf16.Decode(units) {
		buf.WriteRune(r)
	}
	return buf.Bytes()
}
