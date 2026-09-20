// api.js is the thin client of the frozen REST interface (internal/api). It is
// a separate file because it is the only part of the front end that performs
// I/O, which keeps tasks.js importable outside a browser and keeps the wire
// contract in one place: the endpoint paths, the request bodies and the error
// object are all here.
//
// The client owns no state and no policy: it turns an HTTP response into a
// value or an ApiError, and the caller decides what to do about it.

"use strict";

// The versioned prefix every route lives under (api.APIPrefix).
export const API_PREFIX = "/api/v1";

// ApiError is the server's error object ({"error":{summary,detail,field}})
// turned into an exception, so a caller can show the summary and expand the
// detail. The status code is kept because 409 is the one the UI reacts to.
export class ApiError extends Error {
  constructor(status, summary, detail, path, field) {
    super(summary + (detail ? "：" + detail : ""));
    this.name = "ApiError";
    this.status = status;
    this.summary = summary;
    this.detail = detail;
    this.path = path;
    this.field = field || "";
  }

  // conflict reports whether the queue refused the change. 409 is what the
  // delete of a running task, a refused move and a pool start without workers
  // all answer, and it is the one case worth wording specially.
  get conflict() {
    return this.status === 409;
  }
}

// describeError renders an error for a message. A conflict is reported with the
// server's own summary, because those are the operator-facing messages
// ("无法删除正在运行的任务", "无法移动任务", ...).
export function describeError(err) {
  if (err instanceof ApiError) {
    if (err.status === 0) {
      return err.summary + "：" + err.detail;
    }
    return err.detail ? err.summary + "：" + err.detail : err.summary;
  }
  return String((err && err.message) || err);
}

// QueueActions is the queue and pool half of the API. Every method returns the
// decoded body and throws an ApiError, so a caller never has to check a status
// code to find out whether the call worked.
export class QueueActions {
  constructor(base = API_PREFIX) {
    this.base = base;
  }

  async list() {
    return this.request("GET", "/tasks");
  }

  async status() {
    return this.request("GET", "/status");
  }

  async get(id) {
    return this.request("GET", "/tasks/" + encodeURIComponent(id));
  }

  async patch(id, body) {
    return this.request("PATCH", "/tasks/" + encodeURIComponent(id), body);
  }

  async remove(id) {
    return this.request("DELETE", "/tasks/" + encodeURIComponent(id));
  }

  async setEnabled(id, enabled) {
    return this.patch(id, { enabled: Boolean(enabled) });
  }

  // move applies one of the three position values the PATCH body accepts.
  // A refused move answers 409 (internal/api/tasks.go notMovable).
  async move(id, position) {
    return this.patch(id, { position });
  }

  async rename(id, name) {
    return this.patch(id, { name });
  }

  async startPool() {
    return this.request("POST", "/pool/start");
  }

  // stopPool stops the whole pool. timeoutSeconds bounds how long the server
  // waits for the workers to wind down before answering 202 with
  // stopped: false; zero uses the server's own default.
  async stopPool(timeoutSeconds) {
    const query =
      timeoutSeconds > 0 ? "?timeout_seconds=" + timeoutSeconds : "";
    return this.request("POST", "/pool/stop" + query);
  }

  async request(method, path, body) {
    const url = this.base + path;
    const options = {
      method,
      headers: { Accept: "application/json" },
    };
    if (body !== undefined) {
      options.headers["Content-Type"] = "application/json";
      options.body = JSON.stringify(body);
    }

    let response;
    try {
      response = await fetch(url, options);
    } catch (err) {
      throw new ApiError(0, "无法连接服务", String(err && err.message), url);
    }

    const text = await response.text();
    let payload = null;
    if (text !== "") {
      try {
        payload = JSON.parse(text);
      } catch (err) {
        if (response.ok) {
          throw new ApiError(
            response.status,
            "服务返回了无法解析的内容",
            text.slice(0, 200),
            url
          );
        }
      }
    }

    if (!response.ok) {
      const info = (payload && payload.error) || {};
      throw new ApiError(
        response.status,
        info.summary || defaultSummary(response.status),
        info.detail || "",
        url,
        info.field || ""
      );
    }
    return payload;
  }
}

// defaultSummary words a status the server did not explain. It mirrors the
// handlers' own wording for the cases the UI can hit without a JSON body.
function defaultSummary(status) {
  switch (status) {
    case 400:
      return "请求内容不合法";
    case 404:
      return "找不到接口";
    case 405:
      return "方法不允许";
    case 409:
      return "操作与当前状态冲突";
    case 500:
      return "服务内部错误";
    default:
      return "请求失败（HTTP " + status + "）";
  }
}
