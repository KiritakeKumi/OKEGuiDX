package engine

import (
	"bufio"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// This file is the port of Utils/Timecode.cs. INVENTORY.md maps it to
// internal/model/timecode.go, but that package is frozen and holds no
// filesystem access by contract, so the port lives next to its only caller.
//
// The format is the one VFR tooling has used since the MKVToolNix days: v1 names
// a default rate plus explicit frame ranges, v2 lists one timestamp per frame.
// Both are accepted; the pipeline writes v2, because that is what mkvmerge's
// --timestamps wants and what the legacy code saved.

// timecodeHeader matches the first line of both formats. It is the C#
// `\A# time(?:code|stamp) format (v[1-2])`.
var timecodeHeader = regexp.MustCompile(`^# time(?:code|stamp) format (v[1-2])`)

// ticksPerSecond is TimeSpan.TicksPerSecond. The legacy code expressed every
// interval in 100 ns ticks, and the arithmetic below inherits that unit so the
// rounding lands where the original's did.
const ticksPerSecond = 1e7

// timecodeLine matches a v1 range line: `start,end,rate`.
var timecodeLine = regexp.MustCompile(`^(\d+)\s*,\s*(\d+)\s*,\s*(\d+(?:\.\d*)?)$`)

// rangeInterval is one constant-rate run of frames. Interval is in ticks per
// frame, matching RangeInterval in the legacy file.
type rangeInterval struct {
	startFrame int
	endFrame   int
	interval   float64
}

// lengthFrames is the number of frames the interval covers.
func (r rangeInterval) lengthFrames() int { return r.endFrame - r.startFrame + 1 }

// Timecode is a parsed timecode file plus the interval list it normalised to.
type Timecode struct {
	intervals []rangeInterval
}

// LoadTimecode reads a v1 or v2 timecode file.
//
// frames is the script's frame count and only matters for v1: a v1 file
// describes its exceptions and leaves the rest to the "assume" rate, so the
// trailing frames have to be filled in. The legacy constructor defaulted it to
// zero, which made the filler interval end at frame -1.
func LoadTimecode(path string, frames int64) (*Timecode, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, okerr.Wrap(err, okerr.KindNotFound, "找不到timecode文件",
			"%s 不存在或无法读取", path).WithFile(path)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	if !sc.Scan() {
		return nil, okerr.New(okerr.KindConfig, "无法确定timecode版本",
			"%s 是空文件", path).WithFile(path)
	}

	m := timecodeHeader.FindStringSubmatch(strings.TrimSpace(sc.Text()))
	if m == nil {
		return nil, okerr.New(okerr.KindConfig, "无法确定timecode版本",
			"%s 的首行不是 timecode 头", path).WithFile(path)
	}

	tc := &Timecode{}
	switch m[1] {
	case "v1":
		if err := tc.readV1(sc, frames); err != nil {
			return nil, err
		}
	default:
		if err := tc.readV2(sc); err != nil {
			return nil, err
		}
	}
	tc.normalise()
	return tc, nil
}

// TotalLength is the timecode's total runtime, mirroring Timecode.TotalLength:
// the sum of every interval's contribution, truncated to whole ticks.
//
// The intervals are in TimeSpan ticks (100 ns) because that is the unit the
// legacy arithmetic used; a time.Duration is in nanoseconds, so the sum is
// scaled on the way out.
func (t *Timecode) TotalLength() time.Duration {
	var sum float64
	for _, iv := range t.intervals {
		sum += iv.interval * float64(iv.lengthFrames())
	}
	return time.Duration(int64(sum)) * 100
}

// TotalFrames is the last frame number plus one, mirroring Timecode.TotalFrames.
func (t *Timecode) TotalFrames() int {
	if len(t.intervals) == 0 {
		return 0
	}
	return t.intervals[len(t.intervals)-1].endFrame + 1
}

// FrameNumberFromTime returns the frame a timestamp belongs to. Mirrors
// Timecode.GetFrameNumberFromTimeSpan.
//
// The loop accumulates each interval's total duration and, at the first
// interval that reaches the timestamp, counts backwards from its end. The final
// fallback returns the last frame, which is what the original did for a
// timestamp beyond the file.
func (t *Timecode) FrameNumberFromTime(ts time.Duration) int64 {
	tick := float64(ts.Nanoseconds()) / 100.0
	var accumulated float64
	for _, iv := range t.intervals {
		accumulated += iv.interval * float64(iv.lengthFrames())
		if accumulated < tick {
			continue
		}
		deltaFrame := int(math.Round((accumulated - tick) / iv.interval))
		return int64(iv.endFrame - deltaFrame + 1)
	}
	if len(t.intervals) == 0 {
		return 0
	}
	return int64(t.intervals[len(t.intervals)-1].endFrame)
}

// SaveTimecode writes the v2 form. The legacy signature took a version; only v2
// was ever requested (the default argument), and only v2 is written here.
//
// The file is created fresh: FileMode.CreateNew in the original meant an
// existing file was an error, and the caller relied on that to avoid clobbering
// a v2 file it had not produced.
func (t *Timecode) SaveTimecode(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return okerr.Wrap(err, okerr.KindIO, "无法写入timecode文件", "%s: %v", path, err).WithFile(path)
	}
	defer func() { _ = f.Close() }()

	w := bufio.NewWriter(f)
	if _, err := w.WriteString("# timecode format v2\r\n"); err != nil {
		return okerr.Wrap(err, okerr.KindIO, "无法写入timecode文件", "%s: %v", path, err).WithFile(path)
	}
	frame := 0
	var elapsed float64
	for _, iv := range t.intervals {
		for frame <= iv.endFrame {
			// (time / 1e4).ToString("F6"): the timestamp is in milliseconds,
			// and elapsed counts ticks, so the division is 1e4.
			if _, err := w.WriteString(strconv.FormatFloat(elapsed/1e4, 'f', 6, 64)); err != nil {
				return okerr.Wrap(err, okerr.KindIO, "无法写入timecode文件", "%s: %v", path, err).WithFile(path)
			}
			if err := w.WriteByte('\n'); err != nil {
				return okerr.Wrap(err, okerr.KindIO, "无法写入timecode文件", "%s: %v", path, err).WithFile(path)
			}
			elapsed += iv.interval
			frame++
		}
	}
	if err := w.Flush(); err != nil {
		return okerr.Wrap(err, okerr.KindIO, "无法写入timecode文件", "%s: %v", path, err).WithFile(path)
	}
	return nil
}

// readV1 handles TimecodeV1Handler: an "assume" rate followed by explicit
// ranges, with the gaps between ranges filled at the assumed rate.
func (t *Timecode) readV1(sc *bufio.Scanner, frames int64) error {
	defaultInterval := 0.0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(line), "assume") {
			continue
		}
		rate, err := strconv.ParseFloat(strings.TrimSpace(line[6:]), 64)
		if err != nil || rate == 0 {
			return okerr.New(okerr.KindConfig, "timecode格式错误",
				"assume 行的帧率无法解析: %s", line)
		}
		defaultInterval = ticksPerSecond / rate
		break
	}
	if defaultInterval == 0 {
		return okerr.New(okerr.KindConfig, "timecode格式错误", "找不到默认帧率（assume 行）")
	}

	var intervals []rangeInterval
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := timecodeLine.FindStringSubmatch(line)
		if m == nil {
			return okerr.New(okerr.KindConfig, "timecode格式错误",
				"内容行格式错误: %s", line)
		}
		start, _ := strconv.Atoi(m[1])
		end, _ := strconv.Atoi(m[2])
		rate, err := strconv.ParseFloat(m[3], 64)
		if err != nil || rate == 0 {
			return okerr.New(okerr.KindConfig, "timecode格式错误",
				"内容行的帧率无法解析: %s", line)
		}
		if start > end {
			return okerr.New(okerr.KindConfig, "timecode格式错误",
				"起始帧 %d 大于结束帧 %d", start, end)
		}
		intervals = append(intervals, rangeInterval{start, end, ticksPerSecond / rate})
	}

	sort.SliceStable(intervals, func(i, j int) bool { return intervals[i].startFrame < intervals[j].startFrame })
	lastEnd := -1
	for _, iv := range intervals {
		if iv.startFrame <= lastEnd {
			return okerr.New(okerr.KindConfig, "timecode格式错误",
				"帧区间重叠: -%d 与 %d-%d", lastEnd, iv.startFrame, iv.endFrame)
		}
		if iv.startFrame-lastEnd > 1 {
			t.intervals = append(t.intervals, rangeInterval{lastEnd + 1, iv.startFrame - 1, defaultInterval})
		}
		t.intervals = append(t.intervals, iv)
		lastEnd = iv.endFrame
	}

	if int(frames) > t.TotalFrames() {
		t.intervals = append(t.intervals, rangeInterval{t.TotalFrames(), int(frames) - 1, defaultInterval})
	}
	return nil
}

// readV2 handles TimecodeV2Handler: one timestamp per frame, grouped into runs
// of equal spacing.
//
// Two oddities are load-bearing and reproduced: a repeated timestamp is only an
// error when it is not the very first frame, and the very first frame's
// timestamp seeds `lastTime` without contributing an interval.
func (t *Timecode) readV2(sc *bufio.Scanner) error {
	lastTime := -1.0
	lastDiff := 0.0
	firstTime := 0.0
	firstFrame := 0
	currentFrame := -1

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		currentTime, err := strconv.ParseFloat(line, 64)
		if err != nil {
			return okerr.New(okerr.KindConfig, "timecode格式错误",
				"时间戳无法解析: %s", line)
		}
		currentFrame++
		if lastTime == -1 {
			lastTime = currentTime
			continue
		}
		if currentTime == lastTime {
			if currentFrame != 1 {
				return okerr.New(okerr.KindConfig, "timecode格式错误",
					"帧重叠: 第 %d 帧与第 %d 帧", currentFrame-1, currentFrame)
			}
			lastTime = currentTime
			continue
		}
		if math.Abs(currentTime-lastTime-lastDiff) < 1e-3 || lastDiff == 0 {
			lastDiff = currentTime - lastTime
			lastTime = currentTime
			continue
		}
		t.intervals = append(t.intervals, rangeInterval{
			firstFrame,
			currentFrame - 2,
			1e4 * (lastTime - firstTime) / float64(currentFrame-firstFrame-1),
		})
		firstFrame = currentFrame - 1
		firstTime = lastTime
		lastDiff = currentTime - lastTime
		lastTime = currentTime
	}
	if currentFrame < 0 {
		return nil
	}
	if currentFrame == firstFrame {
		// A one-frame file has no spacing to measure; the original divided by
		// zero here and produced NaN, which every later consumer truncated to
		// zero. Leaving the list empty is the same outcome without the NaN.
		return nil
	}
	t.intervals = append(t.intervals, rangeInterval{
		firstFrame,
		currentFrame,
		1e4 * (lastTime - firstTime) / float64(currentFrame-firstFrame),
	})
	return nil
}

// normalise snaps an interval that is within a millionth of a whole NTSC or
// integer rate back onto that rate, mirroring Timecode.NormalizeInterval.
//
// Without it, a rate written as 23.976024 would produce a slightly different
// tick count than 24000/1001, and the frame numbers derived from it would drift
// by one over a long episode.
func (t *Timecode) normalise() {
	for i := range t.intervals {
		iv := &t.intervals[i]
		if iv.interval == 0 {
			continue
		}
		if v := 1001e4 / iv.interval; math.Abs(math.Round(v)-v) < 1e-6 {
			iv.interval = 1001e4 / math.Round(v)
			continue
		}
		if v := 1000e4 / iv.interval; math.Abs(math.Round(v)-v) < 1e-6 {
			iv.interval = 1000e4 / math.Round(v)
		}
	}
}
