package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
)

// get runs one request against the handler.
func get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestHandlerServesTheShell pins the entry point: "/" must answer the embedded
// index, and it must be the page that names the program.
func TestHandlerServesTheShell(t *testing.T) {
	rec := get(t, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
	body := rec.Body.String()
	for _, want := range []string{"OKEGuiDX", "/static/app.js", "/api/v1"} {
		if !strings.Contains(body, want) {
			t.Errorf("index does not mention %q:\n%s", want, body)
		}
	}
}

// TestHandlerServesAssets covers the static tree the shell references.
func TestHandlerServesAssets(t *testing.T) {
	tests := []struct {
		path     string
		wantType string
		wantBody string
	}{
		{path: "/static/app.js", wantType: "javascript", wantBody: `const API = "/api/v1"`},
		{path: "/static/style.css", wantType: "text/css", wantBody: "body"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := get(t, tc.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", tc.path, rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); !strings.Contains(got, tc.wantType) {
				t.Errorf("Content-Type = %q, want it to contain %q", got, tc.wantType)
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("GET %s body does not contain %q", tc.path, tc.wantBody)
			}
		})
	}
}

// TestHandlerFallsBackToTheShell covers the single-page convention: a path the
// embedded tree does not contain is the shell, so a client-side route works
// without a server change.
func TestHandlerFallsBackToTheShell(t *testing.T) {
	for _, path := range []string{"/tasks", "/settings", "/deep/route/that/does/not/exist"} {
		rec := get(t, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want the shell", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "OKEGuiDX") {
			t.Errorf("GET %s did not answer the shell", path)
		}
	}
}

// TestHandlerRefusesTraversal asserts a hostile path cannot escape the embedded
// tree: the fallback answers the shell rather than reading a file.
func TestHandlerRefusesTraversal(t *testing.T) {
	for _, path := range []string{"/../web.go", "/static/../../web.go", "/%2e%2e/web.go"} {
		rec := get(t, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want the shell", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "package web") {
			t.Errorf("GET %s returned the Go source", path)
		}
	}
}

// TestHandlerRejectsNonGet asserts the UI handler only answers reads.
func TestHandlerRejectsNonGet(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Errorf("Allow = %q, want it to list GET", allow)
	}
}

// TestHandlerDisablesCaching pins the freshness rule: the UI ships inside the
// binary, so a cached copy is always a stale copy.
func TestHandlerDisablesCaching(t *testing.T) {
	rec := get(t, "/")
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
}

// TestFSExposesTheEmbeddedTree asserts the embedded files are reachable through
// FS, which is what a test or a future build step walks.
func TestFSExposesTheEmbeddedTree(t *testing.T) {
	f, err := FS().Open(indexFile)
	if err != nil {
		t.Fatalf("FS().Open(%q) error = %v", indexFile, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read embedded index: %v", err)
	}
	if !strings.Contains(string(raw), "OKEGuiDX") {
		t.Error("the embedded index does not name the program")
	}
}

// TestAssetName pins the URL-to-path mapping, including the root case.
func TestAssetName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/", want: indexFile},
		{path: "", want: indexFile},
		{path: "/static/app.js", want: "static/app.js"},
		{path: "/static/", want: "static"},
		{path: "/../web.go", want: "web.go"},
	}
	for _, tc := range tests {
		if got := assetName(tc.path); got != tc.want {
			t.Errorf("assetName(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestHandlerServesTheSettingsPanel pins the F3 deliverable: the settings page
// and its assets are part of the embedded tree, so the panel ships inside the
// binary like the rest of the UI.
func TestHandlerServesTheSettingsPanel(t *testing.T) {
	tests := []struct {
		path     string
		wantType string
		wantBody string
	}{
		{path: "/static/config.html", wantType: "text/html", wantBody: `id="config-root"`},
		{path: "/static/config.js", wantType: "javascript", wantBody: "window.OKEConfig"},
		{path: "/static/config.css", wantType: "text/css", wantBody: ".field"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := get(t, tc.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", tc.path, rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); !strings.Contains(got, tc.wantType) {
				t.Errorf("Content-Type = %q, want it to contain %q", got, tc.wantType)
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("GET %s body does not contain %q", tc.path, tc.wantBody)
			}
		})
	}
}

// TestSettingsPanelNamesEverySetting asserts the panel's form carries every key
// of platform.Config. PUT /api/v1/config replaces the whole file, so a setting
// the form does not carry is a setting the panel would erase; the two lists are
// therefore pinned against each other.
func TestSettingsPanelNamesEverySetting(t *testing.T) {
	// The keys are the JSON names in internal/platform/configdir.go. They are
	// spelled out here rather than reflected from platform.Config because the
	// wire format is the thing being pinned: a rename that kept the field but
	// changed the JSON name would still break an existing OKEGuiConfig.json.
	keys := []string{"vspipePath", "logLevel", "singleNuma", "rpCheckerPath", "avx512", "reducePath"}

	rec := get(t, "/static/config.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/config.js = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, key := range keys {
		// The key appears both in CONFIG_FIELDS and as the id of its control, so
		// the quoted form is the field list.
		if !strings.Contains(body, `"`+key+`"`) {
			t.Errorf("config.js does not mention the %q setting", key)
		}
	}
}

// TestSettingsPanelDocumentsTheAPI asserts the panel talks to the frozen
// endpoints: the settings and the node capabilities. A page that guessed a path
// would be a client of a contract that does not exist.
func TestSettingsPanelDocumentsTheAPI(t *testing.T) {
	rec := get(t, "/static/config.js")
	body := rec.Body.String()
	for _, want := range []string{`const API = "/api/v1"`, `"/config"`, `"/status"`, `"PUT"`} {
		if !strings.Contains(body, want) {
			t.Errorf("config.js does not contain %q", want)
		}
	}
}

// TestSettingsPanelIsPlainJavaScript pins the no-build-step rule: the panel is
// one script tag, so it may not use an import/export syntax that needs a module
// loader, and it may not require a bundler's require(). The wizard page's script
// is checked the same way; the task list is the one page that is an ES module
// because it shares static/api.js with nothing else.
func TestSettingsPanelIsPlainJavaScript(t *testing.T) {
	body := get(t, "/static/config.js").Body.String()
	for _, forbidden := range []string{"\nimport ", "\nexport ", "require("} {
		if strings.Contains(body, forbidden) {
			t.Errorf("config.js contains %q, which needs a bundler or a module loader", forbidden)
		}
	}
	if !strings.Contains(body, `"use strict"`) {
		t.Error("config.js does not opt into strict mode")
	}
	// The panel is loaded with a plain script tag, not as a module.
	page := get(t, "/static/config.html").Body.String()
	if !strings.Contains(page, `<script src="/static/config.js">`) {
		t.Error("config.html does not load config.js with a plain script tag")
	}
}

// TestSettingsPanelRoutesStillFallBackToTheShell covers the router convention
// after the panel was added: a client-side route for the settings page is the
// shell, so a router that only changes the hash still works. The standalone page
// lives at its own path, so both spellings have to be served.
func TestSettingsPanelRoutesStillFallBackToTheShell(t *testing.T) {
	for _, path := range []string{"/settings", "/#/config", "/config"} {
		rec := get(t, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want the shell", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "OKEGuiDX") {
			t.Errorf("GET %s did not answer the shell", path)
		}
	}

	// The page itself is a real asset, not the shell: it must answer its own
	// markup, otherwise the settings panel would only exist inside a router.
	rec := get(t, "/static/config.html")
	if !strings.Contains(rec.Body.String(), "设置") {
		t.Error("/static/config.html did not answer the settings page")
	}
}

// TestHandlerServesTheNewTaskWizard pins the F2 deliverable: the wizard page and
// its assets are part of the embedded tree, so it ships inside the binary like
// the rest of the UI.
//
// The page lives under static/ rather than at the tree root because the embed
// directive is `//go:embed index.html static`; a page outside it would need a
// change to web.go, which three workstreams share.
func TestHandlerServesTheNewTaskWizard(t *testing.T) {
	tests := []struct {
		path     string
		wantType string
		wantBody string
	}{
		{path: "/static/wizard.html", wantType: "text/html", wantBody: "新建任务向导"},
		{path: "/static/wizard.js", wantType: "javascript", wantBody: "window.OKEWizardCore"},
		{path: "/static/wizard-core.js", wantType: "javascript", wantBody: "derivePaths"},
		{path: "/static/wizard.css", wantType: "text/css", wantBody: ".field-error"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := get(t, tc.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", tc.path, rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); !strings.Contains(got, tc.wantType) {
				t.Errorf("Content-Type = %q, want it to contain %q", got, tc.wantType)
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("GET %s body does not contain %q", tc.path, tc.wantBody)
			}
		})
	}
}

// TestWizardPageIsARealAsset asserts the wizard is served as its own document
// rather than through the SPA fallback: the fallback answers index.html, which
// never names the wizard. Without this, a typo in the file name would silently
// serve the task list and the page would look like it loaded.
func TestWizardPageIsARealAsset(t *testing.T) {
	rec := get(t, "/static/wizard.html")
	body := rec.Body.String()
	if !strings.Contains(body, "新建任务向导") {
		t.Error("/static/wizard.html did not answer the wizard page")
	}
	if !strings.Contains(body, "/static/wizard-core.js") {
		t.Error("the wizard page does not reference wizard-core.js")
	}
	// The page must load the pure core before the DOM layer, because wizard.js
	// reads window.OKEWizardCore at its top level.
	core := strings.Index(body, "/static/wizard-core.js")
	dom := strings.Index(body, "/static/wizard.js")
	if core < 0 || dom < 0 || core > dom {
		t.Errorf("script order is wrong: wizard-core.js at %d, wizard.js at %d", core, dom)
	}
}

// TestWizardPageTargetsTheFrozenAPI pins the three fields the wizard derives for
// the pipeline. They are the seam internal/api/tasks.go deliberately left open
// (addTaskRequest.InputScript and friends), so a page that stopped sending them
// would queue tasks the pipeline refuses to run.
func TestWizardPageTargetsTheFrozenAPI(t *testing.T) {
	rec := get(t, "/static/wizard.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/wizard.js = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"/api/v1"`,
		"input_script",
		"working_path_prefix",
		"output_path_prefix",
		"profile_text",
		"base_dir",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("wizard.js does not mention %q", want)
		}
	}
}

// TestWizardScriptsArePlainJavaScript asserts the front end stays buildless: no
// import, no export and no module syntax, so the files load as classic scripts
// in the order the page lists them.
func TestWizardScriptsArePlainJavaScript(t *testing.T) {
	for _, path := range []string{"/static/wizard.js", "/static/wizard-core.js"} {
		rec := get(t, path)
		body := rec.Body.String()
		for _, forbidden := range []string{"\nimport ", "\nexport ", "require("} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s contains %q, which needs a bundler or a module loader", path, forbidden)
			}
		}
		if !strings.Contains(body, `"use strict"`) {
			t.Errorf("%s does not opt into strict mode", path)
		}
	}
}

// TestHandlerServesTheTaskList pins the F1 deliverable: the task list page and
// its assets are part of the embedded tree, so they ship inside the binary like
// the rest of the UI.
func TestHandlerServesTheTaskList(t *testing.T) {
	tests := []struct {
		path     string
		wantType string
		wantBody string
	}{
		{path: "/static/tasks.js", wantType: "javascript", wantBody: "export function formatPercent"},
		{path: "/static/tasks.css", wantType: "text/css", wantBody: ".progress-cell"},
		{path: "/static/api.js", wantType: "javascript", wantBody: "export class QueueActions"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := get(t, tc.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", tc.path, rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); !strings.Contains(got, tc.wantType) {
				t.Errorf("Content-Type = %q, want it to contain %q", got, tc.wantType)
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("GET %s body does not contain %q", tc.path, tc.wantBody)
			}
		})
	}
}

// TestTaskListPageTargetsTheFrozenAPI pins the endpoints the page talks to.
// They are the frozen REST contract (internal/api/api.go Mount), so a page that
// guessed a path would be a client of an interface that does not exist.
func TestTaskListPageTargetsTheFrozenAPI(t *testing.T) {
	api := get(t, "/static/api.js")
	if api.Code != http.StatusOK {
		t.Fatalf("GET /static/api.js = %d, want 200", api.Code)
	}
	body := api.Body.String()
	for _, want := range []string{
		`"/api/v1"`,
		`"/tasks"`,
		`"/status"`,
		`"/pool/start"`,
		`"/pool/stop"`,
		`"PATCH"`,
		`"DELETE"`,
		`{ position }`,
		`{ enabled: Boolean(enabled) }`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("api.js does not mention %q", want)
		}
	}

	// app.js is the WebSocket client, so it names the stream path and builds
	// the ws:// URL from the same prefix.
	app := get(t, "/static/app.js").Body.String()
	for _, want := range []string{
		`const API = "/api/v1"`,
		`const EVENTS = API + "/events"`,
		`new WebSocket(`,
		`url.protocol =`,
	} {
		if !strings.Contains(app, want) {
			t.Errorf("app.js does not mention %q", want)
		}
	}
}

// TestTaskListPageImplementsTheEventPatch pins the E3 contract's two halves:
// the full list builds the baseline, the WebSocket stream patches it, and a
// dropped-event report re-reads the list instead of applying a gap.
func TestTaskListPageImplementsTheEventPatch(t *testing.T) {
	body := get(t, "/static/tasks.js").Body.String()
	for _, want := range []string{
		// The baseline is a full list read.
		"replace(tasks)",
		// The increment is one event per task.
		"apply(event)",
		"patchStatus(task, event)",
		// A dropped event means the baseline is stale.
		"dropNotice",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("tasks.js does not implement %q", want)
		}
	}

	app := get(t, "/static/app.js").Body.String()
	for _, want := range []string{
		// The dropped counter is read off the envelope and acted upon.
		"message.dropped",
		"refreshTasks()",
		// The envelope discriminator.
		`case "status"`,
		`case "hello"`,
	} {
		if !strings.Contains(app, want) {
			t.Errorf("app.js does not implement %q", want)
		}
	}
}

// TestTaskListPageCarriesEveryDataGridColumn pins the column list against the
// legacy DataGrid (Gui/MainWindow.xaml). The header text is the operator-facing
// part of the contract: these are the words the WPF window showed.
func TestTaskListPageCarriesEveryDataGridColumn(t *testing.T) {
	body := get(t, "/static/tasks.js").Body.String()
	headers := []string{
		"任务名称", "输入文件", "章节", "输出文件", "状态", "进度",
		"速度", "码率", "剩余时间", "任务类型", "工作单元", "花屏检查",
	}
	for _, header := range headers {
		if !strings.Contains(body, `label: "`+header+`"`) {
			t.Errorf("the column list does not declare the %q column", header)
		}
	}

	// The order matters too: it is the order the XAML declared and the order the
	// operator reads. 章节 comes between 输入文件 and 输出文件 in the XAML.
	order := []string{"任务名称", "输入文件", "章节", "输出文件", "状态", "进度", "速度", "码率", "剩余时间", "任务类型", "工作单元", "花屏检查"}
	at := -1
	for _, header := range order {
		index := strings.Index(body, `label: "`+header+`"`)
		if index < 0 {
			t.Fatalf("the column list does not declare %q", header)
		}
		if index < at {
			t.Errorf("column %q appears out of DataGrid order", header)
		}
		at = index
	}
}

// TestTaskListPageCarriesTheRowActions pins the per-row operations and the
// toolbar against the C# handlers they port (MainWindow.BtnMoveUp/BtnMoveDown/
// BtnMoveTop/BtnDelete/BtnStop and the bottom button row).
func TestTaskListPageCarriesTheRowActions(t *testing.T) {
	body := get(t, "/static/tasks.js").Body.String()
	for _, want := range []string{
		// Per-row actions, with the position names the PATCH body accepts.
		`top: "top"`,
		`up: "up"`,
		`down: "down"`,
		`label: "置顶"`,
		`label: "上移"`,
		`label: "下移"`,
		`label: "停止"`,
		`label: "删除"`,
		// The toolbar.
		`label: "运行"`,
		`label: "终止"`,
		`label: "新建任务"`,
		`label: "更新章节"`,
		`label: "清空已完成"`,
		`label: "清空全部"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("tasks.js does not declare %q", want)
		}
	}
}

// TestTaskListPageFormatsLikeTheCSharpWindow pins the display strings that come
// from the C# properties rather than from the wire: TaskStatus.ProgressStr,
// TimeRemainStr, RpcStatus and TaskType. A wrong format here is invisible to a
// compile and obvious to the operator, which is why it is asserted by value.
func TestTaskListPageFormatsLikeTheCSharpWindow(t *testing.T) {
	body := get(t, "/static/tasks.js").Body.String()
	for _, want := range []string{
		// TaskStatus.cs: ProgressStr = progressValue.ToString("0.00") + "%"
		`+ "%"`,
		// TaskStatus.cs: TimeRemainStr = (int)TotalHours + ":mm:ss"
		`pad2(minutes)`,
		`pad2(secs)`,
		// TaskStatus.cs: TotalHours > 24*7 => "大于一周"
		`"大于一周"`,
		// RpChecker.cs: the enum members are Chinese and are shown verbatim.
		`"等待中"`,
		`"跳过"`,
		`"错误"`,
		`"未通过"`,
		`"通过"`,
		// TaskStatus.cs: TaskTypeEnum { Normal, ReEncode }
		`"Normal"`,
		`"ReEncode"`,
		// ChapterService.cs: enum ChapterStatus { No, Yes, Added, Maybe, MKV, Warn }
		`No: "No"`,
		`MKV: "MKV"`,
		`Warn: "Warn"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("tasks.js does not contain %q", want)
		}
	}
}

// TestTaskListPageIsAnESModule asserts the page uses real module syntax, which
// the browser loads natively: no bundler, no build step. app.js imports the
// other two by relative path, so they must stay siblings in static/.
func TestTaskListPageIsAnESModule(t *testing.T) {
	app := get(t, "/static/app.js").Body.String()
	for _, want := range []string{`from "./tasks.js"`, `from "./api.js"`} {
		if !strings.Contains(app, want) {
			t.Errorf("app.js does not %s", want)
		}
	}
	shell := get(t, "/").Body.String()
	if !strings.Contains(shell, `type="module"`) {
		t.Error("the shell does not load app.js as a module")
	}
	if !strings.Contains(shell, "/static/tasks.css") {
		t.Error("the shell does not load the task list stylesheet")
	}

	// tasks.js is the pure half: it must not touch the DOM or the network, so it
	// stays importable outside a browser (which is what the node smoke test
	// does). The I/O lives in app.js and api.js.
	tasks := get(t, "/static/tasks.js").Body.String()
	for _, forbidden := range []string{"document.", "window.", "fetch(", "new WebSocket("} {
		if strings.Contains(tasks, forbidden) {
			t.Errorf("tasks.js touches %q; the DOM and the network belong to app.js/api.js", forbidden)
		}
	}
	if !strings.Contains(tasks, "export function") {
		t.Error("tasks.js exports nothing")
	}

	// api.js is the network half and must not touch the DOM either.
	api := get(t, "/static/api.js").Body.String()
	for _, forbidden := range []string{"document.", "window."} {
		if strings.Contains(api, forbidden) {
			t.Errorf("api.js touches %q; the DOM belongs to app.js", forbidden)
		}
	}
	if !strings.Contains(api, "export class QueueActions") {
		t.Error("api.js does not export the queue client")
	}
}

// TestTaskListPageDropsTheVolatileColumns documents the one deliberate
// omission: the legacy window's 花屏检查 button launched RPChecker.exe, which a
// browser cannot do, and the pool's worker buttons changed the worker count,
// which the frozen API has no endpoint for. The page says so instead of
// pretending; this test keeps that decision visible.
func TestTaskListPageDropsTheVolatileColumns(t *testing.T) {
	body := get(t, "/static/app.js").Body.String()
	for _, want := range []string{
		// The RPC result is shown, with the legacy command named.
		"RPChecker",
		// The missing endpoints are named, so an operator knows why.
		"没有对应的端点",
		"没有新增工作单元的端点",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("app.js does not explain %q", want)
		}
	}
}

// TestTaskListPageSmokeTest runs the browser-side smoke tests of the page. They
// are plain ES modules executed by node, so they need no test framework and no
// npm step; when node is not installed the test skips, because a machine without
// node can still build and serve the daemon.
//
// check.mjs pins the formatting helpers against the C# values; dom-smoke.mjs
// drives app.js through a minimal DOM shim and asserts the rendered table, the
// event patch and the re-fetch triggers. Both live under testdata/ so they are
// not embedded: a test has no business shipping inside the binary.
func TestTaskListPageSmokeTest(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; skipping the JavaScript smoke tests")
	}

	scripts := []string{"check.mjs", "dom-smoke.mjs", "conflict-smoke.mjs"}
	for _, script := range scripts {
		t.Run(script, func(t *testing.T) {
			cmd := exec.Command(node, "--no-warnings", script)
			cmd.Dir = filepath.Join("testdata", "js")
			// conflict-smoke.mjs talks to a live daemon and skips itself when
			// this is unset, which is the normal case for `go test ./web/...`.
			cmd.Env = append(os.Environ(), "OKEGUI_TEST_ADDR="+os.Getenv("OKEGUI_TEST_ADDR"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("node %s failed: %v\n%s", script, err, out)
			}
			if !strings.Contains(string(out), "checks passed") &&
				!strings.Contains(string(out), "skipped") {
				t.Errorf("%s did not report its result:\n%s", script, out)
			}
		})
	}
}

// TestTaskListPageReadsTheWireFormat pins the JSON the page reads against the
// frozen model types. It exists because the page shipped with a real bug of
// exactly this kind: model.TaskType is the one status enum without MarshalText,
// so task_type arrives as a NUMBER while TaskProgress, ChapterStatus and
// RPCStatus arrive as strings. Nothing in the Go build could catch that; this
// test can.
func TestTaskListPageReadsTheWireFormat(t *testing.T) {
	raw, err := json.Marshal(model.TaskStatus{
		ID:       model.NewTaskID(),
		Name:     "ep01",
		Input:    model.NewFileRef(`D:\media\ep01.m2ts`),
		Enabled:  true,
		Progress: model.TaskRunning,
		Status:   "压制中",
		TaskType: model.TaskTypeReEncode,
		Chapter:  model.ChapterMaybe,
		RPC:      model.RPCPassed,
	})
	if err != nil {
		t.Fatalf("marshal TaskStatus: %v", err)
	}

	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal TaskStatus: %v", err)
	}

	// The three enums with MarshalText are strings, and the page reads the names
	// the model's String methods return.
	stringsWanted := map[string]string{
		"progress":       "RUNNING",
		"chapter_status": "Maybe",
		"rpc_status":     "通过",
	}
	for key, want := range stringsWanted {
		got, ok := wire[key].(string)
		if !ok {
			t.Errorf("%s = %T, want a string (the page reads it as one)", key, wire[key])
			continue
		}
		if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	// TaskType is the exception, and the page has to tolerate it.
	if got, ok := wire["task_type"].(float64); !ok || got != 1 {
		t.Errorf("task_type = %v (%T), want the number 1 (ReEncode); "+
			"if MarshalText was added, taskTypeText must keep accepting both", wire["task_type"], wire["task_type"])
	}

	// The display fields the page reads by name.
	for _, key := range []string{"id", "name", "enabled", "status", "progress_value", "speed", "bit_rate", "time_remain_seconds", "worker_name", "rpc_output"} {
		if _, ok := wire[key]; !ok {
			t.Errorf("TaskStatus JSON is missing %q, which the page reads", key)
		}
	}

	// FileRef is a single "volume/rel" string, not an object: displayPath
	// splits on the first slash.
	input, ok := wire["input"].(string)
	if !ok {
		t.Fatalf("input = %T, want a string (FileRef MarshalText)", wire["input"])
	}
	if !strings.HasPrefix(input, model.LocalVolume+"/") {
		t.Errorf("input = %q, want the %q volume prefix", input, model.LocalVolume)
	}
}

// TestTaskListPageReadsTheEventWireFormat does the same for the progress event,
// because the page's event handler reads the same names off the WebSocket
// envelope's event object.
func TestTaskListPageReadsTheEventWireFormat(t *testing.T) {
	raw, err := json.Marshal(model.StatusEvent{
		TaskID:            model.NewTaskID(),
		Progress:          model.TaskRunning,
		Step:              "压制中",
		Percent:           42.5,
		Speed:             "12.34 fps",
		BitRate:           "1234.56 kb/s",
		TimeRemainSeconds: 3661,
		FramesDone:        100,
		FramesTotal:       200,
	})
	if err != nil {
		t.Fatalf("marshal StatusEvent: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal StatusEvent: %v", err)
	}
	for _, key := range []string{"task_id", "progress", "step", "percent", "speed", "bit_rate", "time_remain_seconds", "frames_done", "frames_total"} {
		if _, ok := wire[key]; !ok {
			t.Errorf("StatusEvent JSON is missing %q, which the page reads", key)
		}
	}
	if wire["progress"] != "RUNNING" {
		t.Errorf("progress = %v, want the string RUNNING", wire["progress"])
	}
	if _, ok := wire["percent"].(float64); !ok {
		t.Errorf("percent = %T, want a number", wire["percent"])
	}
}

// TestTaskListRoutesStillFallBackToTheShell covers the SPA convention after the
// task list became the shell's own page: a client-side route is still answered
// by index.html, so a deep link works without a server change. The other two
// pages are real documents under static/, so both spellings are checked.
func TestTaskListRoutesStillFallBackToTheShell(t *testing.T) {
	for _, path := range []string{"/tasks", "/#/tasks", "/deep/route"} {
		rec := get(t, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want the shell", path, rec.Code)
			continue
		}
		body := rec.Body.String()
		if !strings.Contains(body, "任务列表") {
			t.Errorf("GET %s did not answer the task list shell", path)
		}
	}

	// The shell is the task list, so its own markup is the thing a deep link
	// gets. A stale copy of the wiring page would still say "接线阶段".
	shell := get(t, "/").Body.String()
	if strings.Contains(shell, "接线阶段") {
		t.Error("the shell still carries the wiring-phase placeholder")
	}
}

// TestTaskListAssetsAreServedFromTheirOwnPaths pins the static paths the page
// declares: a file referenced by the shell but absent from the embedded tree
// would fall back to index.html, and the browser would report a syntax error
// instead of a missing asset.
func TestTaskListAssetsAreServedFromTheirOwnPaths(t *testing.T) {
	assets := []struct {
		path     string
		wantType string
	}{
		{path: "/static/tasks.js", wantType: "javascript"},
		{path: "/static/tasks.css", wantType: "text/css"},
		{path: "/static/api.js", wantType: "javascript"},
		{path: "/static/app.js", wantType: "javascript"},
	}
	for _, asset := range assets {
		rec := get(t, asset.path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", asset.path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); !strings.Contains(got, asset.wantType) {
			t.Errorf("GET %s Content-Type = %q, want %q", asset.path, got, asset.wantType)
		}
		// The fallback answers the shell for a missing asset, and the shell is
		// HTML: a Content-Type of text/html for a .js path is the failure mode
		// this catches.
		if strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("GET %s answered the shell instead of the asset", asset.path)
		}
	}
}
