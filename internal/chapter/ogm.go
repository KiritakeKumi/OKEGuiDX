package chapter

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// OGM is the chapter format OKEGui writes for the muxer and reads back when a
// task is re-run. The shape is fixed by TChapter's OGMParser and by the
// `CHAPTERnn= / CHAPTERnnNAME=` pairs every existing .txt file on disk uses.
//
// This writer is deliberately not the tchapter CLI's `save` command: that
// command writes what the C library holds, while the engine has to write the
// *normalised* list (filtered, deduplicated, renumbered) that LoadChapter
// produced. Keeping the two in Go also means the muxer step has no extra
// process to run.
//
// Two details are inherited from TChapter.ConvertUtil.ToOGM and matter for
// byte-for-byte comparison:
//
//   - The number is `D2`, so it is zero-padded to two digits but not truncated
//     above 99 (chapter 100 writes "CHAPTER100=").
//   - The time is the `Time2String` form, hh:mm:ss.mmm, and lines end with
//     CRLF because the reference built the text with StringBuilder.AppendLine.
func (i *Info) OGM() string {
	var b strings.Builder
	for _, c := range i.Chapters {
		b.WriteString("CHAPTER")
		b.WriteString(pad2(c.Number))
		b.WriteString("=")
		b.WriteString(FormatTimestamp(c.Time))
		b.WriteString("\r\n")
		b.WriteString("CHAPTER")
		b.WriteString(pad2(c.Number))
		b.WriteString("NAME=")
		b.WriteString(c.Name)
		b.WriteString("\r\n")
	}
	return b.String()
}

// WriteOGM writes the chapter list to path. The file is UTF-8 with a BOM,
// which is what .NET's File.WriteAllText(path, text, Encoding.UTF8) produced
// and what the operators' existing chapter files look like.
func (i *Info) WriteOGM(path string) error {
	body := append([]byte{0xEF, 0xBB, 0xBF}, i.OGM()...)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return okerr.Wrap(err, okerr.KindIO, "无法写入章节文件", "%s: %v", path, err).WithFile(path)
	}
	return nil
}

// FormatTimestamp renders a duration the way TChapter's ChapterUtil.Time2String
// does: hh:mm:ss.mmm, with the hours field growing past two digits rather than
// wrapping.
//
// The reference computes the millisecond field as
// `Math.Round((TotalSeconds - Floor(TotalSeconds)) * 1000)`. Two details of
// that expression are load-bearing and are reproduced exactly:
//
//   - Math.Round(double) rounds halfway cases to the *even* neighbour, so
//     2.5 ms becomes 2 and 3.5 ms becomes 4. The measured values are in the
//     test table.
//   - The fraction can round up to 1000, which renders as four digits:
//     1.9999999 s becomes "00:00:01.1000", not "00:00:02.000".
func FormatTimestamp(d time.Duration) string {
	total := d.Seconds()
	whole := math.Floor(total)
	millis := int64(math.RoundToEven((total - whole) * 1000))

	ticks := d.Nanoseconds()
	hours := ticks / int64(time.Hour)
	minutes := (ticks / int64(time.Minute)) % 60
	seconds := (ticks / int64(time.Second)) % 60
	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, seconds, millis)
}

// qpFileLine is one entry of a qpfile: a frame number and the frame type to
// force. The legacy writer emitted "<frame> I" for every chapter mark.
const qpFileLine = " I"

// QPFile renders the qpfile body for a list of I-frame numbers, mirroring
// ChapterService.GenerateQpFile: one "<frame> I" line per entry.
func QPFile(frames []int64) string {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString(strconv.FormatInt(f, 10))
		b.WriteString(qpFileLine)
		b.WriteString("\r\n")
	}
	return b.String()
}

// QPFileAV1 renders the AV1 variant: the same entries joined by commas with the
// " I" suffix shortened to "f", which is what SVT-AV1's --qp-file accepts.
// Mirrors the branch in ExecuteTaskService.GenerateQpFile.
func QPFileAV1(frames []int64) string {
	parts := make([]string, 0, len(frames))
	for _, f := range frames {
		parts = append(parts, strconv.FormatInt(f, 10)+"f")
	}
	return strings.Join(parts, ",")
}

// WriteQPFile writes the qpfile body to path.
func WriteQPFile(path, body string) error {
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return okerr.Wrap(err, okerr.KindIO, "无法写入qpfile", "%s: %v", path, err).WithFile(path)
	}
	return nil
}
