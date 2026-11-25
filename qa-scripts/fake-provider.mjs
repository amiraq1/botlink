// Local fake DeepSeek provider for BOTLINK integration tests.
// Loopback only. Never used by the real server unless BOTLINK_DEV=1 and
// BOTLINK_DEV_PROVIDER_URL points here.
import http from "node:http";

const PORT = Number(process.env.FAKE_PORT || 8098);
const HOST = "127.0.0.1";

let mode = "normal"; // normal | fail | no_usage | delay
let delayMs = 4000;
const stats = {
  chatCalls: 0,
  modelCalls: 0,
  aborted: 0,
  models: [],
  lastMessages: 0,
};

function send(res, status, body) {
  if (res.writableEnded || res.destroyed) return;
  const payload = JSON.stringify(body);
  res.writeHead(status, {
    "content-type": "application/json",
    "content-length": Buffer.byteLength(payload),
  });
  res.end(payload);
}

function reply(index) {
  return {
    id: `chatcmpl-fake-${index}`,
    object: "chat.completion",
    created: Math.floor(Date.now() / 1000),
    model: "deepseek-flash",
    choices: [
      {
        index: 0,
        message: { role: "assistant", content: `رد وهمي رقم ${index}` },
        finish_reason: "stop",
      },
    ],
    usage: {
      prompt_tokens: 1000,
      completion_tokens: 500,
      total_tokens: 1500,
    },
  };
}

const server = http.createServer((req, res) => {
  const url = new URL(req.url, `http://${HOST}:${PORT}`);

  if (req.method === "GET" && url.pathname === "/models") {
    stats.modelCalls += 1;
    return send(res, 200, {
      object: "list",
      data: [
        { id: "deepseek-flash", object: "model", owned_by: "fake" },
        { id: "deepseek-chat", object: "model", owned_by: "fake" },
      ],
    });
  }

  if (req.method === "POST" && url.pathname === "/chat/completions") {
    let raw = "";
    req.on("data", (c) => {
      raw += c;
    });
    req.on("end", () => {
      stats.chatCalls += 1;
      try {
        const parsed = JSON.parse(raw || "{}");
        stats.models.push(parsed.model);
        stats.lastMessages = Array.isArray(parsed.messages)
          ? parsed.messages.length
          : 0;
      } catch {
        // ignore malformed body in the fake
      }
      const index = stats.chatCalls;

      if (mode === "fail") {
        return send(res, 500, {
          error: { message: "fake provider failure", type: "server_error" },
        });
      }

      const body =
        mode === "no_usage"
          ? (({ usage, ...rest }) => rest)(reply(index))
          : reply(index);

      if (mode === "delay") {
        const timer = setTimeout(() => send(res, 200, body), delayMs);
        req.on("aborted", () => {
          stats.aborted += 1;
          clearTimeout(timer);
        });
        res.on("close", () => {
          if (!res.writableEnded) stats.aborted += 1;
        });
        return;
      }
      return send(res, 200, body);
    });
    return;
  }

  if (req.method === "POST" && url.pathname === "/__mode") {
    let raw = "";
    req.on("data", (c) => {
      raw += c;
    });
    req.on("end", () => {
      try {
        const body = JSON.parse(raw || "{}");
        if (typeof body.mode === "string") mode = body.mode;
        if (typeof body.delayMs === "number") delayMs = body.delayMs;
        return send(res, 200, { ok: true, mode, delayMs });
      } catch {
        return send(res, 400, { error: "bad json" });
      }
    });
    return;
  }

  if (req.method === "POST" && url.pathname === "/__reset") {
    mode = "normal";
    delayMs = 4000;
    stats.chatCalls = 0;
    stats.modelCalls = 0;
    stats.aborted = 0;
    stats.models = [];
    stats.lastMessages = 0;
    return send(res, 200, { ok: true });
  }

  if (req.method === "GET" && url.pathname === "/__stats") {
    return send(res, 200, { mode, delayMs, ...stats });
  }

  send(res, 404, { error: "not found" });
});

server.listen(PORT, HOST, () => {
  console.log(`fake provider listening on http://${HOST}:${PORT}`);
});
