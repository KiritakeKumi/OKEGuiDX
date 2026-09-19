package rpc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// Placeholders in RpcTemplate.vpy, replaced verbatim by RpChecker.GetRpcScript.
const (
	placeholderSource = "OKE:SOURCE_SCRIPT"
	placeholderVideo  = "OKE:VIDEO_FILE"
	placeholderArgs   = "OKE:VSPIPE_ARGS"
)

// DefaultTemplatePath is the legacy location of RpcTemplate.vpy, relative to
// the program directory (`.\tools\rpc\RpcTemplate.vpy`).
var DefaultTemplatePath = filepath.Join("tools", "rpc", "RpcTemplate.vpy")

// ScriptPath returns where the generated script is written for a ripped file.
// RpChecker replaced the extension with `_rpc.vpy`, so `00001.mkv` becomes
// `00001_rpc.vpy` next to it.
func ScriptPath(rippedFile string) string {
	return strings.TrimSuffix(rippedFile, filepath.Ext(rippedFile)) + "_rpc.vpy"
}

// ArgsClauses renders the `setattr(mod, 'key', b'value')` lines for the
// profile's vspipe arguments.
//
// RpcJob split each `key=value` at the first '=' and stored the pairs in a
// dictionary. Dictionary order is not observable in the generated script --
// every clause just has to run before loader.exec_module -- so the caller's
// order is used. Values are emitted as Python byte literals.
func ArgsClauses(args []string) string {
	var b strings.Builder
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok {
			// RpcJob did arg.Substring(0, pos) with pos == -1 and threw; the
			// processor is better off ignoring a malformed argument than
			// failing the whole task on it.
			continue
		}
		fmt.Fprintf(&b, "setattr(mod, '%s', b'%s')\n", key, escapePyString(value))
	}
	return b.String()
}

// ScriptContent fills RpcTemplate.vpy in for one run.
//
// The replacement order is the legacy order (source, then video, then args),
// which is observable: a source path that itself contains the literal
// "OKE:VIDEO_FILE" gets rewritten by the second pass. That is what
// RpChecker.GetRpcScript did, and it is preserved rather than "fixed" -- the
// substituted text is a path, and a real path never contains the marker.
func ScriptContent(template, sourceScript, videoFile string, args []string) string {
	content := template
	content = strings.ReplaceAll(content, placeholderSource, sourceScript)
	content = strings.ReplaceAll(content, placeholderVideo, videoFile)
	content = strings.ReplaceAll(content, placeholderArgs, ArgsClauses(args))
	return content
}

// WriteScript renders the script for one run and writes it next to the ripped
// file. It returns the path written.
func WriteScript(templatePath, sourceScript, videoFile string, args []string) (string, error) {
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		return "", okerr.Wrap(err, okerr.KindNotFound, "找不到RPC模板",
			"%s 不存在或无法读取", templatePath)
	}
	path := ScriptPath(videoFile)
	content := ScriptContent(string(raw), sourceScript, videoFile, args)
	//nolint:gosec // the path is derived from the task's own input file, not from untrusted input
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", okerr.Wrap(err, okerr.KindIO, "无法写入RPC脚本", "%s: %v", path, err)
	}
	return path, nil
}

// escapePyString escapes a value for a single-quoted Python byte literal.
// Python cannot express a literal newline inside a byte string, so CR/LF are
// escaped as well as the backslash and quote themselves.
func escapePyString(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\'':
			b.WriteString(`\'`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
