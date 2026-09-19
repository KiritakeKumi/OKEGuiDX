// Package chapter implements the chapter side of a task: locating a chapter
// source, loading it, and normalising the result for the muxer.
//
// Behaviour is defined by Task/ChapterService.cs (281 lines) and by the parts
// of TChapter it uses. Two things changed on purpose:
//
//   - MediaInfo is gone. It was the only libmediainfo user in the whole
//     project (INVENTORY.md §5) and it only ever answered "does this Matroska
//     file carry chapters?", which ffprobe answers directly.
//   - Chapter parsing goes through the libtchapter CLI, which already ports
//     the OGM, MPLS and Matroska-XML parsers (native/tchapter). The tool path
//     is a parameter, never a constant.
package chapter

import (
	"sort"
	"time"
)

// Chapter is one chapter mark. Mirrors TChapter.Chapters.Chapter, minus the
// fields OKEGui never reads.
type Chapter struct {
	// Number is the 1-based chapter number. It is rewritten by Renumber.
	Number int `json:"number"`
	// Time is the mark's timestamp relative to the start of the source.
	Time time.Duration `json:"time"`
	// Name is the display name.
	Name string `json:"name"`
}

// Info is one title's chapter list. Mirrors TChapter.Chapters.ChapterInfo as
// far as OKEGui uses it.
type Info struct {
	// Title is the source's title, when the format carries one.
	Title string `json:"title"`
	// SourceName is the clip or file the title came from. MPLS playlists use
	// it to tell which play item belongs to which .m2ts.
	SourceName string `json:"source_name"`
	// FPSNum and FPSDen are the source frame rate as a rational. Zero means
	// the format did not report one.
	FPSNum int64 `json:"fps_num"`
	FPSDen int64 `json:"fps_den"`
	// Duration is the title's total length, when known.
	Duration time.Duration `json:"duration"`
	// Chapters are the marks, in file order until Sort is called.
	Chapters []Chapter `json:"chapters"`
}

// Count returns the number of chapters.
func (i *Info) Count() int {
	if i == nil {
		return 0
	}
	return len(i.Chapters)
}

// DurationMS returns the title duration in whole milliseconds.
func (i *Info) DurationMS() int64 { return i.Duration.Milliseconds() }

// Sort orders the chapters by timestamp. Mirrors the
// `Chapters.Sort((a, b) => a.Time.CompareTo(b.Time))` in LoadChapter, but
// stable: List<T>.Sort is not stable and reordering equal timestamps would
// make the dedup step below keep a different chapter than it used to.
func (i *Info) Sort() {
	sort.SliceStable(i.Chapters, func(a, b int) bool {
		return i.Chapters[a].Time < i.Chapters[b].Time
	})
}

// Renumber assigns Number = 1..n. Mirrors ChapterInfo.UpdateInfo(int), which
// is what drives the CHAPTERxx= numbering in the written OGM file.
func (i *Info) Renumber() {
	for k := range i.Chapters {
		i.Chapters[k].Number = k + 1
	}
}

// ChapterLabel renders the renumbering template the legacy code used,
// `string.Format("Chapter {0,2:0#}", index)`: the number right-aligned in two
// columns and zero-padded to at least two digits, so 1 is "Chapter 01" and 100
// is "Chapter 100".
func ChapterLabel(index int) string {
	return "Chapter " + pad2(index)
}

// pad2 renders n zero-padded to a minimum of two digits.
func pad2(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

// itoa is strconv.Itoa without the import, kept small because the call sites
// are all inside formatting helpers.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
