package engine

import (
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/video/iframe"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/video/svtav1"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/video/x264"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/video/x265"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// checkReEncodeSlices is CheckReEncodeSlice: turn the profile's requested frame
// ranges into ranges that start and end on the old release's I-frames.
//
// The reasoning is the point of the whole re-encode feature. A re-encoded slice
// is spliced back into a copy of the old release, and a Matroska splice can only
// happen at a key frame, so every boundary has to move to the nearest I-frame
// outside the requested range: the start moves left, the end moves right. Two
// slices that end up touching are merged, because a gap of zero frames is not a
// gap and the part layout would otherwise produce an empty part.
//
// The validation steps are the legacy ones:
//
//   - a slice that starts at or beyond the old release's last frame, or ends
//     beyond it, is Constants.reEncodeSliceErrorSmr;
//   - an open-ended slice ends at that last frame;
//   - a boundary with no I-frame at or before/after it is an error, because the
//     legacy FindNearestLeft/Right returned 0 there and silently moved the slice
//     to the head of the video.
//
// The last point is where the Go port deliberately differs: IFrameInfo returns
// (value, ok) instead of a 0 sentinel (DECISIONS-NEEDED.md B4). A missing
// I-frame is impossible for a well-formed index, because the processor brackets
// the list with frame 0 and the script's frame count; reporting it is strictly
// better than silently producing a wrong splice point.
func checkReEncodeSlices(slices []model.SliceInfo, iFrames iframe.IFrameInfo, input string) ([]model.SliceInfo, error) {
	if len(iFrames) == 0 {
		return nil, okerr.New(okerr.KindConfig, "找不到I帧序列",
			"ReEncode 需要旧版成品的 I 帧序列，但旧版成品没有可用的 I 帧列表").WithFile(input)
	}
	// The legacy bound is the last element of the list, which the processor
	// always sets to the script's frame count.
	numFrames := iFrames[len(iFrames)-1]

	out := make([]model.SliceInfo, 0, len(slices))
	for _, s := range slices {
		if s.Begin >= numFrames || s.End > numFrames {
			return nil, okerr.New(okerr.KindMismatch, okerr.ErrReEncodeSlice.Summary,
				"切片%s不合法，视频长度为%d", s, numFrames).WithFile(input)
		}
		if s.End == model.OpenEnded {
			s.End = numFrames
		}
		left, ok := iFrames.FindNearestLeft(s.Begin)
		if !ok {
			return nil, okerr.New(okerr.KindMismatch, okerr.ErrReEncodeSlice.Summary,
				"切片%s的起始位置之前没有 I 帧", s).WithFile(input)
		}
		right, ok := iFrames.FindNearestRight(s.End)
		if !ok {
			return nil, okerr.New(okerr.KindMismatch, okerr.ErrReEncodeSlice.Summary,
				"切片%s的结束位置之后没有 I 帧", s).WithFile(input)
		}
		out = append(out, model.NewSliceInfo(left, right))
	}
	return model.MergeSlices(out), nil
}

// encodeSpec is one video encode's parameters. It is the pipeline-side mirror of
// video.EncodeSpec, kept separate because the pipeline fills it from the run
// state while an encoder only sees its own slice of it.
type encodeSpec struct {
	vspipe      string
	script      string
	vspipeArgs  []string
	encoder     string
	params      string
	output      string
	totalFrames int64
	frameStart  int64
	frameEnd    int64
	asm         string
}

// newEncoder builds the processor for the profile's encoder. It mirrors the
// switch in DoAllJobs' VideoJob branch.
func (st *runState) newEncoder(spec encodeSpec) jobproc.Processor {
	switch profile.EncoderType(st.p.EncoderType) {
	case profile.EncoderX264:
		return x264.New(x264.Options{
			VSPipe:      spec.vspipe,
			Script:      spec.script,
			VSPipeArgs:  spec.vspipeArgs,
			Encoder:     spec.encoder,
			Params:      spec.params,
			Asm:         spec.asm,
			Output:      spec.output,
			TotalFrames: spec.totalFrames,
			FrameStart:  spec.frameStart,
			FrameEnd:    spec.frameEnd,
		})
	case profile.EncoderSVTAV1:
		return svtav1.New(svtav1.Options{
			VSPipe:      spec.vspipe,
			Script:      spec.script,
			VSPipeArgs:  spec.vspipeArgs,
			Encoder:     spec.encoder,
			Params:      spec.params,
			Output:      spec.output,
			TotalFrames: spec.totalFrames,
			FrameStart:  spec.frameStart,
			FrameEnd:    spec.frameEnd,
		})
	default:
		return x265.New(x265.Options{
			VSPipe:      spec.vspipe,
			Script:      spec.script,
			VSPipeArgs:  spec.vspipeArgs,
			Encoder:     spec.encoder,
			Params:      spec.params,
			Asm:         spec.asm,
			Output:      spec.output,
			TotalFrames: spec.totalFrames,
			FrameStart:  spec.frameStart,
			FrameEnd:    spec.frameEnd,
		})
	}
}

// encoderParams assembles the profile's encoder parameters plus the pipeline's
// own additions. Mirrors GenerateVideoJob:415-455:
//
//   - x265 gets `--pools <mask>` unless the profile already names --pools;
//   - x264 gets `--threads 16` on a machine with more than 10 cores unless the
//     profile already names --threads;
//   - SVT-AV1 gets `--lp 8` on a machine with more than 8 cores unless the
//     profile already names --lp;
//   - the qpfile option is appended last, spelled per codec.
//
// The legacy checks were case-insensitive substring tests on the raw parameter
// string, which is reproduced. The parameter string is passed through unchanged
// otherwise: the wrappers tokenise it themselves, which is what stands in for
// the cmd.exe splitting the original relied on.
func (st *runState) encoderParams(p part, numa int) string {
	params := st.p.EncoderParam
	lower := lowerASCII(params)

	switch st.p.VideoFormat {
	case "HEVC":
		if !contains(lower, "--pools") {
			params += " --pools " + numaPools(st.opts.Numa, numa)
		}
	case "AVC":
		if !contains(lower, "--threads") && platform.UsableCoreCount() > 10 {
			params += " --threads 16"
		}
	case "AV1":
		if !contains(lower, "--lp") && platform.UsableCoreCount() > 8 {
			params += " --lp 8"
		}
	}

	qp := p.qpValue
	if qp == "" {
		qp = st.qpValue
	}
	if qp == "" {
		return params
	}
	if st.p.VideoFormat == "AV1" {
		return params + ` --force-key-frames "` + qp + `"`
	}
	return params + ` --qpfile "` + qp + `"`
}

// numaPools renders the x265 --pools mask for a node, mirroring
// NumaNode.X265PoolsParam. An out-of-range node yields the single-node mask,
// which is what the legacy IndexOutOfRange would have produced had it been
// caught: x265's own default is one pool on every node.
func numaPools(numa *platform.Numa, node int) string {
	if numa == nil {
		return "+"
	}
	if mask := numa.X265PoolsParam(node); mask != "" {
		return mask
	}
	return "+"
}

// contains is the ordinal, case-insensitive substring test the legacy
// `EncoderParam.ToLower().Contains(...)` performed. The caller passes an
// already-lowercased haystack.
func contains(haystack, needle string) bool {
	return indexOf(haystack, needle) >= 0
}

// indexOf is strings.Index without the import at every call site.
func indexOf(haystack, needle string) int {
	n, m := len(haystack), len(needle)
	if m == 0 {
		return 0
	}
	for i := 0; i+m <= n; i++ {
		if haystack[i:i+m] == needle {
			return i
		}
	}
	return -1
}

// partStatus renders the operator-facing status of a part of a re-encode,
// mirroring `$"Part {PartId + 1}/{total} 压制中"` and
// `$"Part {PartId + 1}/{total} 封装中"`.
//
// The number is the part's own id plus one, which is what makes the display agree
// with the `_partN` file names rather than with the position in the layout: the
// two differ whenever the first slice does not start at frame zero, because the
// leading copy is part 0.
func partStatus(p part, total int, verb string) string {
	if total <= 1 {
		return verb
	}
	return "Part " + itoa(p.id+1) + "/" + itoa(total) + " " + verb
}
