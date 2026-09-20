package textfile

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

// utf16Bytes builds the on-disk form of a UTF-16 string, mark included. The
// tests spell the bytes out this way rather than pasting a literal so the
// encoding under test is visible.
func utf16Bytes(t *testing.T, s string, order binary.ByteOrder, mark []byte) []byte {
	t.Helper()
	out := append([]byte{}, mark...)
	for _, u := range utf16.Encode([]rune(s)) {
		var pair [2]byte
		order.PutUint16(pair[:], u)
		out = append(out, pair[:]...)
	}
	return out
}

func TestDecodeStripsTheMarksTheLegacyReaderAccepted(t *testing.T) {
	t.Parallel()
	const body = `{"ProjectName":"日本語"}`

	cases := []struct {
		name string
		raw  []byte
	}{
		{"no mark", []byte(body)},
		{"utf-8 mark", append([]byte{0xEF, 0xBB, 0xBF}, body...)},
		{
			"utf-16 little endian mark",
			utf16Bytes(t, body, binary.LittleEndian, []byte{0xFF, 0xFE}),
		},
		{
			"utf-16 big endian mark",
			utf16Bytes(t, body, binary.BigEndian, []byte{0xFE, 0xFF}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := string(Decode(tc.raw)); got != body {
				t.Errorf("Decode() = %q, want %q", got, body)
			}
		})
	}
}

// TestDecodeKeepsABomThatIsNotAtTheStart pins that only a leading mark is
// special: a U+FEFF inside the text is content.
func TestDecodeKeepsABomThatIsNotAtTheStart(t *testing.T) {
	t.Parallel()
	raw := []byte("a\uFEFFb")
	if got := string(Decode(raw)); got != "a\uFEFFb" {
		t.Errorf("Decode() = %q, want %q", got, "a\uFEFFb")
	}
}

// TestDecodeDoesNotGuessUTF16WithoutAMark matches the legacy reader: a file
// with no mark is UTF-8, even if its bytes happen to look like UTF-16.
func TestDecodeDoesNotGuessUTF16WithoutAMark(t *testing.T) {
	t.Parallel()
	raw := []byte{'{', 0x00, '}', 0x00}
	if got := Decode(raw); !bytes.Equal(got, raw) {
		t.Errorf("Decode() = % x, want % x", got, raw)
	}
}

func TestReadHandlesAMarkedFile(t *testing.T) {
	t.Parallel()
	const body = `{"ProjectName":"ep01"}`
	path := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(path, append([]byte{0xEF, 0xBB, 0xBF}, body...), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if string(got) != body {
		t.Errorf("Read() = %q, want %q", got, body)
	}
}

func TestReadReportsAMissingFile(t *testing.T) {
	t.Parallel()
	if _, err := Read(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("Read() error = nil, want a not-exist error")
	}
}
