package wizard

import (
	"os"
	"regexp"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// This file ports the part of WizardFinish that prepares the script text: the
// #OKE:PROJECTDIR and #OKE:DEBUG tags are rewritten once for the whole profile,
// and the #OKE:INPUTFILE tag supplies the template each per-episode script is
// built from.
//
// All three use .NET's Regex.Split, whose result for one match is
//
//	[before, group1, group2, after]
//
// and whose result for N matches is
//
//	[before, g1, g2, between1, g1, g2, between2, ..., after]
//
// The legacy expressions indexed [0], [1] and [3], which means they always
// rewrote the first tag and, when a script has several, dropped everything from
// the second one on. That is faithful: the shipped scripts have exactly one of
// each. Go's regexp.Split is not a drop-in replacement because it discards the
// capture groups, so the split is implemented here.

// rewriteScript applies the PROJECTDIR and DEBUG rewrites to the profile's
// script text.
//
// The legacy code only read the script (AddTaskService.LoadVsScript did that)
// and rewrote it once per WizardFinish, i.e. once for the whole profile, which
// is why the two tag rewrites are not per-episode. The profile's InputScript is
// resolved against the profile's directory first, which is what
// AddTaskService.LoadVsScript did before it read the file.
func rewriteScript(p *profile.Profile, projectDir string) (string, error) {
	if p.InputScript == "" {
		return "", okerr.New(okerr.KindConfig, "vpy文件找不到",
			"配置 %s 没有指定 InputScript", p.ConfigFilePath)
	}
	path := resolveInput(projectDir, p.InputScript)
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", okerr.Wrap(err, okerr.KindNotFound, "vpy文件找不到",
			"指定的vpy文件没有找到，检查下json文件和vpy文件是不是放一起了？(%q)", path)
	}
	script := string(raw)

	// `dirTag[0] + dirTag[1] + "R\"" + projectDir + "\"" + dirTag[3]`: the old
	// value is dropped on purpose, because the project directory is what the
	// wizard knows and the script cannot.
	if parts := dotNetSplit(profile.ProjectDirTagPattern, script); len(parts) >= 4 {
		script = parts[0] + parts[1] + `R"` + projectDir + `"` + parts[3]
	}
	// `debugTag[0] + debugTag[1] + "None" + debugTag[3]`: the debug branch is
	// forced off, because interleaving the source with the output doubles the
	// frame count the encoder sees.
	if parts := dotNetSplit(profile.DebugTagPattern, script); len(parts) >= 4 {
		script = parts[0] + parts[1] + "None" + parts[3]
	}
	return script, nil
}

// buildVpy splices one source path into the template the INPUTFILE tag defines.
//
// The template comes from profile.InputTagPattern, whose two capture groups are
// the assignment prefix ("\na=") and the quoted value ('"00000.m2ts"'). The
// legacy expression rebuilt the line as
//
//	inputTemplate[0] + inputTemplate[1] + "R\"" + inputFile + "\"" + inputTemplate[3]
//
// which is where the r-string literal comes from: a Windows path is full of
// backslashes and Python would otherwise read them as escape sequences.
//
// The result is the whole script, not just the line.
func buildVpy(script, inputFile string) (string, error) {
	parts := dotNetSplit(profile.InputTagPattern, script)
	if len(parts) < 4 {
		return "", okerr.New(okerr.KindConfig, "vpy没有为OKEGui设计",
			"vpy里没有#OKE:INPUTFILE的标签，无法为源文件生成脚本。")
	}
	return parts[0] + parts[1] + `R"` + inputFile + `"` + parts[3], nil
}

// vpyName is the generated script's name: the working prefix with the timestamp
// appended, so "00000.m2ts" becomes "00000.m2ts-09200905.vpy". It is spelled
// out here because the legacy expression `newPath + "-" + stamp + ".vpy"` is
// the reason a source file keeps its extension in the name.
func vpyName(workingPrefix, stamp string) string {
	return workingPrefix + "-" + stamp + ".vpy"
}

// dotNetSplit reproduces .NET's Regex.Split: the text before each match, the
// text each capture group matched, and the trailing text, in order.
//
// The index of the first match's first group is always 1 and the text that
// follows it is always 3, which is what the legacy expressions relied on. A
// script with no match yields a single element, so callers check the length.
func dotNetSplit(re *regexp.Regexp, s string) []string {
	locs := re.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return []string{s}
	}
	groups := re.NumSubexp()
	parts := make([]string, 0, len(locs)*(groups+1)+1)
	last := 0
	for _, loc := range locs {
		parts = append(parts, s[last:loc[0]])
		for g := 1; g <= groups; g++ {
			start, end := loc[2*g], loc[2*g+1]
			if start < 0 {
				// An optional group that did not participate matched the empty
				// string, which is what .NET reports for it too.
				parts = append(parts, "")
				continue
			}
			parts = append(parts, s[start:end])
		}
		last = loc[1]
	}
	return append(parts, s[last:])
}
