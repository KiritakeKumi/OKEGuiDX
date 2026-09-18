package profile

import "testing"

// The shipped profiles all contain trailing commas. These tests pin that
// tolerance down so a future refactor cannot silently break every existing
// installation.

func TestStripTrailingCommas(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "array trailing comma",
			in:   `{"InputFiles":["a.m2ts","b.m2ts",]}`,
			want: `{"InputFiles":["a.m2ts","b.m2ts"]}`,
		},
		{
			name: "object trailing comma",
			in:   `{"Version":3,"VSVersion":"2024H1",}`,
			want: `{"Version":3,"VSVersion":"2024H1"}`,
		},
		{
			name: "nested with whitespace",
			in:   "{\"ReEncodeSliceArray\":[{\"begin\":2400,\"end\":2900\n},]}",
			want: "{\"ReEncodeSliceArray\":[{\"begin\":2400,\"end\":2900\n}]}",
		},
		{
			name: "comma inside a string is preserved",
			in:   `{"Name":"Commentary, part 2",}`,
			want: `{"Name":"Commentary, part 2"}`,
		},
		{
			name: "escaped quote inside a string",
			in:   `{"Param":"--csv \"a,b\"",}`,
			want: `{"Param":"--csv \"a,b\""}`,
		},
		{
			name: "no trailing comma is untouched",
			in:   `{"Version":3}`,
			want: `{"Version":3}`,
		},
		{
			name: "empty containers",
			in:   `{"A":[],"B":{}}`,
			want: `{"A":[],"B":{}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := stripTrailingCommas(tc.in); got != tc.want {
				t.Errorf("stripTrailingCommas() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHasTrailingComma(t *testing.T) {
	t.Parallel()
	positive := []string{
		`[1,2,]`,
		`{"a":1,}`,
		"[1,\n]",
	}
	for _, in := range positive {
		if !hasTrailingComma(in) {
			t.Errorf("hasTrailingComma(%q) = false, want true", in)
		}
	}
	negative := []string{
		`[1,2]`,
		`{"a":1}`,
		`{"a":"x,]"}`,
		`[1, 2]`,
	}
	for _, in := range negative {
		if hasTrailingComma(in) {
			t.Errorf("hasTrailingComma(%q) = true, want false", in)
		}
	}
}

func TestParseAcceptsTrailingCommas(t *testing.T) {
	t.Parallel()
	// This is the shape of every shipped example profile.
	raw := `{
    "Version" : 3,
    "VSVersion" : "2024H1",
    "EncoderType" : "x265",
    "ContainerFormat" : "mkv",
    "Fps" : 23.976,
    "InputFiles" : [
        "a.m2ts",
        "b.m2ts",
    ],
    "AudioTracks" : [{
        "OutputCodec" : "flac"
    },],
}`
	p, err := Parse(raw, "test.json")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(p.InputFiles) != 2 {
		t.Errorf("len(InputFiles) = %d, want 2", len(p.InputFiles))
	}
	if len(p.AudioTracks) != 1 {
		t.Errorf("len(AudioTracks) = %d, want 1", len(p.AudioTracks))
	}
}
