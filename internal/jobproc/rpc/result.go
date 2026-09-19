package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// ErrResultParse reports a .rpc file that could not be decoded.
var ErrResultParse = &okerr.Error{Kind: okerr.KindUnknown, Summary: "RPC结果解析失败"}

// FileNamePair is the legacy `(string src, string opt)` tuple: the task script
// and the ripped file the samples belong to.
type FileNamePair struct {
	Src string
	Opt string
}

// Logs mirrors the legacy LogBuffer. Only Inf is ever written.
type Logs struct {
	Inf bool `json:"Inf"`
}

// Sample is one RPCOUT measurement.
//
// A Y-only run leaves ValueU and ValueV at PSNRUVThreshold, which is exactly
// what the legacy code compared them against, so the fields stay meaningful
// even when the script never reported them.
type Sample struct {
	Index  int
	Value  float64
	ValueU float64
	ValueV float64
}

// Result is the decoded content of one .rpc file.
type Result struct {
	// Samples are the per-frame PSNR values in the order the script printed
	// them.
	Samples []Sample
	// FileNamePair is nil when the legacy writer never assigned it, which is
	// the case for every four-field result.
	FileNamePair *FileNamePair
	// Logs mirrors the legacy LogBuffer.
	Logs Logs
	// YUV reports whether the file used the four-field RpcResult3 layout.
	YUV bool
}

// wirePair is Newtonsoft's rendering of a ValueTuple: Item1/Item2 members
// rather than a JSON array. Pointers keep the legacy "null" members that were
// written when the pair had never been assigned.
//
// The decoder also accepts the two-element array form, which is what the
// RPChecker viewer's own writer produces for its ResultV1 fallback.
type wirePair struct {
	Item1 *string `json:"Item1"`
	Item2 *string `json:"Item2"`
}

// UnmarshalJSON implements json.Unmarshaler.
func (w *wirePair) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		*w = wirePair{}
		return nil
	}
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var elems []*string
		if err := json.Unmarshal(trimmed, &elems); err != nil {
			return fmt.Errorf("解析 FileNamePair 数组失败: %w", err)
		}
		*w = wirePair{}
		if len(elems) > 0 {
			w.Item1 = elems[0]
		}
		if len(elems) > 1 {
			w.Item2 = elems[1]
		}
		return nil
	}
	// The alias avoids recursing into this method.
	type plain wirePair
	var p plain
	if err := json.Unmarshal(trimmed, &p); err != nil {
		return fmt.Errorf("解析 FileNamePair 失败: %w", err)
	}
	*w = wirePair(p)
	return nil
}

func (w wirePair) toPair() *FileNamePair {
	if w.Item1 == nil && w.Item2 == nil {
		return nil
	}
	pair := &FileNamePair{}
	if w.Item1 != nil {
		pair.Src = *w.Item1
	}
	if w.Item2 != nil {
		pair.Opt = *w.Item2
	}
	return pair
}

func pairToWire(p *FileNamePair) wirePair {
	if p == nil {
		return wirePair{}
	}
	return wirePair{Item1: &p.Src, Item2: &p.Opt}
}

// wireSample4 is Newtonsoft's rendering of
// `(int index, double value, double valueU, double valueV)`. It also decodes
// the shorter two-field tuple, because a reader cannot know which shape a file
// uses until it has looked.
type wireSample4 struct {
	Item1 *float64 `json:"Item1"`
	Item2 *float64 `json:"Item2"`
	Item3 *float64 `json:"Item3"`
	Item4 *float64 `json:"Item4"`
}

// wireSample2 is Newtonsoft's rendering of `(int index, double value)`.
type wireSample2 struct {
	Item1 *float64 `json:"Item1"`
	Item2 *float64 `json:"Item2"`
}

// decodeSample accepts one tuple in either the Newtonsoft object form or the
// array form `[index, value, ...]`. The second result reports whether the
// tuple carried chroma values.
func decodeSample(raw json.RawMessage) (Sample, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var elems []float64
		if err := json.Unmarshal(trimmed, &elems); err != nil {
			return Sample{}, false, fmt.Errorf("解析样本数组失败: %w", err)
		}
		if len(elems) < 2 {
			return Sample{}, false, fmt.Errorf("样本数组只有 %d 个元素", len(elems))
		}
		s := Sample{Index: int(elems[0]), Value: elems[1], ValueU: PSNRUVThreshold, ValueV: PSNRUVThreshold}
		if len(elems) > 3 {
			s.ValueU, s.ValueV = elems[2], elems[3]
			return s, true, nil
		}
		return s, false, nil
	}

	var w wireSample4
	if err := json.Unmarshal(trimmed, &w); err != nil {
		return Sample{}, false, fmt.Errorf("解析样本失败: %w", err)
	}
	if w.Item1 == nil || w.Item2 == nil {
		return Sample{}, false, fmt.Errorf("样本缺少 Item1/Item2")
	}
	s := Sample{Index: int(*w.Item1), Value: *w.Item2, ValueU: PSNRUVThreshold, ValueV: PSNRUVThreshold}
	if w.Item3 != nil && w.Item4 != nil {
		s.ValueU, s.ValueV = *w.Item3, *w.Item4
		return s, true, nil
	}
	return s, false, nil
}

// samplesField decodes the Data array of either result shape and remembers
// which shape it saw.
type samplesField struct {
	samples []Sample
	yuv     bool
}

// UnmarshalJSON implements json.Unmarshaler.
func (f *samplesField) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("解析 Data 失败: %w", err)
	}
	f.samples = make([]Sample, 0, len(raw))
	for _, item := range raw {
		s, isYUV, err := decodeSample(item)
		if err != nil {
			return err
		}
		f.samples = append(f.samples, s)
		if isYUV {
			f.yuv = true
		}
	}
	return nil
}

// rpcFile is the on-disk envelope RpChecker wrote: a one-element array holding
// one result object. It is the shape used for decoding, where the number of
// tuple members is not known up front.
type rpcFile struct {
	Data         samplesField `json:"Data"`
	FileNamePair wirePair     `json:"FileNamePair"`
	Logs         Logs         `json:"Logs"`
}

// rpcFile2 is the two-field encoding used for a Y-only run.
type rpcFile2 struct {
	Data         []wireSample2 `json:"Data"`
	FileNamePair wirePair      `json:"FileNamePair"`
	Logs         Logs          `json:"Logs"`
}

// rpcFile3 is the four-field encoding used for a YUV run.
type rpcFile3 struct {
	Data         []wireSample4 `json:"Data"`
	FileNamePair wirePair      `json:"FileNamePair"`
	Logs         Logs          `json:"Logs"`
}

// ParseResult decodes a .rpc file. Both legacy shapes are accepted: the
// two-field RpcResult and the four-field RpcResult3. So is the array-of-arrays
// form, which is what the RPChecker viewer's own writer emits.
func ParseResult(data []byte) (*Result, error) {
	var files []rpcFile
	if err := json.Unmarshal(data, &files); err != nil {
		return nil, okerr.Wrap(err, ErrResultParse.Kind, ErrResultParse.Summary, "%v", err)
	}
	if len(files) == 0 {
		return nil, okerr.New(ErrResultParse.Kind, ErrResultParse.Summary, "结果文件是空数组")
	}
	first := files[0]
	return &Result{
		Samples:      first.Data.samples,
		FileNamePair: first.FileNamePair.toPair(),
		Logs:         first.Logs,
		YUV:          first.Data.yuv,
	}, nil
}

// EncodeResult renders a result in the legacy on-disk shape: a one-element
// array. yuv selects the four-field RpcResult3 layout; the two-field
// RpcResult layout is used otherwise.
func EncodeResult(r *Result, yuv bool) ([]byte, error) {
	if r == nil {
		return nil, okerr.New(okerr.KindUnknown, "RPC结果为空", "无法序列化空结果")
	}
	if yuv {
		out := make([]wireSample4, 0, len(r.Samples))
		for _, s := range r.Samples {
			out = append(out, wireSample4{
				Item1: float64Ptr(float64(s.Index)),
				Item2: float64Ptr(s.Value),
				Item3: float64Ptr(s.ValueU),
				Item4: float64Ptr(s.ValueV),
			})
		}
		return marshalJSON([]rpcFile3{{
			Data:         out,
			FileNamePair: pairToWire(r.FileNamePair),
			Logs:         r.Logs,
		}})
	}
	out := make([]wireSample2, 0, len(r.Samples))
	for _, s := range r.Samples {
		out = append(out, wireSample2{
			Item1: float64Ptr(float64(s.Index)),
			Item2: float64Ptr(s.Value),
		})
	}
	return marshalJSON([]rpcFile2{{
		Data:         out,
		FileNamePair: pairToWire(r.FileNamePair),
		Logs:         r.Logs,
	}})
}

func float64Ptr(v float64) *float64 { return &v }

// marshalJSON renders v the way Newtonsoft did: no HTML escaping and no
// trailing newline.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, okerr.Wrap(err, okerr.KindUnknown, "RPC结果序列化失败", "%v", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
