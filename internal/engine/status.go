package engine

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
)

// UnknownTimeRemain is the legacy sentinel for an ETA that cannot be computed.
// The C# code stored TimeSpan.FromDays(30) whenever there was no estimate
// (CommandlineVideoEncoder.Update, TaskManager.AddTask), and the UI rendered it
// as "大于一周" like any other value beyond a week.
const UnknownTimeRemain = 30 * 24 * time.Hour

const (
	// updatesPerEstimate is the size of the ETA sliding window
	// (StatusUpdate.UpdatesPerEstimate).
	updatesPerEstimate = 10
	// minWindowGap is the minimum distance between two observations before the
	// window estimate is used (StatusUpdate.FiveSeconds).
	minWindowGap = 5 * time.Second
	// tick is the resolution of the legacy TimeSpan, and therefore of every
	// duration this package derives.
	tick = 100 * time.Nanosecond
)

// StatusUpdate accumulates the counters a running step reports and derives the
// values shown to the operator: percent done, speed and the estimated time
// remaining.
//
// It is a port of Job/StatusUpdate.cs (MeGUI's FillValues). The five progress
// sources are consulted in the legacy order — explicit percent, estimated total
// time, frame counts, file sizes, clip position — and counters that were not
// reported are back-derived from the resulting fraction. One StatusUpdate
// tracks one step and is safe for concurrent use.
//
// The C# code used decimal (28-digit base-10) arithmetic and TimeSpan's 100 ns
// tick resolution; this port uses exact rationals and quantizes derived
// durations to the same ticks, so the truncation points match the original.
type StatusUpdate struct {
	mu sync.Mutex

	taskID model.TaskID
	step   string

	// Raw inputs. A nil pointer means "never reported", the equivalent of the
	// legacy nullable properties. Unlike the C# setters, setting a value always
	// replaces it; assigning null there kept the previous value.
	percent     *big.Rat
	estTotal    *time.Duration
	framesDone  *int64
	framesTotal *int64
	currentSize *int64
	totalSize   *int64
	clipPos     *time.Duration
	clipLen     *time.Duration
	elapsed     time.Duration

	// Derived outputs, filled by Fill.
	percentExact   float64
	percentKnown   bool
	framesDoneOut  int64
	framesDoneSet  bool
	framesTotalOut int64
	framesTotalSet bool
	currentSizeOut int64
	currentSizeSet bool
	projectedSize  int64
	projectedSet   bool
	clipPosOut     time.Duration
	clipPosSet     bool
	speed          string
	estTime        time.Duration
	estKnown       bool
	bitRate        string

	// ETA sliding window, mirroring previousUpdates / previousUpdatesProgress /
	// updateIndex. Slots start at zero, like the C# arrays.
	prevUpdates  [updatesPerEstimate]time.Duration
	prevProgress [updatesPerEstimate]*big.Rat
	updateIndex  int
}

// NewStatusUpdate returns an accumulator for one step of a task.
func NewStatusUpdate(taskID model.TaskID, step string) *StatusUpdate {
	u := &StatusUpdate{taskID: taskID, step: step}
	for i := range u.prevProgress {
		u.prevProgress[i] = new(big.Rat)
	}
	return u
}

// SetPercent records an explicit completion percentage. A negative value means
// "unknown" (model.StatusEvent.Percent) and clears the source; NaN and
// infinities are ignored because the legacy decimal type could not hold them.
func (u *StatusUpdate) SetPercent(percent float64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case math.IsNaN(percent) || math.IsInf(percent, 0):
		return
	case percent < 0:
		u.percent = nil
		u.percentExact, u.percentKnown = 0, false
		return
	}
	r, _ := new(big.Rat).SetString(strconv.FormatFloat(percent, 'g', -1, 64))
	u.percent = r
	u.percentExact, u.percentKnown = percent, true
}

// SetEstimatedTime records the step's own estimate of the total run time. Zero
// is stored but never used as a progress source, exactly like the legacy
// `_timeEstimate != TimeSpan.Zero` guard.
func (u *StatusUpdate) SetEstimatedTime(total time.Duration) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.estTotal = &total
	u.estTime, u.estKnown = total, true
}

// SetFramesDone records how many frames have been processed so far.
func (u *StatusUpdate) SetFramesDone(done int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.framesDone = &done
	u.framesDoneOut, u.framesDoneSet = done, true
}

// SetFramesTotal records the total number of frames.
func (u *StatusUpdate) SetFramesTotal(total int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.framesTotal = &total
	u.framesTotalOut, u.framesTotalSet = total, true
}

// SetCurrentSize records the current output size in bytes.
func (u *StatusUpdate) SetCurrentSize(size int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.currentSize = &size
	u.currentSizeOut, u.currentSizeSet = size, true
}

// SetProjectedSize records the projected final output size in bytes.
func (u *StatusUpdate) SetProjectedSize(size int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.totalSize = &size
	u.projectedSize, u.projectedSet = size, true
}

// SetClipPosition records the position within the clip.
func (u *StatusUpdate) SetClipPosition(position time.Duration) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clipPos = &position
	u.clipPosOut, u.clipPosSet = position, true
}

// SetClipLength records the total length of the clip.
func (u *StatusUpdate) SetClipLength(length time.Duration) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clipLen = &length
}

// SetElapsed records the time elapsed since the step started.
func (u *StatusUpdate) SetElapsed(elapsed time.Duration) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.elapsed = elapsed
}

// SetBitRate records a preformatted bitrate string. It is not part of
// FillValues; the C# job classes set TaskStatus.BitRate directly.
func (u *StatusUpdate) SetBitRate(bitRate string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.bitRate = bitRate
}

// Fill recomputes the derived values from the accumulated counters. It mirrors
// StatusUpdate.FillValues, including its habit of leaving everything after a
// failed arithmetic operation untouched: the legacy code swallowed the
// exception, so the caller only saw the values computed up to that point.
func (u *StatusUpdate) Fill() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.fill()
}

func (u *StatusUpdate) fill() {
	fraction, hasFraction := u.fraction()
	if hasFraction {
		// Scale before converting, so an explicit 33.3% comes back as 33.3 and
		// not as 33.300000000000004.
		percent, _ := new(big.Rat).Mul(fraction, big.NewRat(100, 1)).Float64()
		u.percentExact, u.percentKnown = percent, true
	}

	// Frame counts. The legacy code reports the raw values first and then
	// back-derives only the counter that was not reported.
	if u.framesDone != nil {
		u.framesDoneOut, u.framesDoneSet = *u.framesDone, true
	}
	if u.framesTotal != nil {
		u.framesTotalOut, u.framesTotalSet = *u.framesTotal, true
	}
	if u.framesTotal != nil && u.framesDone == nil && hasFraction {
		done := new(big.Rat).Mul(fraction, new(big.Rat).SetInt64(*u.framesTotal))
		v, ok := truncateCount(done)
		if !ok {
			return
		}
		u.framesDoneOut, u.framesDoneSet = v, true
	}
	if u.framesTotal == nil && u.framesDone != nil && hasFraction {
		if fraction.Sign() == 0 {
			return
		}
		total := new(big.Rat).Quo(new(big.Rat).SetInt64(*u.framesDone), fraction)
		v, ok := truncateCount(total)
		if !ok {
			return
		}
		u.framesTotalOut, u.framesTotalSet = v, true
	}

	// Output size. Only the projected total is derived: the legacy code never
	// estimates the current size, so the UI never claims a measurement it does
	// not have.
	if u.currentSize != nil {
		u.currentSizeOut, u.currentSizeSet = *u.currentSize, true
	}
	if u.totalSize != nil {
		u.projectedSize, u.projectedSet = *u.totalSize, true
	}
	if u.currentSize != nil && u.totalSize == nil && hasFraction {
		if fraction.Sign() == 0 {
			return
		}
		projected := new(big.Rat).Quo(new(big.Rat).SetInt64(*u.currentSize), fraction)
		v, ok := truncateCount(projected)
		if !ok {
			return
		}
		u.projectedSize, u.projectedSet = v, true
	}

	// Clip position. Only the position is derived, never the length, so the UI
	// does not suggest a measurement it does not have.
	if u.clipPos != nil {
		u.clipPosOut, u.clipPosSet = *u.clipPos, true
	}
	if u.clipLen != nil && u.clipPos == nil && hasFraction {
		pos := new(big.Rat).Mul(big.NewRat(int64(*u.clipLen/tick), 1), fraction)
		v, ok := truncateTicks(pos)
		if !ok {
			return
		}
		u.clipPosOut, u.clipPosSet = v, true
	}

	// Speed. The legacy strings are kept verbatim, including the missing space
	// in "3x realtime". Speeds are ratios of ticks, except FPS, which divides
	// frames by elapsed seconds.
	switch {
	case u.framesDone != nil && u.elapsed > 0:
		frames := new(big.Rat).Mul(big.NewRat(*u.framesDone, 1), big.NewRat(int64(time.Second/tick), 1))
		u.speed = formatWhole(ratioFloat(frames, big.NewRat(int64(u.elapsed/tick), 1))) + " FPS"
	case u.clipPos != nil && u.elapsed > 0:
		u.speed = formatWhole(ratioFloat(big.NewRat(int64(*u.clipPos/tick), 1), big.NewRat(int64(u.elapsed/tick), 1))) + "x realtime"
	case hasFraction && u.clipLen != nil && u.elapsed > 0:
		length := new(big.Rat).Mul(big.NewRat(int64(*u.clipLen/tick), 1), fraction)
		u.speed = formatWhole(ratioFloat(length, big.NewRat(int64(u.elapsed/tick), 1))) + "x realtime"
	}

	if !hasFraction {
		return
	}
	u.fillEstimate(fraction)
}

// fillEstimate advances the sliding window and computes the remaining time.
// The window branch needs progress since the sample ten observations ago; the
// fallback extrapolates the elapsed time by the current fraction.
//
// Everything is computed in TimeSpan ticks, because that is where the legacy
// code truncated: a rational of nanoseconds would round at a different point.
func (u *StatusUpdate) fillEstimate(fraction *big.Rat) {
	slot := u.progressSlot()
	deltaTicks := int64((u.elapsed - u.prevUpdates[u.updateIndex]) / tick)
	progress := new(big.Rat).Sub(fraction, slot)

	var ticks *big.Rat
	if progress.Sign() > 0 && u.elapsed-u.prevUpdates[u.updateIndex] > minWindowGap {
		oneMinus := new(big.Rat).Sub(big.NewRat(1, 1), fraction)
		ticks = new(big.Rat).Mul(big.NewRat(deltaTicks, 1), oneMinus)
		ticks.Quo(ticks, progress)
	} else {
		if fraction.Sign() == 0 {
			// 1/fraction is a division by zero in the legacy code; the
			// swallowed exception left the window untouched.
			return
		}
		inv := new(big.Rat).Inv(fraction)
		inv.Sub(inv, big.NewRat(1, 1))
		ticks = new(big.Rat).Mul(big.NewRat(int64(u.elapsed/tick), 1), inv)
	}
	est, ok := truncateTicks(ticks)
	if !ok {
		return
	}
	u.estTime, u.estKnown = est, true

	u.prevUpdates[u.updateIndex] = u.elapsed
	slot.Set(fraction)
	u.updateIndex = (u.updateIndex + 1) % updatesPerEstimate
}

// progressSlot returns the window slot the next comparison reads, allocating it
// when the zero value was used instead of NewStatusUpdate.
func (u *StatusUpdate) progressSlot() *big.Rat {
	if u.prevProgress[u.updateIndex] == nil {
		u.prevProgress[u.updateIndex] = new(big.Rat)
	}
	return u.prevProgress[u.updateIndex]
}

// fraction resolves the completion fraction from the five legacy sources, in
// the legacy priority order: explicit percent, estimated time, frame counts,
// file sizes, clip position.
func (u *StatusUpdate) fraction() (*big.Rat, bool) {
	switch {
	case u.percent != nil:
		return new(big.Rat).Quo(u.percent, big.NewRat(100, 1)), true
	case u.estTotal != nil && *u.estTotal != 0:
		return new(big.Rat).SetFrac(
			new(big.Int).SetInt64(int64(u.elapsed/tick)),
			new(big.Int).SetInt64(int64(*u.estTotal/tick))), true
	case u.framesDone != nil && u.framesTotal != nil && *u.framesTotal != 0:
		return new(big.Rat).SetFrac(new(big.Int).SetInt64(*u.framesDone), new(big.Int).SetInt64(*u.framesTotal)), true
	case u.currentSize != nil && u.totalSize != nil && *u.totalSize != 0:
		// Integer division, exactly like the legacy ulong/ulong expression: a
		// partially written file therefore reports zero progress.
		return new(big.Rat).SetInt64(*u.currentSize / *u.totalSize), true
	case u.clipPos != nil && u.clipLen != nil && *u.clipLen != 0:
		return new(big.Rat).SetFrac(
			new(big.Int).SetInt64(int64(*u.clipPos/tick)),
			new(big.Int).SetInt64(int64(*u.clipLen/tick))), true
	}
	return nil, false
}

// Percent returns the derived completion percentage and whether it is known.
func (u *StatusUpdate) Percent() (float64, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.percentExact, u.percentKnown
}

// Speed returns the derived speed string, empty when it cannot be computed.
func (u *StatusUpdate) Speed() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.speed
}

// FramesDone returns the frame counter, derived from the fraction when the step
// did not report it.
func (u *StatusUpdate) FramesDone() (int64, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.framesDoneOut, u.framesDoneSet
}

// FramesTotal returns the total frame count, derived from the fraction when the
// step did not report it.
func (u *StatusUpdate) FramesTotal() (int64, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.framesTotalOut, u.framesTotalSet
}

// CurrentSize returns the reported output size in bytes.
func (u *StatusUpdate) CurrentSize() (int64, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.currentSizeOut, u.currentSizeSet
}

// ProjectedSize returns the output size extrapolated from the fraction, when
// the step reported a current size but no total.
func (u *StatusUpdate) ProjectedSize() (int64, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.projectedSize, u.projectedSet
}

// ClipPosition returns the position within the clip, estimated from the
// fraction when the step does not report its own position.
func (u *StatusUpdate) ClipPosition() (time.Duration, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.clipPosOut, u.clipPosSet
}

// EstimatedTime returns the derived remaining time. ok is false when no
// estimate could be computed, in which case the legacy sentinel
// UnknownTimeRemain is returned.
func (u *StatusUpdate) EstimatedTime() (time.Duration, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.estKnown {
		return UnknownTimeRemain, false
	}
	return u.estTime, true
}

// Event snapshots the derived values as a status event. The lifecycle state is
// RUNNING and an unknown percent is -1, per model.StatusEvent; callers replace
// the state for terminal events. An unknown ETA is reported as the legacy
// UnknownTimeRemain sentinel, which FormatTimeRemain renders as "大于一周".
func (u *StatusUpdate) Event() model.StatusEvent {
	u.mu.Lock()
	defer u.mu.Unlock()
	ev := model.StatusEvent{
		TaskID:   u.taskID,
		Progress: model.TaskRunning,
		Step:     u.step,
		Percent:  -1,
		Speed:    u.speed,
		BitRate:  u.bitRate,
	}
	if u.percentKnown {
		ev.Percent = u.percentExact
	}
	if u.framesDoneSet {
		ev.FramesDone = u.framesDoneOut
	}
	if u.framesTotalSet {
		ev.FramesTotal = u.framesTotalOut
	}
	remain := UnknownTimeRemain
	if u.estKnown {
		remain = u.estTime
	}
	ev.TimeRemainSeconds = remain.Seconds()
	return ev
}

// FormatTimeRemain renders a remaining time the way TaskStatus.TimeRemainStr
// does: whole hours followed by ":mm:ss", with anything beyond a week shown as
// "大于一周". That also covers the UnknownTimeRemain sentinel. The minute and
// second fields are absolute, matching the legacy TimeSpan custom format,
// while the hour field keeps the sign.
func FormatTimeRemain(d time.Duration) string {
	if d > 7*24*time.Hour {
		return "大于一周"
	}
	hours := int64(d / time.Hour)
	rest := d - time.Duration(hours)*time.Hour
	if rest < 0 {
		rest = -rest
	}
	return strconv.FormatInt(hours, 10) + ":" +
		pad2(int64(rest/time.Minute)) + ":" + pad2(int64(rest%time.Minute/time.Second))
}

// FormatPercent renders a progress value the way TaskStatus.ProgressValue does:
// a negative value means "unknown" and renders as an empty string with unknown
// set, anything else renders as "0.00%".
func FormatPercent(percent float64) (text string, unknown bool) {
	if percent < 0 {
		return "", true
	}
	return formatFixed2(percent) + "%", false
}

// pad2 renders a value below 100 as two digits.
func pad2(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

// ratioFloat returns a/b as a float64, or 0 when the ratio is not finite.
func ratioFloat(a, b *big.Rat) float64 {
	v, _ := new(big.Rat).Quo(a, b).Float64()
	return v
}

// truncateCount converts a rational to a count the way the legacy (ulong) cast
// did: truncate towards zero first, then fail when the result is negative or
// outside the model's int64 range (the legacy bound was ulong).
func truncateCount(r *big.Rat) (int64, bool) {
	n := new(big.Int).Quo(r.Num(), r.Denom())
	if n.Sign() < 0 || !n.IsInt64() {
		return 0, false
	}
	return n.Int64(), true
}

// truncateTicks converts a rational number of TimeSpan ticks to a duration the
// way the legacy (long) cast did: truncate towards zero and fail on overflow.
func truncateTicks(r *big.Rat) (time.Duration, bool) {
	n := new(big.Int).Quo(r.Num(), r.Denom())
	if !n.IsInt64() {
		return 0, false
	}
	return time.Duration(n.Int64()) * tick, true
}

// formatWhole renders v the way the legacy decimal.ToString("0") did: rounded
// half away from zero, without a fractional part.
func formatWhole(v float64) string {
	r := math.Round(v)
	if r == 0 {
		return "0"
	}
	return strconv.FormatFloat(r, 'f', 0, 64)
}

// formatFixed2 renders v the way .NET's "0.00" format did: the shortest
// round-trippable decimal is rounded half away from zero at two digits, which
// is why 99.995 prints as "100.00" and not as "99.99".
func formatFixed2(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "∞"
	case math.IsInf(v, -1):
		return "-∞"
	}

	s, neg := strings.CutPrefix(strconv.FormatFloat(v, 'f', -1, 64), "-")
	intPart, frac, _ := strings.Cut(s, ".")
	if len(frac) <= 2 {
		return sign(neg) + intPart + "." + frac + strings.Repeat("0", 2-len(frac))
	}

	digits := []byte(intPart + frac[:2])
	if frac[2] >= '5' {
		i := len(digits) - 1
		for i >= 0 && digits[i] == '9' {
			digits[i] = '0'
			i--
		}
		if i < 0 {
			digits = append([]byte{'1'}, digits...)
		} else {
			digits[i]++
		}
	}
	cut := len(intPart)
	if len(digits) > len(intPart)+2 {
		cut++
	}
	return sign(neg) + string(digits[:cut]) + "." + string(digits[cut:])
}

// sign returns the leading minus for negative values.
func sign(neg bool) string {
	if neg {
		return "-"
	}
	return ""
}
