package model

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewFileRefNormalizesSeparators(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"windows absolute", `D:\WORKS\ep01\00000.m2ts`, "/D:/WORKS/ep01/00000.m2ts"},
		{"windows relative", `Main_Disc\BDMV\STREAM\00000.m2ts`, "/Main_Disc/BDMV/STREAM/00000.m2ts"},
		{"unix absolute", "/mnt/media/ep01/00000.m2ts", "/mnt/media/ep01/00000.m2ts"},
		{"bare name", "00000.m2ts", "/00000.m2ts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := NewFileRef(tc.in)
			if got.Rel != tc.want {
				t.Errorf("NewFileRef(%q).Rel = %q, want %q", tc.in, got.Rel, tc.want)
			}
			if got.Volume != LocalVolume {
				t.Errorf("Volume = %q, want %q", got.Volume, LocalVolume)
			}
		})
	}
}

func TestFileRefResolveIsIdentityOnLocalVolume(t *testing.T) {
	t.Parallel()
	// In standalone mode the local volume root carries no prefix of its own, so
	// resolving must give back the path the user typed. This is what makes the
	// FileRef indirection invisible on a single machine.
	//
	// On Windows the root is empty because the drive belongs to each path; on
	// Unix it is the filesystem root. Both must round-trip.
	ref := NewFileRef(`D:\WORKS\okgui\ep01\00000.m2ts`)
	want := filepath.FromSlash("D:/WORKS/okgui/ep01/00000.m2ts")
	for _, root := range []string{"", `D:\`} {
		roots := map[string]string{LocalVolume: root}
		if got := ref.Resolve(roots); got != want {
			t.Errorf("Resolve(root=%q) = %q, want %q", root, got, want)
		}
	}
}

// TestFileRefResolveKeepsDriveLetter is the regression test for the bug where
// the drive was dropped, so every downstream stage looked for the source under
// a path on the current drive instead of the one the user gave.
func TestFileRefResolveKeepsDriveLetter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"windows absolute", `D:\a\b\ep01.mkv`, `D:\a\b\ep01.mkv`},
		{"windows other drive", `C:\a\b\ep01.mkv`, `C:\a\b\ep01.mkv`},
		{"unix absolute", "/a/b/ep01.mkv", "/a/b/ep01.mkv"},
		{"bare name", "ep01.mkv", "/ep01.mkv"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want := filepath.FromSlash(strings.ReplaceAll(tc.want, `\`, "/"))
			if got := NewFileRef(tc.in).ResolveLocal(""); got != want {
				t.Errorf("NewFileRef(%q).ResolveLocal(\"\") = %q, want %q", tc.in, got, want)
			}
		})
	}
}

// TestFileRefDriveWinsOverRoot pins the rule that a drive-qualified reference
// is not prefixed with a volume root: the drive is per path, so prefixing one
// would produce a path like "E:\mnt\D:\a".
func TestFileRefDriveWinsOverRoot(t *testing.T) {
	t.Parallel()
	ref := NewFileRef(`D:\a\b\ep01.mkv`)
	got := ref.Resolve(map[string]string{LocalVolume: `E:\mnt`})
	want := filepath.FromSlash("D:/a/b/ep01.mkv")
	if got != want {
		t.Errorf("Resolve() = %q, want %q", got, want)
	}
}

func TestFileRefUnknownVolumeFallsBackToLocal(t *testing.T) {
	t.Parallel()
	ref := FileRef{Volume: "nas", Rel: "/ep01/00000.m2ts"}
	roots := map[string]string{LocalVolume: "/mnt"}
	got := ref.Resolve(roots)
	want := filepath.Join("/mnt", "ep01", "00000.m2ts")
	if got != want {
		t.Errorf("Resolve() = %q, want %q (fallback to local volume)", got, want)
	}
}

func TestFileRefHelpers(t *testing.T) {
	t.Parallel()
	ref := NewFileRef("/media/ep01/00000.m2ts")
	if got := ref.Base(); got != "00000.m2ts" {
		t.Errorf("Base() = %q", got)
	}
	if got := ref.Ext(); got != ".m2ts" {
		t.Errorf("Ext() = %q", got)
	}
	if got := ref.Dir().String(); got != "local/media/ep01" {
		t.Errorf("Dir().String() = %q", got)
	}
	if got := ref.WithExt(".mkv").Rel; got != "/media/ep01/00000.mkv" {
		t.Errorf("WithExt() = %q", got)
	}
	if got := ref.Join("sub.mkv").Rel; got != "/media/ep01/00000.m2ts/sub.mkv" {
		t.Errorf("Join() = %q", got)
	}
}

func TestFileRefJSONRoundTrip(t *testing.T) {
	t.Parallel()
	orig := NewFileRef(`D:\WORKS\ep01\00000.m2ts`)
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var back FileRef
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if back != orig {
		t.Errorf("round trip = %+v, want %+v", back, orig)
	}
}

// TestFileRefDriveSurvivesQueueRoundTrip is the end-to-end anchor for the
// dropped-drive bug. The task queue serializes a FileRef to the documented
// "volume/path" string (MarshalText) and reads it back (UnmarshalText); every
// downstream stage then calls Resolve. Both expectations below are literals
// rather than values computed by the code under test, so a regression cannot
// hide: reverting normalizeRel or Resolve makes this fail.
func TestFileRefDriveSurvivesQueueRoundTrip(t *testing.T) {
	t.Parallel()
	orig := NewFileRef(`D:\a\b\ep01.mkv`)

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	// A drive-qualified reference keeps the drive in its rel form, so the
	// serialized identity is "local/D:/a/b/ep01.mkv", not "local/a/b/ep01.mkv".
	if want := `"local/D:/a/b/ep01.mkv"`; string(data) != want {
		t.Fatalf("Marshal() = %s, want %s", data, want)
	}

	var back FileRef
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got, want := back.ResolveLocal(""), filepath.FromSlash("D:/a/b/ep01.mkv"); got != want {
		t.Errorf("ResolveLocal(\"\") after a queue round trip = %q, want %q", got, want)
	}
}

func TestNewTaskIDIsUniqueAndWellFormed(t *testing.T) {
	t.Parallel()
	seen := make(map[TaskID]struct{}, 1000)
	for range 1000 {
		id := NewTaskID()
		if len(id) != 36 {
			t.Fatalf("NewTaskID() = %q, want 36 characters", id)
		}
		if _, err := ParseTaskID(string(id)); err != nil {
			t.Fatalf("ParseTaskID(%q) error = %v", id, err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("NewTaskID() produced a duplicate: %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestParseTaskIDRejectsMalformed(t *testing.T) {
	t.Parallel()
	for _, s := range []string{
		"",
		"not-a-uuid",
		"00000000-0000-4000-8000-00000000000",   // too short
		"00000000-0000-4000-8000-0000000000000", // too long
		"00000000x0000-4000-8000-000000000000",  // wrong separator
		"zzzzzzzz-0000-4000-8000-000000000000",  // non-hex
	} {
		if _, err := ParseTaskID(s); err == nil {
			t.Errorf("ParseTaskID(%q) = nil error, want rejection", s)
		}
	}
}

func TestSliceInfoIsIllegal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		slice SliceInfo
		want  bool
	}{
		{"normal", SliceInfo{100, 200}, false},
		{"open ended", SliceInfo{100, OpenEnded}, false},
		{"negative begin", SliceInfo{-1, 200}, true},
		{"end before begin", SliceInfo{200, 100}, true},
		{"empty range", SliceInfo{100, 100}, true},
		{"negative end other than -1", SliceInfo{100, -5}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.slice.IsIllegal(); got != tc.want {
				t.Errorf("IsIllegal() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckAndMerge(t *testing.T) {
	t.Parallel()
	t.Run("contiguous slices merge", func(t *testing.T) {
		t.Parallel()
		// This is the exact case from dist/windows/examples/00001.m2ts.json:
		// [40,2400] and [2400,2900] are adjacent and must become one slice.
		got, err := CheckAndMerge([]SliceInfo{{40, 2400}, {2400, 2900}})
		if err != nil {
			t.Fatalf("CheckAndMerge() error = %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d slices, want 1: %v", len(got), got)
		}
		if got[0].Begin != 40 || got[0].End != 2900 {
			t.Errorf("merged = %v, want [40, 2900]", got[0])
		}
	})

	t.Run("open ended survives", func(t *testing.T) {
		t.Parallel()
		got, err := CheckAndMerge([]SliceInfo{{100, 200}, {200, OpenEnded}})
		if err != nil {
			t.Fatalf("CheckAndMerge() error = %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d slices, want 1: %v", len(got), got)
		}
		if got[0].End != OpenEnded {
			t.Errorf("End = %d, want %d", got[0].End, OpenEnded)
		}
	})

	t.Run("overlap is rejected", func(t *testing.T) {
		t.Parallel()
		_, err := CheckAndMerge([]SliceInfo{{100, 300}, {200, 400}})
		if !errors.Is(err, ErrSlicesOverlap) {
			t.Fatalf("error = %v, want ErrSlicesOverlap", err)
		}
	})

	t.Run("disjoint slices are kept", func(t *testing.T) {
		t.Parallel()
		got, err := CheckAndMerge([]SliceInfo{{0, 100}, {200, 300}})
		if err != nil {
			t.Fatalf("CheckAndMerge() error = %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d slices, want 2: %v", len(got), got)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		t.Parallel()
		got, err := CheckAndMerge(nil)
		if err != nil || got != nil {
			t.Fatalf("CheckAndMerge(nil) = %v, %v; want nil, nil", got, err)
		}
	})
}

func TestMergeSlicesMergesOverlaps(t *testing.T) {
	t.Parallel()
	got := MergeSlices([]SliceInfo{{0, 100}, {50, 200}, {300, 400}})
	if len(got) != 2 {
		t.Fatalf("got %d slices, want 2: %v", len(got), got)
	}
	if got[0].Begin != 0 || got[0].End != 200 {
		t.Errorf("first = %v, want [0, 200]", got[0])
	}
}

func TestInfoSetDupOrEmptyDowngradesMuxing(t *testing.T) {
	t.Parallel()
	// A duplicate or silent track must never be muxed into the main container.
	for _, mux := range []MuxOption{MuxOptionDefault, MuxOptionMka, MuxOptionExternal} {
		info := Info{Mux: mux}
		info.SetDupOrEmpty(true)
		if info.Mux != MuxOptionExtractOnly {
			t.Errorf("Mux = %v, want ExtractOnly after SetDupOrEmpty(true)", info.Mux)
		}
	}
	skip := Info{Mux: MuxOptionSkip}
	skip.SetDupOrEmpty(true)
	if skip.Mux != MuxOptionSkip {
		t.Errorf("Mux = %v, want Skip to be preserved", skip.Mux)
	}
	back := Info{Mux: MuxOptionDefault}
	back.SetDupOrEmpty(false)
	if back.Mux != MuxOptionDefault {
		t.Errorf("Mux = %v, want Default when cleared", back.Mux)
	}
}

func TestMediaFileAddTrackRejectsDuplicates(t *testing.T) {
	t.Parallel()
	m := NewMediaFile()
	if err := m.AddTrack(&Track{TrackType: TrackTypeVideo}); err != nil {
		t.Fatalf("first AddTrack(video) error = %v", err)
	}
	if err := m.AddTrack(&Track{TrackType: TrackTypeVideo}); !errors.Is(err, ErrMultipleVideoTracks) {
		t.Fatalf("second AddTrack(video) error = %v, want ErrMultipleVideoTracks", err)
	}
	if err := m.AddTrack(&Track{TrackType: TrackTypeChapter}); err != nil {
		t.Fatalf("first AddTrack(chapter) error = %v", err)
	}
	if err := m.AddTrack(&Track{TrackType: TrackTypeChapter}); !errors.Is(err, ErrMultipleChapterTracks) {
		t.Fatalf("second AddTrack(chapter) error = %v, want ErrMultipleChapterTracks", err)
	}
}

func TestMediaFileTracksAreOrderedForMuxing(t *testing.T) {
	t.Parallel()
	m := NewMediaFile()
	for _, tt := range []TrackType{TrackTypeChapter, TrackTypeSubtitle, TrackTypeAudio, TrackTypeVideo} {
		if err := m.AddTrack(&Track{TrackType: tt}); err != nil {
			t.Fatalf("AddTrack(%v) error = %v", tt, err)
		}
	}
	got := m.Tracks()
	want := []TrackType{TrackTypeVideo, TrackTypeAudio, TrackTypeSubtitle, TrackTypeChapter}
	if len(got) != len(want) {
		t.Fatalf("got %d tracks, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].TrackType != want[i] {
			t.Errorf("track %d = %v, want %v", i, got[i].TrackType, want[i])
		}
	}
}

func TestEnumJSONRoundTrip(t *testing.T) {
	t.Parallel()
	// Persisted state must survive a round trip through JSON, because the task
	// queue is written to disk.
	type payload struct {
		Progress TaskProgress  `json:"progress"`
		Chapter  ChapterStatus `json:"chapter"`
		RPC      RPCStatus     `json:"rpc"`
		Mux      MuxOption     `json:"mux"`
		Track    TrackType     `json:"track"`
	}
	orig := payload{
		Progress: TaskRunning,
		Chapter:  ChapterMaybe,
		RPC:      RPCFailed,
		Mux:      MuxOptionExtractOnly,
		Track:    TrackTypeSubtitle,
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var back payload
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if back != orig {
		t.Errorf("round trip = %+v, want %+v", back, orig)
	}
}

func TestRPCStatusKeepsLegacyNames(t *testing.T) {
	t.Parallel()
	// The Chinese enum names appear in the UI and in persisted state; changing
	// them would break existing installations.
	cases := map[RPCStatus]string{
		RPCWaiting: "等待中",
		RPCSkipped: "跳过",
		RPCError:   "错误",
		RPCFailed:  "未通过",
		RPCPassed:  "通过",
	}
	for status, want := range cases {
		if got := status.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", status, got, want)
		}
	}
}

func TestRPCStatusCanOpenResult(t *testing.T) {
	t.Parallel()
	if RPCWaiting.CanOpenRPCResult() {
		t.Error("waiting status should not open the result window")
	}
	if !RPCPassed.CanOpenRPCResult() || !RPCFailed.CanOpenRPCResult() {
		t.Error("finished statuses should open the result window")
	}
}

func TestMaxOrderSentinel(t *testing.T) {
	t.Parallel()
	// Info.Order defaults to MaxInt, meaning "append at the end". Profiles may
	// omit it, so the zero value must be replaced by the sentinel explicitly.
	info := NewInfo()
	if info.Order != MaxOrder {
		t.Errorf("NewInfo().Order = %d, want %d", info.Order, MaxOrder)
	}
	if info.Language != DefaultLanguage {
		t.Errorf("NewInfo().Language = %q, want %q", info.Language, DefaultLanguage)
	}
}
