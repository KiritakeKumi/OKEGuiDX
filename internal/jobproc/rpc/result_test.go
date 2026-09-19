package rpc

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// The fixtures in testdata/legacy_*.json are byte-for-byte output of the
// legacy writer: Newtonsoft.Json 13.0.1 serializing the RpcResult/RpcResult3
// classes from RpChecker.cs. They are the regression baseline for the decoder.

func TestParseLegacyFixtures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		fixture     string
		wantSamples []Sample
		wantYUV     bool
		wantPair    *FileNamePair
	}{
		{
			// RpcResult: every RPCOUT line carried exactly two numbers.
			name:    "two-field result",
			fixture: "legacy_v2.json",
			wantSamples: []Sample{
				{Index: 0, Value: 40.123457, ValueU: PSNRUVThreshold, ValueV: PSNRUVThreshold},
				{Index: 1, Value: 41.987654, ValueU: PSNRUVThreshold, ValueV: PSNRUVThreshold},
				{Index: 2, Value: 39.5, ValueU: PSNRUVThreshold, ValueV: PSNRUVThreshold},
			},
			wantPair: &FileNamePair{Src: `D:\a\source.vpy`, Opt: `D:\a\00001.mkv`},
		},
		{
			// RpcResult3: four numbers per line. RpChecker never assigned
			// FileNamePair on this instance, so the legacy file holds nulls.
			name:    "four-field result",
			fixture: "legacy_v3.json",
			wantSamples: []Sample{
				{Index: 0, Value: 40.123457, ValueU: 45.123457, ValueV: 46.123457},
				{Index: 1, Value: 41.987654, ValueU: 45.987654, ValueV: 46.987654},
				{Index: 2, Value: 39.5, ValueU: 44.5, ValueV: 45.5},
			},
			wantYUV: true,
		},
		{
			// A run with no RPCOUT line at all.
			name:    "empty result",
			fixture: "legacy_empty.json",
			// The Data array is empty, so nothing reveals the tuple arity.
			// The legacy writer emitted RpcResult3 in that case, but a reader
			// cannot tell; YUV stays false and the encoder re-derives it from
			// the sample count.
			wantYUV: false,
		},
		{
			name:    "failing result",
			fixture: "legacy_failing.json",
			wantSamples: []Sample{
				{Index: 10, Value: 25.5, ValueU: 45, ValueV: 46},
				{Index: 11, Value: 41, ValueU: 30, ValueV: 46},
			},
			wantYUV: true,
		},
		{
			name:    "non-ASCII paths",
			fixture: "legacy_unicode.json",
			wantSamples: []Sample{
				{Index: 0, Value: 40.5, ValueU: PSNRUVThreshold, ValueV: PSNRUVThreshold},
			},
			wantPair: &FileNamePair{Src: `D:\我的 作品\第01話.vpy`, Opt: `D:\我的 作品\第01話.mkv`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := readFixture(t, tc.fixture)
			got, err := ParseResult(data)
			if err != nil {
				t.Fatalf("ParseResult() error = %v", err)
			}
			assertSamples(t, got.Samples, tc.wantSamples)
			if got.YUV != tc.wantYUV {
				t.Errorf("YUV = %v, want %v", got.YUV, tc.wantYUV)
			}
			assertPair(t, got.FileNamePair, tc.wantPair)
			if got.Logs.Inf {
				t.Error("Logs.Inf = true, want false")
			}
		})
	}
}

// TestParseArrayForm covers the shape RPChecker's own writer produces for its
// ResultV1/ResultV2 fallbacks, where the tuples are JSON arrays.
func TestParseArrayForm(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		raw     string
		want    []Sample
		wantYUV bool
	}{
		{
			name: "two-element arrays",
			raw:  `[{"Data":[[0,40.5],[1,41.25]],"FileNamePair":["src.vpy","00001.mkv"],"Logs":{"Inf":false}}]`,
			want: []Sample{
				{Index: 0, Value: 40.5, ValueU: PSNRUVThreshold, ValueV: PSNRUVThreshold},
				{Index: 1, Value: 41.25, ValueU: PSNRUVThreshold, ValueV: PSNRUVThreshold},
			},
		},
		{
			name:    "four-element arrays",
			raw:     `[{"Data":[[0,40.5,45.5,46.5]],"FileNamePair":null,"Logs":{"Inf":false}}]`,
			want:    []Sample{{Index: 0, Value: 40.5, ValueU: 45.5, ValueV: 46.5}},
			wantYUV: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseResult([]byte(tc.raw))
			if err != nil {
				t.Fatalf("ParseResult() error = %v", err)
			}
			assertSamples(t, got.Samples, tc.want)
			if got.YUV != tc.wantYUV {
				t.Errorf("YUV = %v, want %v", got.YUV, tc.wantYUV)
			}
		})
	}
}

func TestParseResultRejectsMalformedInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
	}{
		{"empty input", ""},
		{"not JSON", "not json at all"},
		{"empty array", "[]"},
		{"Data is not an array", `[{"Data":{},"FileNamePair":null,"Logs":{"Inf":false}}]`},
		{"sample is not a number", `[{"Data":[{"Item1":0,"Item2":"high"}],"FileNamePair":null,"Logs":{"Inf":false}}]`},
		{"sample missing Item2", `[{"Data":[{"Item1":0}],"FileNamePair":null,"Logs":{"Inf":false}}]`},
		{"short array sample", `[{"Data":[[0]],"FileNamePair":null,"Logs":{"Inf":false}}]`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseResult([]byte(tc.raw))
			if err == nil {
				t.Fatalf("ParseResult() = %+v, want an error", got)
			}
			if e := okerr.AsError(err); e.Kind != ErrResultParse.Kind {
				t.Errorf("Kind = %q, want %q", e.Kind, ErrResultParse.Kind)
			}
		})
	}
}

func TestEncodeResultMatchesLegacyShape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		result  *Result
		yuv     bool
		want    string
		wantKey []string
	}{
		{
			name: "two-field",
			result: &Result{
				Samples: []Sample{
					{Index: 0, Value: 40.123457},
					{Index: 1, Value: 41.987654},
				},
				FileNamePair: &FileNamePair{Src: `D:\a\source.vpy`, Opt: `D:\a\00001.mkv`},
			},
			want:    `[{"Data":[{"Item1":0,"Item2":40.123457},{"Item1":1,"Item2":41.987654}],"FileNamePair":{"Item1":"D:\\a\\source.vpy","Item2":"D:\\a\\00001.mkv"},"Logs":{"Inf":false}}]`,
			wantKey: []string{"Data", "FileNamePair", "Logs"},
		},
		{
			name: "four-field without a pair",
			result: &Result{
				Samples: []Sample{{Index: 0, Value: 40.123457, ValueU: 45.123457, ValueV: 46.123457}},
			},
			yuv:     true,
			want:    `[{"Data":[{"Item1":0,"Item2":40.123457,"Item3":45.123457,"Item4":46.123457}],"FileNamePair":{"Item1":null,"Item2":null},"Logs":{"Inf":false}}]`,
			wantKey: []string{"Data", "FileNamePair", "Logs"},
		},
		{
			name:    "empty four-field",
			result:  &Result{},
			yuv:     true,
			want:    `[{"Data":[],"FileNamePair":{"Item1":null,"Item2":null},"Logs":{"Inf":false}}]`,
			wantKey: []string{"Data", "FileNamePair", "Logs"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := EncodeResult(tc.result, tc.yuv)
			if err != nil {
				t.Fatalf("EncodeResult() error = %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("EncodeResult() = %s\nwant %s", got, tc.want)
			}
			// Field order matters to nothing but readability, yet a stable
			// shape is what the regression fixtures compare against.
			var probe map[string]json.RawMessage
			if err := json.Unmarshal(got[1:len(got)-1], &probe); err != nil {
				t.Fatalf("output is not a JSON object: %v", err)
			}
			for _, key := range tc.wantKey {
				if _, ok := probe[key]; !ok {
					t.Errorf("missing key %q in %s", key, got)
				}
			}
		})
	}
}

// TestEncodeResultRoundTrips feeds every legacy fixture back through the
// encoder and checks that the decoded value survives.
func TestEncodeResultRoundTrips(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"legacy_v2.json", "legacy_v3.json", "legacy_failing.json", "legacy_unicode.json"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			first, err := ParseResult(readFixture(t, name))
			if err != nil {
				t.Fatalf("ParseResult() error = %v", err)
			}
			data, err := EncodeResult(first, first.YUV)
			if err != nil {
				t.Fatalf("EncodeResult() error = %v", err)
			}
			second, err := ParseResult(data)
			if err != nil {
				t.Fatalf("re-parse error = %v", err)
			}
			assertSamples(t, second.Samples, first.Samples)
			assertPair(t, second.FileNamePair, first.FileNamePair)
			if second.YUV != first.YUV {
				t.Errorf("YUV = %v, want %v", second.YUV, first.YUV)
			}
		})
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func assertSamples(t *testing.T, got, want []Sample) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d samples, want %d\n got: %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("sample[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func assertPair(t *testing.T, got, want *FileNamePair) {
	t.Helper()
	switch {
	case got == nil && want == nil:
		return
	case got == nil:
		t.Fatalf("FileNamePair = nil, want %+v", want)
	case want == nil:
		t.Fatalf("FileNamePair = %+v, want nil", got)
	case *got != *want:
		t.Errorf("FileNamePair = %+v, want %+v", *got, *want)
	}
}

// TestTracebackRegexMatchesRealOutput pins the pattern against a real vspipe
// traceback captured from VapourSynth R42.
func TestTracebackRegexMatchesRealOutput(t *testing.T) {
	t.Parallel()

	lines := strings.Split(strings.TrimRight(string(readFixture(t, "vspipe_vpy_error_stderr.txt")), "\r\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	if !containsLine(lines, "Python exception: No module named 'definitely_not_installed'") {
		t.Fatalf("fixture does not contain the Python exception line: %q", lines)
	}
	if !containsLine(lines, "ModuleNotFoundError: No module named 'definitely_not_installed'") {
		t.Fatalf("fixture does not contain the traceback terminator: %q", lines)
	}
	if !reTraceback.MatchString("ModuleNotFoundError: No module named 'definitely_not_installed'") {
		t.Error("reTraceback did not match the real traceback terminator")
	}
	if reTraceback.MatchString("Traceback (most recent call last):") {
		t.Error("reTraceback matched a non-terminal traceback line")
	}
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

// TestParseErrorsAreStructured guards the okerr contract of ParseResult.
func TestParseErrorsAreStructured(t *testing.T) {
	t.Parallel()

	_, err := ParseResult([]byte("{"))
	if err == nil {
		t.Fatal("ParseResult() = nil, want an error")
	}
	var structured *okerr.Error
	if !errors.As(err, &structured) {
		t.Fatalf("error is not *okerr.Error: %T", err)
	}
	if structured.Summary == "" {
		t.Error("Summary is empty")
	}
}
