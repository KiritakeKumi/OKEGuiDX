package eac3to

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// Line patterns, taken verbatim from EACDemuxer.DetectFileTracks and readStream.
//
// eac3to's listing is a two-part document: an optional header carrying the
// container runtime, then one line per track. The header format differs between
// sources, which is why the legacy code probed the runtime with an unanchored
// `\d*:\d*:\d*` while anchoring the track lines with `^\d*?: .*$`.
var (
	// reTimecode matches an "h:mm:ss" run anywhere in a line.
	reTimecode = regexp.MustCompile(`(\d*):(\d*):(\d*)`)
	// reTrackLine matches a listing line.
	reTrackLine = regexp.MustCompile(`^\d*?: .*$`)
	// reTrackParts splits a listing line into index, description and
	// information.
	reTrackParts = regexp.MustCompile(`^(\d*?): (.*?), (.*?)$`)

	// reAnalyze and reProgress extract percentages from the extraction pass.
	reAnalyze  = regexp.MustCompile(`analyze: ([0-9]+)%`)
	reProgress = regexp.MustCompile(`process: ([0-9]+)%`)
)

// doneMarker is the lowercase needle the legacy code searched for to report
// completion: the trailing "Done." of every eac3to run.
const doneMarker = "done."

// pgsLanguageFixup mirrors the legacy workaround for PGS tracks on discs whose
// playlist carries no language. When a listing line mentions PGS and contains
// no comma at all, ", Japanese" is appended so the line still matches the
// three-part track pattern.
const pgsLanguageFixup = "Japanese"

// unknownCodecError mirrors the ArgumentException the legacy code threw when a
// listing line named a codec the table does not know.
func unknownCodecError(line string) error {
	return okerr.New(okerr.KindConfig, "不明类型", "不明类型: %s", line).WithOutput(line)
}

// Detector incrementally parses eac3to output. The legacy EACDemuxer parsed the
// listing during the analysis pass and only progress lines during the extraction
// pass, which is exactly how a Detector is used here.
type Detector struct {
	sourceFile        string
	workingPathPrefix string
	tracks            []*TrackInfo
	length            int
}

// NewDetector returns a Detector for one source file.
func NewDetector(sourceFile, workingPathPrefix string) *Detector {
	return &Detector{sourceFile: sourceFile, workingPathPrefix: workingPathPrefix}
}

// Tracks returns the tracks recognised so far, in listing order.
func (d *Detector) Tracks() []*TrackInfo { return d.tracks }

// Length returns the runtime of the last header line seen, in whole seconds.
func (d *Detector) Length() int { return d.length }

// Feed parses one raw output line.
func (d *Detector) Feed(line string) error {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	if m := reTimecode.FindStringSubmatch(line); m != nil {
		// The legacy code used int.Parse on each component. A missing
		// component counts as zero, matching `\d*` matching the empty string;
		// only a non-numeric field is an error.
		hour, err := parseTimePart(m[1])
		if err != nil {
			return err
		}
		minute, err := parseTimePart(m[2])
		if err != nil {
			return err
		}
		second, err := parseTimePart(m[3])
		if err != nil {
			return err
		}
		d.length = hour*3600 + minute*60 + second
	}
	if !reTrackLine.MatchString(line) {
		return nil
	}
	return d.addTrack(line)
}

// FeedAll parses a whole listing and returns the tracks it contains. It is the
// equivalent of the analysis pass.
func (d *Detector) FeedAll(listing string) error {
	for _, line := range strings.Split(listing, "\n") {
		if err := d.Feed(strings.TrimSuffix(line, "\r")); err != nil {
			return err
		}
	}
	return nil
}

// addTrack is DetectFileTracks' tail: the PGS fixup, the three-part split and
// the unknown-codec rejection.
func (d *Detector) addTrack(line string) error {
	if strings.Contains(line, "PGS") && !strings.Contains(line, ",") {
		line += ", " + pgsLanguageFixup
	}
	m := reTrackParts.FindStringSubmatch(line)
	if m == nil {
		// Fewer than the three groups the legacy code required: not a track.
		return nil
	}
	index, err := strconv.Atoi(m[1])
	if err != nil {
		return err
	}
	ot, ok := lookupOutput(m[2])
	if !ok {
		return unknownCodecError(line)
	}
	d.tracks = append(d.tracks, &TrackInfo{
		Codec:             ot.Codec,
		Index:             index,
		Information:       strings.TrimSpace(m[3]),
		RawOutput:         line,
		SourceFile:        d.sourceFile,
		WorkingPathPrefix: d.workingPathPrefix,
		Type:              ot.Type,
		Length:            d.length,
	})
	return nil
}

// parseTimePart mirrors int.Parse on one component of an h:mm:ss run, where a
// missing component counts as zero.
func parseTimePart(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.Atoi(s)
}

// TrackProgress is one progress update from the extraction pass.
type TrackProgress struct {
	// Percent is the value eac3to printed, 0..100.
	Percent float64
	// Analyze is true for "analyze: N%" lines, false for "process: N%".
	Analyze bool
	// Completed is true for the final "Done." line.
	Completed bool
}

// ParseProgress extracts a progress update from one extraction-pass line.
// ok is false when the line carried none.
//
// The legacy code reported every parsed value in the analysis pass, but only
// values above 1% during extraction; that filter is applied here, so a 0% or 1%
// update counts as "no progress".
func ParseProgress(line string) (TrackProgress, bool) {
	if m := reAnalyze.FindStringSubmatch(line); m != nil {
		if p, err := strconv.ParseFloat(m[1], 64); err == nil && p > 1 {
			return TrackProgress{Percent: p, Analyze: true}, true
		}
	}
	if m := reProgress.FindStringSubmatch(line); m != nil {
		if p, err := strconv.ParseFloat(m[1], 64); err == nil && p > 1 {
			return TrackProgress{Percent: p}, true
		}
	}
	if strings.Contains(strings.ToLower(line), doneMarker) {
		return TrackProgress{Percent: 100, Completed: true}, true
	}
	return TrackProgress{}, false
}
