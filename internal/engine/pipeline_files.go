package engine

import (
	"hash/crc32"
	"io"
	"os"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
)

// This file holds the two file-naming helpers the pipeline needs that no frozen
// package provides: the CRC32 tag the legacy code appended to every finished
// file (Model/OKEFile.cs AddCRC32) and the DOS-style pattern the cleaner's search
// uses.

// addCRC32 renames a finished file to `<stem> [XXXXXXXX]<ext>`, mirroring
// OKEFile.AddCRC32 with Utils/CRC32.ComputeChecksumString.
//
// The tag is what lets an operator tell two encodes of the same episode apart,
// and the legacy pipeline applied it to every External track and to the final
// container. A failure is reported, not fatal: the file exists and is usable
// under its untagged name, which is what the legacy `return false` meant.
func addCRC32(path string) error {
	target := crc32Name(path)
	if target == path {
		return nil
	}
	if err := os.Rename(path, target); err != nil {
		return err
	}
	log.Debug("已添加CRC32", "file", target)
	return nil
}

// fileCRC32 computes the standard IEEE CRC-32 of a file's contents. The legacy
// SafeProxy implements the same polynomial (0xEDB88320) and the same final XOR,
// so the tag matches the .NET release byte for byte.
func fileCRC32(path string) (uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	h := crc32.NewIEEE()
	if _, err := io.Copy(h, f); err != nil {
		return 0, err
	}
	return h.Sum32(), nil
}

// upperHex8 renders a 32-bit value as the eight uppercase hex digits .NET's
// ToString("X8") produced.
func upperHex8(v uint32) string {
	const digits = "0123456789ABCDEF"
	var buf [8]byte
	for i := len(buf) - 1; i >= 0; i-- {
		buf[i] = digits[v&0xF]
		v >>= 4
	}
	return string(buf[:])
}
