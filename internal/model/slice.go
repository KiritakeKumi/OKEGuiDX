package model

import "errors"

// Errors returned by the model package. They are sentinel values so that
// callers can use errors.Is.
var (
	ErrMultipleVideoTracks   = errors.New("model: multiple video tracks are being added")
	ErrMultipleChapterTracks = errors.New("model: multiple chapter tracks are being added")
	ErrUnknownTrackType      = errors.New("model: unknown track type")
	ErrInvalidSlice          = errors.New("model: invalid slice")
	ErrSlicesOverlap         = errors.New("model: slices overlap")
)

// SliceInfo is a half-open frame range [Begin, End). End == -1 means "until the
// end of the video", matching the profile format.
type SliceInfo struct {
	Begin int64 `json:"begin"`
	End   int64 `json:"end"`
}

// OpenEnded is the sentinel End value meaning "to the end of the video".
const OpenEnded int64 = -1

// NewSliceInfo returns a slice with the given bounds.
func NewSliceInfo(begin, end int64) SliceInfo { return SliceInfo{Begin: begin, End: end} }

// IsIllegal reports whether the slice bounds are invalid. Mirrors
// SliceInfo.CheckIllegal.
func (s SliceInfo) IsIllegal() bool {
	if s.Begin < 0 {
		return true
	}
	if s.End == OpenEnded {
		return false
	}
	return s.End < 0 || s.Begin >= s.End
}

// Length returns End-Begin, or -1 for an open-ended slice.
func (s SliceInfo) Length() int64 {
	if s.End == OpenEnded {
		return OpenEnded
	}
	return s.End - s.Begin
}

// String implements fmt.Stringer using the legacy format.
func (s SliceInfo) String() string {
	return "[" + itoa(s.Begin) + ", " + itoa(s.End) + "]"
}

// Compare orders slices by Begin. Mirrors SliceInfo.CompareTo.
func (s SliceInfo) Compare(other SliceInfo) int {
	if s.Begin >= other.Begin {
		return 1
	}
	return -1
}

// CheckAndMerge validates that sorted slices do not overlap and merges
// contiguous ones. It returns ErrSlicesOverlap when two slices overlap.
//
// The input must already be sorted by Begin and each slice must be legal.
// Mirrors SliceInfoArray.CheckAndMerge.
func CheckAndMerge(slices []SliceInfo) ([]SliceInfo, error) {
	if len(slices) == 0 {
		return nil, nil
	}
	work := make([]SliceInfo, len(slices))
	copy(work, slices)
	for i := range work {
		if work[i].End == OpenEnded {
			work[i].End = maxInt64
		}
	}

	out := make([]SliceInfo, 0, len(work))
	prev := work[0]
	for i := 1; i < len(work); i++ {
		switch {
		case work[i].Begin < prev.End:
			return nil, ErrSlicesOverlap
		case work[i].Begin == prev.End:
			prev.End = work[i].End
		default:
			out = append(out, prev)
			prev = work[i]
		}
	}
	out = append(out, prev)

	for i := range out {
		if out[i].End == maxInt64 {
			out[i].End = OpenEnded
		}
	}
	return out, nil
}

// MergeSlices merges contiguous or overlapping slices. The input must be sorted
// by Begin. Mirrors SliceInfoArray.Merge.
func MergeSlices(slices []SliceInfo) []SliceInfo {
	if len(slices) == 0 {
		return nil
	}
	out := make([]SliceInfo, 0, len(slices))
	prev := slices[0]
	for i := 1; i < len(slices); i++ {
		if slices[i].Begin <= prev.End {
			prev.End = slices[i].End
			continue
		}
		out = append(out, prev)
		prev = slices[i]
	}
	return append(out, prev)
}

const maxInt64 = int64(^uint64(0) >> 1)

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	var buf [21]byte
	i := len(buf)
	// The magnitude is computed in unsigned arithmetic so that math.MinInt64,
	// whose negation overflows int64, is handled correctly.
	u := uint64(v) //nolint:gosec // the two's-complement reinterpretation is intentional here
	if neg {
		u = -u
	}
	for u > 0 {
		i--
		buf[i] = byte('0' + u%10)
		u /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
