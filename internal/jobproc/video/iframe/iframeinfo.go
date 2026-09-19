package iframe

import (
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
)

// IFrameInfo is the ordered list of I-frame numbers of an old release, plus the
// alignment helpers the re-encode slicer uses. It mirrors Model/Info/VSPipeInfo.cs,
// where IFrameInfo derived from List<long> and carried the same three methods.
//
// The list is sorted ascending and, once populated by the processor, always
// contains frame 0 and the script's frame count.
type IFrameInfo []int64

// Contains reports whether v is in the list. Mirrors List<long>.Contains.
func (f IFrameInfo) Contains(v int64) bool {
	for _, x := range f {
		if x == v {
			return true
		}
	}
	return false
}

// FindNearestLeft returns the largest element <= begin: the nearest I-frame at
// or before a slice's start. Mirrors IFrameInfo.FindNearestLeft.
//
// The legacy method was `FindLast(x => x <= begin)` and returned 0 when nothing
// matched, which is indistinguishable from a legitimate hit on frame 0. The
// second result reports a match instead, so a caller cannot silently move a
// slice to the head of the video.
func (f IFrameInfo) FindNearestLeft(begin int64) (int64, bool) {
	for i := len(f) - 1; i >= 0; i-- {
		if f[i] <= begin {
			return f[i], true
		}
	}
	return 0, false
}

// FindNearestRight returns the smallest element >= end: the nearest I-frame at
// or after a slice's end. Mirrors IFrameInfo.FindNearestRight.
//
// The legacy method was `Find(x => x >= end)` and returned 0 when nothing
// matched; the second result reports a match for the same reason as above.
func (f IFrameInfo) FindNearestRight(end int64) (int64, bool) {
	for _, x := range f {
		if x >= end {
			return x, true
		}
	}
	return 0, false
}

// FindInRangeIndex locates the run of I-frames that falls inside range and
// returns it as an INDEX range, not a frame range. Mirrors
// IFrameInfo.FindInRangeIndex, including its inclusive upper bound.
//
// The returned SliceInfo.Begin is the index of the first element in
// [range.Begin, range.End), and SliceInfo.End is the index of the last element
// of the consecutive run starting there. The second result is false when no
// element falls inside the range, which is what the legacy `return null` meant.
//
// Callers slice the list with `f[Begin : End+1]`; note that SliceInfo.Length is
// therefore one less than the number of frames selected, and that a single
// element yields Begin == End, which model.SliceInfo.IsIllegal reports as an
// illegal frame slice. That is intentional: this value is an index range.
func (f IFrameInfo) FindInRangeIndex(r model.SliceInfo) (model.SliceInfo, bool) {
	first := -1
	for i, x := range f {
		if r.Begin <= x && x < r.End {
			first = i
			break
		}
	}
	if first < 0 {
		return model.SliceInfo{}, false
	}

	last := first
	for i := first + 1; i < len(f); i++ {
		if r.Begin <= f[i] && f[i] < r.End {
			last = i
			continue
		}
		break
	}
	return model.NewSliceInfo(int64(first), int64(last)), true
}

// InRange returns the I-frames selected by FindInRangeIndex, i.e. the elements
// at indices [Begin, End]. It is the Go equivalent of the legacy
// `GetRange((int)index.begin, (int)(index.GetLength() + 1))` call.
func (f IFrameInfo) InRange(index model.SliceInfo) IFrameInfo {
	begin, end := index.Begin, index.End
	if begin < 0 || end < begin || end >= int64(len(f)) {
		return nil
	}
	return append(IFrameInfo(nil), f[begin:end+1]...)
}
