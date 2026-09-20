// config.js is the settings panel: the Web UI counterpart of Gui/ConfigPanel.xaml
// and the OKEGuiConfig class in Utils/Initializer.cs.
//
// It reads GET /api/v1/config, edits a local copy of the settings object and
// writes the whole object back with PUT /api/v1/config. The write is a full
// replacement, not a patch (internal/api/config.go), so every field the panel
// knows about is always present in the body: a field left out would be erased.
//
// The panel is a plain script with no framework and no build step, like the rest
// of the front end. It carries its own markup, so there is exactly one copy of
// the form and two ways in:
//
//   - the standalone page /static/config.html loads this file, which fills
//     #config-root and wires itself;
//   - a router calls window.OKEConfig.mount(element) and gets the same panel
//     inside whatever container it is routing to.
//
// The whole file is one IIFE so it shares no top-level names with the other page
// scripts (wizard.js also declares a top-level API constant); the only global it
// creates is window.OKEConfig.

(function () {
  "use strict";

  const API = "/api/v1";

  // LOG_LEVELS is the legacy ComboBox (ConfigPanel.xaml), least to most verbose.
  // The legacy list also had OFF, but validateConfig rejects it, so it is not
  // offered; a file that already holds OFF shows it as an extra entry so the
  // panel never silently changes a value it cannot represent.
  const LOG_LEVELS = ["FATAL", "ERROR", "WARN", "INFO", "DEBUG", "TRACE"];

  // CONFIG_FIELDS is every key of platform.Config, in the order the JSON file
  // writes them. The keys are the legacy property names, so an existing
  // OKEGuiConfig.json keeps working.
  const CONFIG_FIELDS = [
    "vspipePath",
    "logLevel",
    "singleNuma",
    "rpCheckerPath",
    "avx512",
    "reducePath",
  ];

  // REQUIRED_TOOLS mirrors internal/toolchain.requiredTools: the engine refuses
  // to start a task when one of these is missing.
  const REQUIRED_TOOLS = ["vspipe", "ffmpeg", "ffprobe"];

  // TOOL_NOTES explains what each tool is for, so an operator can tell which
  // missing tool costs which feature. The keys are the names in
  // node.Capabilities.Tools (the toolchain constants).
  const TOOL_NOTES = {
    vspipe: "VapourSynth 脚本求值",
    ffmpeg: "解封装与转码",
    ffprobe: "媒体信息探测",
    x264: "x264 视频编码",
    x265: "x265 视频编码",
    svtav1: "SVT-AV1 视频编码",
    mkvmerge: "MKV 封装",
    mkvextract: "MKV 解封装",
    muxer: "l-smash 封装",
    qaac: "AAC 音频编码",
    "eac3to-wrapper": "eac3to 音轨处理",
    flac: "FLAC 音频编码",
    tchapter: "章节处理",
    rpchecker: "RPC 检查",
  };

  // FEATURE_NOTES explains node.features. A feature is advertised only when the
  // tools behind it are present, which is why the tags sit next to the tool
  // table: they answer "can this node produce AAC?".
  const FEATURE_NOTES = {
    aac: "可以输出 AAC（qaac 已就位）",
    eac3to: "可以使用 eac3to 处理音轨",
    numa: "启用按 NUMA 节点绑核",
    "fdk-aac": "本版本不使用 libfdk-aac",
  };

  // TEMPLATE is the form itself. It lives here rather than in the page so that a
  // router gets the markup together with the behaviour, and so there is exactly
  // one definition of it. The ids it declares are the keys of CONFIG_FIELDS plus
  // the read-only parts.
  const TEMPLATE = `
    <section class="panel">
      <p class="banner">
        编辑的是 <code>OKEGuiConfig.json</code>。
        <strong>保存会整体替换配置文件</strong>（<code>PUT /api/v1/config</code> 是全量替换，
        不是局部修改），所以表单里的每一项都会写回文件。
      </p>
      <p class="banner muted">
        日志级别保存后立即生效；工具路径、Numa 与 AVX512 只影响下次启动的 daemon，
        因为工具链在启动时就已经发现完了。
      </p>
    </section>

    <section class="panel">
      <h2>外部工具</h2>
      <p class="muted">
        留空表示使用 <code>tools</code> 目录下自动发现的路径。
        浏览器打不开本机的文件对话框，所以这里用「填入已发现的路径」代替旧版的「选择」按钮，
        也可以直接把绝对路径粘贴进来。
      </p>

      <div class="field">
        <label for="vspipePath">VSPipe.exe</label>
        <div class="row">
          <input id="vspipePath" type="text" spellcheck="false" autocomplete="off"
                 placeholder="留空则使用 tools\\vapoursynth\\vspipe.exe" />
          <button id="vspipe-fill" type="button">填入已发现的路径</button>
        </div>
        <p class="hint" id="vspipe-hint">正在读取…</p>
      </div>

      <div class="field">
        <label for="rpCheckerPath">RPChecker.exe</label>
        <div class="row">
          <input id="rpCheckerPath" type="text" spellcheck="false" autocomplete="off"
                 placeholder="留空则使用 tools\\rpc\\RPChecker.exe" />
          <button id="rpChecker-fill" type="button">填入已发现的路径</button>
        </div>
        <p class="hint" id="rpChecker-hint">正在读取…</p>
      </div>
    </section>

    <section class="panel">
      <h2>行为</h2>

      <div class="field">
        <label for="logLevel">记录详细程度（从上到下越来越详细）</label>
        <select id="logLevel"></select>
        <p class="hint">
          旧版下拉框还有 <code>OFF</code>（完全关闭日志），当前接口不接受该值，
          因此不再提供；文件里已有的 <code>OFF</code> 会原样显示，保存时会被拒绝并提示改选。
        </p>
      </div>

      <div class="field check">
        <input id="singleNuma" type="checkbox" />
        <label for="singleNuma">跳过 Numa 检测（AMD Zen / Zen 2 系列请勾选）</label>
      </div>

      <div class="field check">
        <input id="avx512" type="checkbox" />
        <label for="avx512">开启 AVX512 烤鸡模式</label>
      </div>

      <div class="field check">
        <input id="reducePath" type="checkbox" />
        <label for="reducePath">启用中间文件目录和输出目录路径缩减功能</label>
      </div>
    </section>

    <section class="panel actions">
      <button id="save" type="button" class="primary">保存</button>
      <button id="discard" type="button">取消</button>
      <span id="save-state" class="muted" role="status"></span>
    </section>

    <section class="panel">
      <h2>配置文件位置</h2>
      <dl class="paths">
        <dt>配置目录</dt>
        <dd id="config-dir">未知</dd>
        <dt>配置文件</dt>
        <dd id="config-file">未知</dd>
      </dl>
      <p class="hint">
        这是按节点操作系统推断的默认位置：接口没有返回实际路径，
        若 daemon 启动时指定了 <code>--config</code>，请以该参数为准。
      </p>
    </section>

    <section class="panel">
      <h2>工具状态</h2>
      <p id="node" class="muted">正在读取…</p>
      <p class="features" id="features"></p>
      <table>
        <thead>
          <tr>
            <th>工具</th>
            <th>状态</th>
            <th>版本</th>
            <th>变体</th>
            <th>路径</th>
            <th>说明</th>
          </tr>
        </thead>
        <tbody id="tool-rows"></tbody>
      </table>
      <p class="hint">
        「必需」是引擎启动任务前会检查的三个工具（vspipe / ffmpeg / ffprobe）；
        其余工具缺失只会让对应功能不可用，例如没有 qaac 就无法输出 AAC。
        这张表是只读的：工具链在 daemon 启动时发现一次，重装工具后需要重启 daemon。
      </p>
    </section>
  `;

  // configPaths returns the default per-user location of the settings file for a
  // node's OS. It mirrors platform.ConfigDirPath, which is
  // os.UserConfigDir()/OKEGuiDX/OKEGuiConfig.json. The REST API does not return
  // the path the daemon actually opened, so the panel labels this as the default
  // and tells the operator to check --config when in doubt.
  function configPaths(os) {
    if (os === "windows") {
      return {
        dir: "%AppData%\\OKEGuiDX",
        file: "%AppData%\\OKEGuiDX\\OKEGuiConfig.json",
      };
    }
    return {
      dir: "$XDG_CONFIG_HOME/OKEGuiDX（缺省 ~/.config/OKEGuiDX）",
      file: "~/.config/OKEGuiDX/OKEGuiConfig.json",
    };
  }

  // isRequiredTool reports whether a missing tool blocks the engine.
  function isRequiredTool(name) {
    return REQUIRED_TOOLS.indexOf(name) !== -1;
  }

  // knownTools returns every tool name the table can show, sorted.
  function knownTools() {
    return Object.keys(TOOL_NOTES).sort();
  }

  // configBody builds the PUT /api/v1/config body. The settings object is nested
  // under "config" because that is the frozen request shape.
  function configBody(cfg) {
    return { config: cfg };
  }

  // readError extracts the API's summary/detail/field triple from a failed
  // response, so the panel shows the same wording the CLI would. The status text
  // is the fallback for a body that is not the API's error object, which is what
  // a proxy or a panic answers with.
  function readError(status, statusText, body) {
    const info = body && body.error ? body.error : {};
    const summary = info.summary || statusText || "HTTP " + status;
    const detail = info.detail || "";
    return {
      summary,
      detail,
      field: info.field || "",
      message: summary + (detail ? "：" + detail : ""),
    };
  }

  // resolve looks an element up inside root. root is a Document on the standalone
  // page and an element when a router mounts the panel, so both cases are
  // covered.
  function resolve(root, id) {
    if (typeof root.getElementById === "function") {
      return root.getElementById(id);
    }
    return root.querySelector("#" + id);
  }

  // ---------------------------------------------------------------------------
  // Panel
  // ---------------------------------------------------------------------------

  // createPanel wires the panel inside root, filling it with the form first.
  function createPanel(root) {
    // loaded holds the last values read from the daemon, so 取消 restores them.
    let loaded = null;

    root.innerHTML = TEMPLATE;
    const byId = (id) => resolve(root, id);

    // request performs one API call and returns the decoded JSON. A failure is
    // thrown as an Error carrying the API's wording, because that is what the
    // operator needs to see.
    async function request(path, options) {
      const resp = await fetch(path, options);
      const text = await resp.text();
      let body = null;
      try {
        body = text ? JSON.parse(text) : null;
      } catch (err) {
        // A non-JSON body is reported through the HTTP status below.
      }
      if (!resp.ok) {
        const info = readError(resp.status, resp.statusText, body);
        const error = new Error(info.message);
        error.summary = info.summary;
        error.detail = info.detail;
        error.field = info.field;
        error.status = resp.status;
        throw error;
      }
      return body;
    }

    // setState writes the save status line. kind is "", "error" or "ok".
    function setState(text, kind) {
      const el = byId("save-state");
      if (!el) {
        return;
      }
      el.textContent = text || "";
      el.className = kind === "error" ? "state-error" : kind === "ok" ? "state-ok" : "muted";
    }

    // renderLogLevels fills the ComboBox equivalent.
    function renderLogLevels(current) {
      const select = byId("logLevel");
      const levels = LOG_LEVELS.slice();
      if (current && levels.indexOf(current) === -1) {
        levels.unshift(current);
      }
      select.replaceChildren(
        ...levels.map((level) => {
          const option = document.createElement("option");
          option.value = level;
          option.textContent = level;
          return option;
        })
      );
      select.value = current || levels[0];
    }

    // readForm collects the settings object the panel would write.
    function readForm() {
      const cfg = {};
      for (const field of CONFIG_FIELDS) {
        const el = byId(field);
        cfg[field] = el.type === "checkbox" ? el.checked : el.value;
      }
      return cfg;
    }

    // writeForm fills the controls from a settings object.
    function writeForm(cfg) {
      for (const field of CONFIG_FIELDS) {
        const el = byId(field);
        if (el.type === "checkbox") {
          el.checked = cfg[field] === true;
        } else if (field === "logLevel") {
          renderLogLevels(cfg[field]);
        } else {
          el.value = cfg[field] || "";
        }
      }
    }

    // cell builds one table cell.
    function cell(text, cls) {
      const td = document.createElement("td");
      td.textContent = text;
      if (cls) {
        td.className = cls;
      }
      return td;
    }

    // toolRow builds the row of a tool the toolchain found.
    function toolRow(name, info) {
      const tr = document.createElement("tr");
      tr.append(
        cell(name),
        cell("已找到", "state-ok"),
        cell(info.version || "未知"),
        cell(info.variant || "—"),
        cell(info.path || "", "path"),
        cell(TOOL_NOTES[name] || "", "note")
      );
      return tr;
    }

    // missingRow builds the row of a tool that was not found. Missing tools are
    // the point of this table: the legacy EnvironmentChecker popped one
    // MessageBox per missing tool, this shows them all at once.
    //
    // configured is the path the settings file names for this tool, when it names
    // one. A configured path that does not exist is not reported in node.tools at
    // all, so the two sources are merged by renderTools.
    function missingRow(name, configured) {
      const tr = document.createElement("tr");
      const required = isRequiredTool(name);
      tr.append(
        cell(name),
        cell(required ? "缺失（必需）" : "缺失", required ? "state-error" : "state-missing"),
        cell("—"),
        cell("—"),
        cell(configured ? configured + "（配置的路径不存在）" : "—", "path"),
        cell(TOOL_NOTES[name] || "", "note")
      );
      return tr;
    }

    // renderFeatures draws one tag per known feature, marking the ones this node
    // does not advertise.
    function renderFeatures(features) {
      const names = features.slice().sort();
      for (const name of Object.keys(FEATURE_NOTES)) {
        if (names.indexOf(name) === -1) {
          names.push(name);
        }
      }
      byId("features").replaceChildren(
        ...names.map((name) => {
          const on = features.indexOf(name) !== -1;
          const span = document.createElement("span");
          span.className = on ? "tag" : "tag off";
          span.textContent = (on ? "✓ " : "✗ ") + name;
          span.title = FEATURE_NOTES[name] || "";
          return span;
        })
      );
    }

    // configuredPaths maps the two path settings onto tool names, the same
    // mapping cmd/okegui uses when it hands the settings to the toolchain.
    function configuredPaths(cfg) {
      const out = {};
      if (cfg && cfg.vspipePath) {
        out.vspipe = cfg.vspipePath;
      }
      if (cfg && cfg.rpCheckerPath) {
        out.rpchecker = cfg.rpCheckerPath;
      }
      return out;
    }

    // renderToolHints fills the "填入已发现的路径" buttons with what the toolchain
    // found, and disables a button whose tool is absent.
    function renderToolHints(node) {
      const tools = node.tools || {};
      const pairs = [
        { id: "vspipe", tool: "vspipe" },
        { id: "rpChecker", tool: "rpchecker" },
      ];
      for (const pair of pairs) {
        const info = tools[pair.tool];
        const hint = byId(pair.id + "-hint");
        const button = byId(pair.id + "-fill");
        if (info && info.path) {
          hint.textContent =
            "已发现：" + info.path + (info.version ? "（版本 " + info.version + "）" : "");
          hint.className = "hint ok";
          button.disabled = false;
        } else {
          hint.textContent = "未发现，可手动填写绝对路径。";
          hint.className = "hint missing";
          button.disabled = true;
        }
      }
    }

    // renderTools draws the node summary, the feature tags and the tool table.
    function renderTools(node, cfg) {
      const tools = node.tools || {};
      const found = Object.keys(tools).sort();
      const known = knownTools();
      const absent = known.filter((name) => found.indexOf(name) === -1);

      byId("node").textContent =
        "节点 " +
        (node.node_id || "?") +
        " · 角色 " +
        (node.role || "?") +
        " · " +
        (node.os || "?") +
        "/" +
        (node.arch || "?") +
        " · CPU " +
        (node.cpus === undefined ? "?" : node.cpus) +
        " 核 · NUMA 节点 " +
        (node.numa_nodes === undefined ? "?" : node.numa_nodes) +
        " · 工具 " +
        found.length +
        "/" +
        known.length +
        " 个已找到";

      renderFeatures(Array.isArray(node.features) ? node.features : []);

      const configured = configuredPaths(cfg);
      const rows = found.map((name) => toolRow(name, tools[name] || {}));
      for (const name of absent) {
        rows.push(missingRow(name, configured[name] || ""));
      }
      byId("tool-rows").replaceChildren(...rows);

      const paths = configPaths(node.os);
      byId("config-dir").textContent = paths.dir;
      byId("config-file").textContent = paths.file;

      renderToolHints(node);
    }

    // readStatus loads the node capabilities and redraws the read-only table.
    async function readStatus(cfg) {
      const resp = await request(API + "/status", { headers: { Accept: "application/json" } });
      renderTools(resp.node || {}, cfg);
    }

    // load reads the settings and the node capabilities. The capabilities are
    // read on this page because the settings window is where the legacy program
    // checked the environment, and a broken tool path is the first thing an
    // operator looks for.
    async function load() {
      setState("正在读取…", "");
      try {
        const configResp = await request(API + "/config", {
          headers: { Accept: "application/json" },
        });
        loaded = configResp.config || {};
        writeForm(loaded);
        await readStatus(loaded);
        setState("");
      } catch (err) {
        setState("读取失败：" + err.message, "error");
      }
    }

    // save writes the whole settings object back. The server validates it, so a
    // rejected value (an unknown log level, a malformed body) arrives here as the
    // API's error object and is shown verbatim.
    async function save() {
      const cfg = readForm();
      setState("正在保存…", "");
      try {
        const resp = await request(API + "/config", {
          method: "PUT",
          headers: { "Content-Type": "application/json", Accept: "application/json" },
          body: JSON.stringify(configBody(cfg)),
        });
        loaded = resp.config || cfg;
        writeForm(loaded);
        setState("已保存。日志级别立即生效，其余设置下次启动生效。", "ok");
        try {
          // The tool paths may now point somewhere else, so the read-only table
          // is refreshed from the daemon's own view of the machine.
          await readStatus(loaded);
        } catch (err) {
          // The save succeeded; a failed refresh must not look like a failed
          // save.
        }
      } catch (err) {
        setState("保存失败：" + err.message, "error");
      }
    }

    // discard puts the form back to what was last read.
    function discard() {
      if (loaded) {
        writeForm(loaded);
        setState("已还原为上次读取的值。");
      }
    }

    // fillPath copies a discovered tool path into its text box. It re-reads the
    // status rather than caching it, so a path that changed since the page loaded
    // is not offered.
    async function fillPath(inputId, toolName) {
      try {
        const resp = await request(API + "/status", {
          headers: { Accept: "application/json" },
        });
        const info = ((resp.node || {}).tools || {})[toolName];
        if (info && info.path) {
          byId(inputId).value = info.path;
          setState("");
        }
      } catch (err) {
        setState("读取工具路径失败：" + err.message, "error");
      }
    }

    // wire installs the listeners and loads the data.
    function wire() {
      byId("save").addEventListener("click", () => {
        save();
      });
      byId("discard").addEventListener("click", discard);
      byId("vspipe-fill").addEventListener("click", () => {
        fillPath("vspipePath", "vspipe");
      });
      byId("rpChecker-fill").addEventListener("click", () => {
        fillPath("rpCheckerPath", "rpchecker");
      });
      load();
    }

    return { load, save, discard, readForm, writeForm, renderTools, wire };
  }

  // mount fills a container with the panel and wires it. root may be a Document
  // (the standalone page) or an element (a router's route container). Mounting
  // the same container twice replaces the previous panel instead of leaving two
  // sets of listeners behind.
  function mount(root) {
    const panel = createPanel(root || document);
    panel.wire();
    return panel;
  }

  // The helpers are exposed for the page, for tests and for a router that wants
  // to reuse the table rendering. The file stays self-contained: it is one
  // script tag in the browser, and the same file is requireable under Node's
  // test runner, so the pure half can be pinned without a DOM. The two guards
  // are what make both work.
  const exported = {
    mount,
    createPanel,
    configPaths,
    isRequiredTool,
    knownTools,
    configBody,
    readError,
    TEMPLATE,
    LOG_LEVELS,
    CONFIG_FIELDS,
    REQUIRED_TOOLS,
    TOOL_NOTES,
    FEATURE_NOTES,
  };
  if (typeof window !== "undefined") {
    window.OKEConfig = exported;
  }
  if (typeof module !== "undefined" && module.exports) {
    module.exports = exported;
  }

  // The standalone page wires itself. A router mounts the panel explicitly, so
  // the auto-wiring only happens when this page's own container is present.
  if (typeof document !== "undefined") {
    const own = document.getElementById("config-root");
    if (own) {
      mount(own);
    }
  }
})();
