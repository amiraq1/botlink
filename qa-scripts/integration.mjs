// End-to-end integration against the real BOTLINK server and a loopback fake
// provider. No real key and no paid request: the fake only answers locally.
const BASE = process.env.BOTLINK_BASE || "http://127.0.0.1:8099";
const FAKE = process.env.FAKE_BASE || "http://127.0.0.1:8098";
const PASSWORD = process.env.ADMIN_PASSWORD;
const FAKE_KEY = "sk-fake-integration-key-000000";

function requireLoopback(name, value) {
  const url = new URL(value);
  const host = url.hostname;
  if (host !== "localhost" && host !== "127.0.0.1" && host !== "::1") {
    throw new Error(`${name} must be a loopback origin (localhost, 127.0.0.1 or ::1), got ${value}`);
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new Error(`${name} must be http(s), got ${value}`);
  }
}

requireLoopback("BOTLINK_BASE", BASE);
requireLoopback("FAKE_BASE", FAKE);
if (!PASSWORD) throw new Error("ADMIN_PASSWORD is required for the QA run");

let cookie = "";
const results = [];

function check(name, ok, detail) {
  results.push({ name, ok: Boolean(ok), detail });
  if (!ok) throw new Error(`FAILED: ${name} — ${JSON.stringify(detail)}`);
  console.log(`PASS ${name}${detail === undefined ? "" : ` :: ${JSON.stringify(detail)}`}`);
}

async function raw(method, path, body, base = BASE) {
  const res = await fetch(base + path, {
    method,
    headers: {
      ...(body === undefined ? {} : { "content-type": "application/json" }),
      ...(cookie ? { cookie } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }
  return { res, data };
}

async function api(method, path, body) {
  const { res, data } = await raw(method, path, body);
  if (!res.ok) {
    throw new Error(`${method} ${path} -> ${res.status}: ${JSON.stringify(data)}`);
  }
  return data;
}

async function fake(method, path, body) {
  const { res, data } = await raw(method, path, body, FAKE);
  if (!res.ok) throw new Error(`fake ${method} ${path} -> ${res.status}`);
  return data;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function listRuns(gid) {
  return api("GET", `/api/admin/groups/${gid}/agent-runs`);
}

async function detail(gid) {
  return api("GET", `/api/admin/groups/${gid}`);
}

async function waitRun(gid, pred, { afterId = 0, timeoutMs = 20000 } = {}) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const runs = await listRuns(gid);
    const found = runs.find((r) => r.id > afterId && pred(r));
    if (found) return found;
    await sleep(150);
  }
  throw new Error(`no run matched within ${timeoutMs}ms (group ${gid})`);
}

async function waitStatus(gid, status, timeoutMs = 10000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const d = await detail(gid);
    if (d.group.status === status) return d;
    await sleep(150);
  }
  throw new Error(`group ${gid} never reached ${status}`);
}

async function createManagedGroup({ name, agentName, model, budget }) {
  const gid = (
    await api("POST", "/api/admin/groups", {
      name,
      topic: "fake provider integration",
      turnMode: "leader_round",
      maxTurns: 50,
      members: [
        { name: "Alpha", roleTitle: "مراقب" },
        { name: "Beta", roleTitle: "مراقب" },
      ],
    })
  ).id;
  const agent = await api("POST", `/api/admin/groups/${gid}/managed-agents`, {
    member: { name: agentName, roleTitle: "وكيل تكامل" },
    agent: {
      connectionId: connection.id,
      model,
      persona: "أجب بإيجاز.",
      maxOutputTokens: 128,
      contextMaxMessages: 20,
      contextMaxChars: 2000,
      dailyRunLimit: 50,
      dailyBudgetMicros: budget ?? null,
      enabled: true,
    },
  });
  return { gid, agent };
}

// ---------------------------------------------------------------------------
await fake("POST", "/__reset");

const login = await raw("POST", "/api/auth/login", { password: PASSWORD });
check("admin login", login.res.status === 200, login.res.status);
const setCookie = login.res.headers.getSetCookie?.() ?? [];
check("admin session cookie", setCookie.length > 0, setCookie.length);
cookie = setCookie[0].split(";")[0];

const existingConnections = await api("GET", "/api/admin/llm/connections");
// Reuse only the dedicated QA connection. Refusing other connections keeps a
// test run from overwriting prices on a real development/production database.
const qaConnection = existingConnections.find((c) => c.label === "fake-provider");
if (!qaConnection && existingConnections.length > 0) {
  throw new Error(
    "This database already has a non-QA LLM connection; use a dedicated test database instead",
  );
}
const connection =
  qaConnection ??
  (await api("POST", "/api/admin/llm/connections", {
    provider: "deepseek",
    label: "fake-provider",
    apiKey: FAKE_KEY,
    modelHint: "deepseek-flash",
  }));
const connJSON = JSON.stringify(connection);
check("connection created without exposing key", !connJSON.includes(FAKE_KEY) && !("apiKey" in connection), connection);

const listedConns = JSON.stringify(await api("GET", "/api/admin/llm/connections"));
check("connection list hides key", !listedConns.includes(FAKE_KEY));

const connCheck = await api("POST", `/api/admin/llm/connections/${connection.id}/check`);
check("provider model check reaches fake", connCheck.status === "ok" && connCheck.models?.length === 2, connCheck);

await api("PUT", `/api/admin/llm/connections/${connection.id}/prices/deepseek-flash`, {
  inputMicrosPer1M: 270000,
  outputMicrosPer1M: 1100000,
  currency: "USD",
});
const prices = await api("GET", `/api/admin/llm/connections/${connection.id}/prices`);
check("price stored", prices.length === 1 && prices[0].inputMicrosPer1M === 270000 && prices[0].outputMicrosPer1M === 1100000, prices);

// A. Full success cycle ------------------------------------------------------
const main = await createManagedGroup({ name: "Integration Main", agentName: "Writer", model: "deepseek-flash" });
await api("POST", `/api/admin/groups/${main.gid}/leader-message`, { text: "@Writer اكتب تحديثاً قصيراً" });
const okRun = await waitRun(main.gid, (r) => r.status === "completed");
const okDetail = await detail(main.gid);
const okReply = okDetail.messages.find((m) => m.turnSeq === okRun.turnSeq && m.senderBotId === main.agent.botId);
check("successful run completed with message", okRun.messageId != null && okReply?.content === "رد وهمي رقم 1", {
  runId: okRun.id,
  content: okReply?.content,
});
check("usage tokens recorded", okRun.promptTokens === 1000 && okRun.completionTokens === 500 && okRun.totalTokens === 1500, okRun);
check("cost computed from admin price", okRun.costMicros === 820 && okRun.costBasis === "usage" && okRun.costConfirmed === false, {
  costMicros: okRun.costMicros,
  costBasis: okRun.costBasis,
});
const usageOK = await api("GET", `/api/admin/groups/${main.gid}/agents/${main.agent.botId}/usage`);
check("usage summary after success", usageOK.spentMicros === 820 && usageOK.costKnown === true && usageOK.budgetState === "unset", usageOK);
const beforeReprice = okRun.costMicros;
await api("PUT", `/api/admin/llm/connections/${connection.id}/prices/deepseek-flash`, {
  inputMicrosPer1M: 1000000,
  outputMicrosPer1M: 2000000,
  currency: "USD",
});
const afterReprice = (await listRuns(main.gid)).find((r) => r.id === okRun.id);
check("price change does not reprice old run", afterReprice.costMicros === beforeReprice, {
  before: beforeReprice,
  after: afterReprice.costMicros,
});
await api("PUT", `/api/admin/llm/connections/${connection.id}/prices/deepseek-flash`, {
  inputMicrosPer1M: 270000,
  outputMicrosPer1M: 1100000,
  currency: "USD",
});

// B. Provider failure + explicit retry ---------------------------------------
await fake("POST", "/__mode", { mode: "fail" });
const failBase = Math.max(...(await listRuns(main.gid)).map((r) => r.id));
const callsBeforeFail = (await fake("GET", "/__stats")).chatCalls;
await api("POST", `/api/admin/groups/${main.gid}/leader-message`, { text: "@Writer سيحاول" });
const msgsAfterFailSend = (await detail(main.gid)).messages.length;
const failed = await waitRun(main.gid, (r) => r.status === "failed" && r.id > failBase);
await sleep(1800);
const runsAfterFail = await listRuns(main.gid);
const statsAfterFail = await fake("GET", "/__stats");
check("provider failure recorded once (no auto retry)", runsAfterFail.filter((r) => r.id > failBase).length === 1 && statsAfterFail.chatCalls - callsBeforeFail === 1, {
  newRuns: runsAfterFail.filter((r) => r.id > failBase).length,
  chatCalls: statsAfterFail.chatCalls - callsBeforeFail,
});
check("failed run stores no reply", failed.messageId == null && (await detail(main.gid)).messages.length === msgsAfterFailSend, {
  errorCode: failed.errorCode,
  messageId: failed.messageId,
});
await fake("POST", "/__mode", { mode: "normal" });
await api("POST", `/api/admin/groups/${main.gid}/agent-runs/${failed.id}/retry`);
const retried = await waitRun(main.gid, (r) => r.status === "completed" && r.retryOf === failed.id);
check("explicit retry creates a new attempt", retried.id !== failed.id && retried.attempt === failed.attempt + 1, {
  failed: failed.id,
  retried: retried.id,
  attempt: retried.attempt,
});
check("retry reply stored and priced", retried.messageId != null && retried.costMicros === 820, retried);
const usageUnknown = await api("GET", `/api/admin/groups/${main.gid}/agents/${main.agent.botId}/usage`);
check("failed run makes cost unknown but no budget means not blocked", usageUnknown.costKnown === false && usageUnknown.budgetState === "unset" && !usageUnknown.budgetBlocked, usageUnknown);

// C. Pause / resume / skip / stop while running ------------------------------
await fake("POST", "/__mode", { mode: "delay", delayMs: 6000 });
let lastRun = Math.max(...(await listRuns(main.gid)).map((r) => r.id));
await api("POST", `/api/admin/groups/${main.gid}/leader-message`, { text: "@Writer اكتب ببطء" });
const msgCount = (await detail(main.gid)).messages.length;
const pauseRun = await waitRun(main.gid, (r) => r.status === "running" && r.id > lastRun);
await api("POST", `/api/admin/groups/${main.gid}/pause`);
const pausedRun = await waitRun(main.gid, (r) => r.id === pauseRun.id && r.status === "cancelled");
await waitStatus(main.gid, "paused");
check("pause cancels in-flight run without storing reply", pausedRun.messageId == null && (await detail(main.gid)).messages.length === msgCount, pausedRun);

await api("POST", `/api/admin/groups/${main.gid}/resume`);
await api("POST", `/api/admin/groups/${main.gid}/leader-message`, { text: "@Writer استئناف" });
const resumeMsgCount = (await detail(main.gid)).messages.length;
const resumedRun = await waitRun(main.gid, (r) => r.status === "running" && r.id > pauseRun.id);
await api("POST", `/api/admin/groups/${main.gid}/skip`);
const skippedRun = await waitRun(main.gid, (r) => r.id === resumedRun.id && r.status === "cancelled");
const afterSkip = await waitStatus(main.gid, "waiting_for_leader");
check("skip cancels in-flight run and moves turn", skippedRun.messageId == null && afterSkip.group.currentTurnBotId == null && afterSkip.messages.length === resumeMsgCount, {
  run: skippedRun.id,
  status: afterSkip.group.status,
});

lastRun = Math.max(...(await listRuns(main.gid)).map((r) => r.id));
await api("POST", `/api/admin/groups/${main.gid}/leader-message`, { text: "@Writer اكتب قبل الإيقاف" });
const msgCountAtStop = (await detail(main.gid)).messages.length;
const stopRun = await waitRun(main.gid, (r) => r.status === "running" && r.id > lastRun);
await api("POST", `/api/admin/groups/${main.gid}/stop`);
const stoppedRun = await waitRun(main.gid, (r) => r.id === stopRun.id && r.status === "cancelled");
const stopped = await waitStatus(main.gid, "stopped");
await sleep(6500);
const finalRuns = await listRuns(main.gid);
const finalDetail = await detail(main.gid);
check("stop cancels run, late reply rejected", stoppedRun.messageId == null && finalDetail.messages.length === msgCountAtStop && finalRuns.every((r) => r.status !== "running") && finalDetail.group.status === "stopped", {
  stoppedRun: stoppedRun.status,
  messages: finalDetail.messages.length,
  running: finalRuns.filter((r) => r.status === "running").length,
});
const fakeStats = await fake("GET", "/__stats");
check("fake provider saw aborted in-flight calls", fakeStats.aborted > 0, { aborted: fakeStats.aborted });

// D. Missing price blocks a budgeted agent -----------------------------------
const budgeted = await createManagedGroup({ name: "Integration No Price", agentName: "Priceless", model: "deepseek-chat", budget: 1000 });
await fake("POST", "/__mode", { mode: "normal" });
await api("POST", `/api/admin/groups/${budgeted.gid}/leader-message`, { text: "@Priceless اكتب" });
const unknownRun = await waitRun(budgeted.gid, (r) => r.status === "completed");
const usageBudget = await api("GET", `/api/admin/groups/${budgeted.gid}/agents/${budgeted.agent.botId}/usage`);
check("missing price keeps run cost null", unknownRun.costMicros == null && unknownRun.costBasis !== "usage", {
  costMicros: unknownRun.costMicros,
  costBasis: unknownRun.costBasis,
});
check("missing price blocks budget as cost_unknown", usageBudget.costKnown === false && usageBudget.budgetBlocked === true && usageBudget.budgetBlockReason === "cost_unknown", usageBudget);
const runsBeforeBlocked = (await listRuns(budgeted.gid)).length;
await api("POST", `/api/admin/groups/${budgeted.gid}/leader-message`, { text: "@Priceless محاولة أخرى" });
const msgsAfterBlockedSend = (await detail(budgeted.gid)).messages.length;
await sleep(2500);
check("budgeted agent starts no new run while cost unknown", (await listRuns(budgeted.gid)).length === runsBeforeBlocked && (await detail(budgeted.gid)).messages.length === msgsAfterBlockedSend, {
  runs: (await listRuns(budgeted.gid)).length,
  messages: (await detail(budgeted.gid)).messages.length,
});

// E. External agent room continuity + stale reply rejection ------------------
const externalGid = (
  await api("POST", "/api/admin/groups", {
    name: "Integration External",
    topic: "external room",
    turnMode: "auto_round_robin",
    maxTurns: 50,
    members: [{ name: "ExtOne" }, { name: "ExtTwo" }],
  })
).id;
const externalDetail = await detail(externalGid);
const extOne = externalDetail.members.find((m) => m.name === "ExtOne");
const room = await api("GET", `/api/room/${extOne.agentToken}`);
check("external room reachable", room.status === "running" && room.isYourTurn === true, {
  status: room.status,
  isYourTurn: room.isYourTurn,
});
const accepted = await api("POST", `/api/room/${extOne.agentToken}/reply`, {
  expectedTurnSeq: room.turnSeq,
  content: "رد خارجي حقيقي",
});
const stale = await api("POST", `/api/room/${extOne.agentToken}/reply`, {
  expectedTurnSeq: room.turnSeq,
  content: "رد متأخر",
});
const afterExternal = await detail(externalGid);
check("external agent reply accepted once; stale late reply rejected", accepted.accepted === true && stale.accepted === false && afterExternal.group.currentTurnBotId === "bot_2", {
  accepted: accepted.accepted,
  stale: stale.accepted,
  next: afterExternal.group.currentTurnBotId,
});
check("external group still running after reply", afterExternal.group.status === "running", afterExternal.group.status);

console.log("\nALL CHECKS PASSED");
console.log(JSON.stringify({ results, fakeStats }, null, 2));
