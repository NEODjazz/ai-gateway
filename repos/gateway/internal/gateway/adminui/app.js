(() => {
  "use strict";

  const state = { token: sessionStorage.getItem("ai_gateway_admin_token") || "", usage: null, customerUsage: null, customerScope: null, requestLogs: [], requestLogNextBefore: "", requestLogNextRequestID: "", requestLogSettings: null, routing: null, keys: [], users: [], teams: [], organizations: [], models: [], aiHub: [], costRecommendations: [], providers: [], credentials: [], deployments: [], modelGroups: [], guardrails: [], mcpServers: [], mcpToolsets: [], catalog: null, budgets: [], audit: [] };
  const $ = (id) => document.getElementById(id);
  const loginView = $("login-view");
  const consoleView = $("console-view");
  const loginForm = $("login-form");
  const tokenInput = $("admin-token");
  const loginError = $("login-error");
  const globalError = $("global-error");
  const pageTitles = { overview: "Overview", usage: "Usage & spend", customers: "Customer insights", "request-logs": "Request logs", routing: "Routing diagnostics", playground: "Chat playground", keys: "Virtual keys", users: "Users", teams: "Teams", organizations: "Organizations", models: "Models", "ai-hub": "AI Hub & optimization", deployments: "Providers & models", guardrails: "Guardrails", mcp: "MCP registry", budgets: "Budgets", audit: "Audit log" };
  let pendingConfirmation = null;

  function setText(id, value) { const element = $(id); if (element) element.textContent = value; }
  function clear(element) { while (element.firstChild) element.removeChild(element.firstChild); }
  function textCell(value, secondary) {
    const cell = document.createElement("td");
    const strong = document.createElement("strong");
    strong.textContent = value || "—";
    cell.appendChild(strong);
    if (secondary) { const small = document.createElement("small"); small.textContent = secondary; cell.appendChild(small); }
    return cell;
  }
  function plainCell(value) { const cell = document.createElement("td"); cell.textContent = value == null || value === "" ? "—" : String(value); return cell; }
  function formatDate(value) {
    if (!value) return "—";
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? String(value) : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(date);
  }
  function formatMoney(value, currency) {
    if (value == null) return "—";
    try { return new Intl.NumberFormat(undefined, { style: "currency", currency: currency || "USD", maximumFractionDigits: 4 }).format(value); }
    catch (_) { return `${value} ${currency || ""}`.trim(); }
  }
  function formatNumber(value) { return value == null ? "—" : new Intl.NumberFormat().format(value); }

  async function api(path, options = {}) {
    const headers = new Headers(options.headers || {});
    headers.set("Authorization", `Bearer ${state.token}`);
    headers.set("Accept", "application/json");
    const response = await fetch(path, { ...options, headers, cache: "no-store" });
    if (response.status === 401 || response.status === 403) {
      const error = new Error(response.status === 401 ? "Invalid or expired admin token." : "This credential does not have the admin role.");
      error.auth = true;
      throw error;
    }
    if (!response.ok) {
      let message = `Request failed (${response.status})`;
      try { const body = await response.json(); message = body?.error?.message || message; } catch (_) {}
      throw new Error(message);
    }
    if (response.status === 204) return null;
    return response.json();
  }

  async function apiJSON(path, method, body) {
    return api(path, { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  }

  function requestLogQuery(before = "", beforeRequestID = "") {
    const query = new URLSearchParams({ days: $("request-log-days").value, limit: "100" });
    for (const [name, id] of [["status", "request-log-status"], ["request_id", "request-log-request-id"], ["model", "request-log-model"], ["provider", "request-log-provider"], ["team_id", "request-log-team"], ["credential_id", "request-log-credential"]]) {
      const value = $(id).value.trim(); if (value) query.set(name, value);
    }
    if (before) { query.set("before", before); query.set("before_request_id", beforeRequestID); }
    return query;
  }

  async function loadData({ action = "" } = {}) {
    globalError.hidden = true;
    const query = new URLSearchParams({ limit: "100" });
    if (action.trim()) query.set("action", action.trim());
    const requests = [
      api(`/admin/v1/usage/report?days=${encodeURIComponent($("usage-days").value)}`),
      api("/admin/v1/keys?limit=100"),
      api("/v1/models"),
      api("/admin/v1/model-catalog"),
      api("/admin/v1/budgets"),
      api(`/admin/v1/audit/events?${query}`),
      api("/admin/v1/routing/diagnostics"),
      api(`/admin/v1/request-logs?${requestLogQuery()}`),
      api("/admin/v1/request-logs/settings"),
      api("/admin/v1/users?limit=100"),
      api("/admin/v1/teams?limit=100"),
      api("/admin/v1/model-deployments"),
      api("/admin/v1/guardrail-policies"),
      api("/admin/v1/mcp/servers"),
      api("/admin/v1/mcp/toolsets"),
      api("/admin/v1/organizations?limit=100"),
      api("/admin/v1/ai-hub/models"),
      api("/admin/v1/cost-optimization/recommendations"),
      api("/admin/v1/providers"),
      api("/admin/v1/credentials"),
      api("/admin/v1/model-groups"),
    ];
    const results = await Promise.allSettled(requests);
    const authFailure = results.find((result) => result.status === "rejected" && result.reason?.auth);
    if (authFailure) throw authFailure.reason;
    const errors = [];
    if (results[0].status === "fulfilled") state.usage = results[0].value; else errors.push(`Usage: ${results[0].reason.message}`);
    if (results[1].status === "fulfilled") state.keys = results[1].value?.data || []; else errors.push(`Virtual keys: ${results[1].reason.message}`);
    if (results[2].status === "fulfilled") state.models = results[2].value?.data || []; else errors.push(`Models: ${results[2].reason.message}`);
    if (results[3].status === "fulfilled") state.catalog = results[3].value; else errors.push(`Catalog: ${results[3].reason.message}`);
    if (results[4].status === "fulfilled") state.budgets = results[4].value?.data || []; else errors.push(`Budgets: ${results[4].reason.message}`);
    if (results[5].status === "fulfilled") state.audit = results[5].value?.data || []; else errors.push(`Audit: ${results[5].reason.message}`);
    if (results[6].status === "fulfilled") state.routing = results[6].value; else errors.push(`Routing: ${results[6].reason.message}`);
    if (results[7].status === "fulfilled") { state.requestLogs = results[7].value?.data || []; state.requestLogNextBefore = results[7].value?.next_before || ""; state.requestLogNextRequestID = results[7].value?.next_request_id || ""; } else errors.push(`Request logs: ${results[7].reason.message}`);
    if (results[8].status === "fulfilled") state.requestLogSettings = results[8].value; else errors.push(`Request log settings: ${results[8].reason.message}`);
    if (results[9].status === "fulfilled") state.users = results[9].value?.data || []; else errors.push(`Users: ${results[9].reason.message}`);
    if (results[10].status === "fulfilled") state.teams = results[10].value?.data || []; else errors.push(`Teams: ${results[10].reason.message}`);
    if (results[11].status === "fulfilled") state.deployments = results[11].value?.data || []; else errors.push(`Deployments: ${results[11].reason.message}`);
    if (results[12].status === "fulfilled") state.guardrails = results[12].value?.data || []; else errors.push(`Guardrails: ${results[12].reason.message}`);
    if (results[13].status === "fulfilled") state.mcpServers = results[13].value?.data || []; else errors.push(`MCP servers: ${results[13].reason.message}`);
    if (results[14].status === "fulfilled") state.mcpToolsets = results[14].value?.data || []; else errors.push(`MCP toolsets: ${results[14].reason.message}`);
    if (results[15].status === "fulfilled") state.organizations = results[15].value?.data || []; else errors.push(`Organizations: ${results[15].reason.message}`);
    if (results[16].status === "fulfilled") state.aiHub = results[16].value?.data || []; else errors.push(`AI Hub: ${results[16].reason.message}`);
    if (results[17].status === "fulfilled") state.costRecommendations = results[17].value?.data || []; else errors.push(`Cost optimization: ${results[17].reason.message}`);
    if (results[18].status === "fulfilled") state.providers = results[18].value?.data || []; else errors.push(`Providers: ${results[18].reason.message}`);
    if (results[19].status === "fulfilled") state.credentials = results[19].value?.data || []; else errors.push(`Credentials: ${results[19].reason.message}`);
    if (results[20].status === "fulfilled") state.modelGroups = results[20].value?.data || []; else errors.push(`Model groups: ${results[20].reason.message}`);
    renderAll();
    setText("console-health", errors.length ? "Degraded" : "Operational");
    if (errors.length) { globalError.textContent = errors.join(" · "); globalError.hidden = false; }
    const now = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date());
    setText("last-refresh", `Updated ${now}`);
  }

  function catalogEntry(model) {
    const entries = (state.catalog?.models || []).filter((item) => item.model === model.id);
    return entries.find((item) => item.provider === model.owned_by) || (entries.length === 1 ? entries[0] : {});
  }

  function renderAll() {
    const activeKeys = state.keys.filter((item) => !item.revoked_at && (!item.expires_at || new Date(item.expires_at) > new Date())).length;
    const activeBudgets = state.budgets.filter((item) => item.enabled).length;
    setText("stat-models", formatNumber(state.models.length));
    setText("stat-keys", formatNumber(activeKeys));
    setText("stat-catalog", formatNumber(state.catalog?.models?.length || 0));
    setText("stat-budgets", formatNumber(activeBudgets));
    setText("stat-audit", formatNumber(state.audit.length));
    setText("models-badge", formatNumber(state.models.length));
    setText("keys-badge", formatNumber(activeKeys));
    setText("budgets-badge", formatNumber(activeBudgets));
    setText("request-logs-badge", formatNumber(state.requestLogs.length));
    setText("users-badge", formatNumber(state.users.length));
    setText("teams-badge", formatNumber(state.teams.length));
    setText("organizations-badge", formatNumber(state.organizations.filter((item)=>item.status==="active").length));
    setText("deployments-badge", formatNumber(state.deployments.filter((item)=>item.enabled).length));
    setText("guardrails-badge", formatNumber(state.guardrails.filter((item)=>item.enabled).length));
    setText("mcp-badge", formatNumber(state.mcpServers.filter((item)=>item.enabled).length));
    setText("catalog-version", state.catalog?.version ? `Version ${state.catalog.version}` : "Runtime registry");
    renderOverview();
    renderRouting();
    renderPlaygroundModels();
    renderUsage();
    renderCustomer();
    renderRequestLogs();
    renderKeys();
    renderUsers();
    renderTeams();
    renderOrganizations();
    renderAIHub();
    renderProviders();
    renderCredentials();
    renderDeployments();
    renderModelGroups();
    renderGuardrails();
    renderMCP();
    renderModels();
    renderBudgets();
    renderAudit();
  }

  function renderOverview() {
    const models = $("overview-models"); clear(models); models.classList.remove("loading-block");
    if (!state.models.length) { models.textContent = "No models are available."; models.classList.add("loading-block"); }
    for (const model of state.models.slice(0, 5)) {
      const row = document.createElement("div"); row.className = "list-row";
      const info = document.createElement("div");
      const name = document.createElement("strong"); name.textContent = model.id;
      const owner = document.createElement("small"); owner.textContent = model.owned_by || "Unassigned provider";
      info.append(name, owner);
      const tag = document.createElement("span"); tag.className = "provider-tag"; tag.textContent = "model";
      row.append(info, tag); models.appendChild(row);
    }
    const audit = $("overview-audit"); clear(audit); audit.classList.remove("loading-block");
    if (!state.audit.length) { audit.textContent = "No management activity yet."; audit.classList.add("loading-block"); }
    for (const event of state.audit.slice(0, 5)) {
      const item = document.createElement("div"); item.className = "timeline-item";
      const title = document.createElement("strong"); title.textContent = event.action || "Management event";
      const detail = document.createElement("small"); detail.textContent = `${event.outcome || "unknown"} · ${formatDate(event.occurred_at)}`;
      item.append(title, detail); audit.appendChild(item);
    }
  }

  function renderRouting() {
    setText("routing-strategy", `Strategy ${state.routing?.strategy || "—"}`);
    const container = $("routing-cards"); clear(container); container.classList.remove("loading-block");
    const endpoints = state.routing?.endpoints || [];
    if (!endpoints.length) { container.textContent = "No routing endpoints are available."; container.classList.add("loading-block"); return; }
    for (const endpoint of endpoints) {
      const card = document.createElement("article"); card.className = "panel routing-card";
      const heading = document.createElement("div"); heading.className = "routing-card-heading";
      const identity = document.createElement("div"); const name = document.createElement("h3"); name.textContent = endpoint.name; const type = document.createElement("small"); type.textContent = `${endpoint.type} · priority ${endpoint.priority} · weight ${endpoint.weight || 1}`; identity.append(name, type);
      const status = document.createElement("span"); status.className = `outcome ${endpoint.state === "available" ? "succeeded" : endpoint.state === "half_open" ? "attempted" : "failed"}`; status.textContent = endpoint.state.replaceAll("_", " "); heading.append(identity, status);
      const metrics = document.createElement("div"); metrics.className = "routing-metrics";
      for (const [label, value] of [["Latency EWMA", endpoint.samples ? `${Math.round(endpoint.latency_ewma_ms)} ms` : "No samples"], ["Failure EWMA", endpoint.samples ? `${(endpoint.failure_ewma * 100).toFixed(1)}%` : "—"], ["In flight", `${endpoint.in_flight_requests}/${endpoint.max_parallel_requests || "∞"}`], ["Queue", `${endpoint.queued_requests}/${endpoint.queue_capacity || 0}`]]) { const item = document.createElement("div"); const small = document.createElement("small"); small.textContent = label; const strong = document.createElement("strong"); strong.textContent = value; item.append(small, strong); metrics.appendChild(item); }
      const tags = document.createElement("div"); tags.className = "capabilities";
      for (const value of [...(endpoint.capabilities || []), ...(endpoint.models || []).map((model) => `model:${model}`), endpoint.dlp_enabled ? "DLP" : "", endpoint.av_enabled ? "AV" : "", endpoint.shadow ? `shadow:${endpoint.mirror_percentage || 0}%` : ""].filter(Boolean)) { const tag = document.createElement("span"); tag.className = "capability"; tag.textContent = value; tags.appendChild(tag); }
      card.append(heading, metrics, tags); container.appendChild(card);
    }
  }

  function renderPlaygroundModels() {
    const select = $("playground-model"); const selected = select.value; clear(select);
    for (const model of state.models) { const option = document.createElement("option"); option.value = model.id; option.textContent = `${model.id} · automatic routing`; select.appendChild(option); }
    if (Array.from(select.options).some((option) => option.value === selected)) select.value = selected;
  }

  function responseText(content) {
    if (typeof content === "string") return content;
    if (Array.isArray(content)) return content.filter((item) => item?.type === "text" || item?.type === "output_text").map((item) => item.text || "").join("\n");
    return content == null ? "" : JSON.stringify(content, null, 2);
  }

  async function runPlayground(event) {
    event.preventDefault(); const error = $("playground-error"); error.hidden = true;
    const submit = $("playground-submit"); submit.disabled = true; submit.textContent = "Running…";
    const messages = []; const system = $("playground-system").value.trim(); if (system) messages.push({ role: "system", content: system }); messages.push({ role: "user", content: $("playground-message").value });
    const started = performance.now();
    try {
      const response = await fetch("/v1/chat/completions", { method: "POST", cache: "no-store", headers: { "Authorization": `Bearer ${state.token}`, "Accept": "application/json", "Content-Type": "application/json" }, body: JSON.stringify({ model: $("playground-model").value, messages, temperature: Number($("playground-temperature").value), max_tokens: Number($("playground-max-tokens").value) }) });
      const body = await response.json(); if (!response.ok) throw new Error(body?.error?.message || `Request failed (${response.status})`);
      $("playground-result").textContent = responseText(body.choices?.[0]?.message?.content) || JSON.stringify(body, null, 2);
      const usage = body.usage || {}; setText("playground-meta", `${Math.round(performance.now() - started)} ms · ${formatNumber(usage.total_tokens || 0)} tokens · ${response.headers.get("X-Request-ID") || "no request id"}`);
    } catch (requestError) { error.textContent = requestError.message; error.hidden = false; }
    finally { submit.disabled = false; submit.textContent = "Run request"; }
  }

  function renderUsage() {
    const totals = state.usage?.totals || [];
    const requests = totals.reduce((sum, item) => sum + Number(item.requests || 0), 0);
    const errors = totals.reduce((sum, item) => sum + Number(item.errors || 0), 0);
    const tokens = totals.reduce((sum, item) => sum + Number(item.total_tokens || 0), 0);
    const latencyWeight = totals.reduce((sum, item) => sum + Number(item.avg_latency_ms || 0) * Number(item.requests || 0), 0);
    setText("usage-requests", formatNumber(requests));
    setText("usage-error-rate", requests ? `${(errors / requests * 100).toFixed(1)}% errors` : "No final outcomes");
    setText("usage-tokens", formatNumber(tokens));
    setText("usage-spend", totals.length ? totals.map((item) => formatMoney(item.cost, item.currency)).join(" · ") : "—");
    setText("usage-latency", requests ? `${formatNumber(Math.round(latencyWeight / requests))} ms` : "—");

    const chart = $("usage-chart"); clear(chart); chart.classList.remove("loading-block");
    const byDate = new Map();
    for (const item of state.usage?.daily || []) byDate.set(item.date, (byDate.get(item.date) || 0) + Number(item.total_tokens || 0));
    const daily = Array.from(byDate, ([date, value]) => ({ date, value })).sort((a, b) => a.date.localeCompare(b.date));
    const maxValue = Math.max(1, ...daily.map((item) => item.value));
    if (!daily.length) { chart.textContent = "No usage events in this window."; chart.classList.add("loading-block"); }
    for (const item of daily) {
      const column = document.createElement("div"); column.className = "usage-bar-column"; column.title = `${item.date}: ${formatNumber(item.value)} tokens`;
      const value = document.createElement("span"); value.textContent = formatNumber(item.value);
      const track = document.createElement("div"); track.className = "usage-bar-track";
      const bar = document.createElement("div"); bar.className = "usage-bar"; bar.style.height = `${Math.max(3, item.value / maxValue * 100)}%`; track.appendChild(bar);
      const label = document.createElement("small"); label.textContent = item.date.slice(5);
      column.append(value, track, label); chart.appendChild(column);
    }
    renderUsageBreakdown("usage-models-table", state.usage?.by_model || []);
    renderUsageBreakdown("usage-providers-table", state.usage?.by_provider || []);
  }

  function renderUsageBreakdown(id, rows) {
    const body = $(id); clear(body);
    for (const item of rows.slice(0, 20)) {
      const row = document.createElement("tr"); row.appendChild(textCell(item.name)); row.appendChild(plainCell(formatNumber(item.requests))); row.appendChild(plainCell(formatNumber(item.total_tokens))); row.appendChild(plainCell(formatMoney(item.cost, item.currency))); body.appendChild(row);
    }
    if (!rows.length) { const row = document.createElement("tr"); const cell = document.createElement("td"); cell.colSpan = 4; cell.className = "muted"; cell.textContent = "No usage data."; row.appendChild(cell); body.appendChild(row); }
  }

  function renderCustomer() {
    if (!state.customerScope || !state.customerUsage) { $("customer-results").hidden=true; return; }
    $("customer-results").hidden=false;
    const totals=state.customerUsage.totals||[],requests=totals.reduce((sum,item)=>sum+Number(item.requests||0),0),errors=totals.reduce((sum,item)=>sum+Number(item.errors||0),0),tokens=totals.reduce((sum,item)=>sum+Number(item.total_tokens||0),0);
    setText("customer-requests",formatNumber(requests));setText("customer-tokens",formatNumber(tokens));setText("customer-spend",totals.length?totals.map((item)=>formatMoney(item.cost,item.currency)).join(" · "):"—");setText("customer-error-rate",requests?`${(errors/requests*100).toFixed(1)}%`:"—");
    const scope=state.customerScope;
    const budgets=state.budgets.filter((item)=>item.scope_type==="global"||(item.scope_type===scope.type&&item.scope_id===scope.id));
    const budgetBody=$("customer-budgets-table");clear(budgetBody);$("customer-budgets-empty").hidden=budgets.length!==0;
    for(const budget of budgets){const row=document.createElement("tr");row.appendChild(textCell(`${budget.scope_type}: ${budget.scope_id}`,budget.period));row.appendChild(plainCell(formatMoney(budget.max_cost,budget.currency)));row.appendChild(plainCell(formatNumber(budget.max_tokens)));row.appendChild(statusCell(budget.enabled));budgetBody.appendChild(row)}
    const keys=state.keys.filter((item)=>scope.type==="key"?item.id===scope.id:scope.type==="user"?item.user_id===scope.id:item.team_id===scope.id);
    const keyBody=$("customer-keys-table");clear(keyBody);$("customer-keys-empty").hidden=keys.length!==0;
    for(const key of keys){const active=!key.revoked_at&&!key.disabled_at&&(!key.expires_at||new Date(key.expires_at)>new Date());const row=document.createElement("tr");row.appendChild(textCell(key.alias||key.id,key.id));row.appendChild(plainCell(formatNumber(key.rate_limit_rpm||0)));row.appendChild(plainCell(formatNumber(key.rate_limit_tpm||0)));row.appendChild(statusCell(active));keyBody.appendChild(row)}
  }

  async function loadCustomer(event){event.preventDefault();const type=$("customer-scope-type").value,id=$("customer-scope-id").value.trim(),days=$("customer-days").value;try{state.customerUsage=await api(`/admin/v1/customers/${encodeURIComponent(type)}/${encodeURIComponent(id)}/usage?days=${encodeURIComponent(days)}`);state.customerScope={type,id};renderCustomer();showToast("Customer usage loaded")}catch(error){globalError.textContent=error.message;globalError.hidden=false}}

  function requestLogEndpoint(log) { return log.provider_endpoint_name || log.provider || "—"; }

  function renderRequestLogs() {
    const body = $("request-logs-table"); clear(body); $("request-logs-empty").hidden = state.requestLogs.length !== 0;
    const settings = state.requestLogSettings;
    setText("request-log-privacy", settings ? `Content storage ${settings.content_stored ? "on" : "off"} · ${settings.retention_days}d retention` : "Content storage off");
    for (const log of state.requestLogs) {
      const row = document.createElement("tr");
      row.appendChild(textCell(formatDate(log.timestamp), log.request_id));
      const outcomeCell = document.createElement("td"); const outcome = document.createElement("span"); outcome.className = `outcome ${log.status === "ok" ? "succeeded" : "failed"}`; outcome.textContent = log.status === "ok" ? "Success" : log.failure_class || "Error"; outcomeCell.appendChild(outcome); row.appendChild(outcomeCell);
      row.appendChild(textCell(log.model, `${requestLogEndpoint(log)} · ${log.api_type || "request"}`));
      row.appendChild(textCell(log.user_id || "—", log.team_id || log.credential_id || ""));
      row.appendChild(textCell(formatNumber(log.total_tokens), `${formatNumber(log.input_tokens)} in · ${formatNumber(log.output_tokens)} out`));
      row.appendChild(textCell(`${formatNumber(log.latency_ms)} ms`, log.cache_status ? `cache ${log.cache_status}` : ""));
      row.appendChild(plainCell(formatMoney(log.cost, log.currency)));
      const actions = document.createElement("td"); actions.className = "row-actions"; const details = document.createElement("button"); details.type = "button"; details.className = "row-button"; details.textContent = "Details"; details.addEventListener("click", () => openRequestLog(log.request_id)); actions.appendChild(details); row.appendChild(actions);
      body.appendChild(row);
    }
    $("request-logs-more").hidden = !state.requestLogNextBefore;
  }

  async function loadRequestLogs(append = false) {
    const page = await api(`/admin/v1/request-logs?${requestLogQuery(append ? state.requestLogNextBefore : "", append ? state.requestLogNextRequestID : "")}`);
    state.requestLogs = append ? state.requestLogs.concat(page.data || []) : (page.data || []);
    state.requestLogNextBefore = page.next_before || "";
    state.requestLogNextRequestID = page.next_request_id || "";
    renderRequestLogs(); setText("request-logs-badge", formatNumber(state.requestLogs.length));
  }

  async function openRequestLog(requestID) {
    try {
      const log = await api(`/admin/v1/request-logs/${encodeURIComponent(requestID)}`);
      setText("request-log-dialog-title", log.request_id || "Request details");
      const detail = $("request-log-detail"); clear(detail);
      const fields = [["Timestamp", formatDate(log.timestamp)], ["Outcome", log.status], ["Failure class", log.failure_class], ["API type", log.api_type], ["Model", log.model], ["Endpoint", requestLogEndpoint(log)], ["Endpoint type", log.provider_endpoint_type], ["User", log.user_id], ["Team", log.team_id], ["Credential fingerprint", log.credential_id], ["Tokens", `${formatNumber(log.input_tokens)} input · ${formatNumber(log.output_tokens)} output · ${formatNumber(log.total_tokens)} total`], ["Latency", `${formatNumber(log.latency_ms)} ms`], ["Cache", log.cache_status], ["Cost", formatMoney(log.cost, log.currency)], ["Content stored", log.content_stored ? "Yes" : "No"]];
      for (const [label, value] of fields) { const item = document.createElement("div"); const key = document.createElement("small"); key.textContent = label; const data = document.createElement("strong"); data.textContent = value || "—"; item.append(key, data); detail.appendChild(item); }
      $("request-log-dialog").showModal();
    } catch (error) { globalError.textContent = error.message; globalError.hidden = false; }
  }

  function renderKeys() {
    const body = $("keys-table"); clear(body); $("keys-empty").hidden = state.keys.length !== 0;
    for (const key of state.keys) {
      const row = document.createElement("tr");
      row.appendChild(textCell(key.alias || key.id, `${key.id} · Created ${formatDate(key.created_at)}`));
      row.appendChild(textCell(key.user_id, key.team_id));
      const grants = [...(key.roles || []), ...(key.allowed_models || []).map((value) => `model:${value}`), ...(key.allowed_tools || []).map((value) => `tool:${value}`)];
      row.appendChild(plainCell(grants.join(", ") || "Unscoped"));
      row.appendChild(textCell(key.rate_limit_rpm ? `${formatNumber(key.rate_limit_rpm)} RPM` : "No RPM limit", key.rate_limit_tpm ? `${formatNumber(key.rate_limit_tpm)} TPM` : "No TPM limit"));
      const expired = key.expires_at && new Date(key.expires_at) <= new Date();
      const active = !key.revoked_at && !expired;
      const statusCell = document.createElement("td"); const status = document.createElement("span"); status.className = `outcome ${active && !key.disabled_at ? "succeeded" : "failed"}`; status.textContent = key.revoked_at ? "Revoked" : expired ? "Expired" : key.disabled_at ? "Disabled" : "Active"; statusCell.appendChild(status); row.appendChild(statusCell);
      row.appendChild(plainCell(formatDate(key.last_used_at)));
      const actions = document.createElement("td"); actions.className = "row-actions";
      if (active) {
        const edit = document.createElement("button"); edit.type = "button"; edit.className = "row-button"; edit.textContent = "Edit"; edit.addEventListener("click", () => openKeyDialog(key, "edit"));
        const rotate = document.createElement("button"); rotate.type = "button"; rotate.className = "row-button"; rotate.textContent = "Rotate"; rotate.addEventListener("click", () => openKeyDialog(key, "rotate"));
        const toggle = document.createElement("button"); toggle.type = "button"; toggle.className = "row-button"; toggle.textContent = key.disabled_at ? "Enable" : "Disable"; toggle.addEventListener("click", () => setKeyDisabled(key.id, !key.disabled_at));
        const usage = document.createElement("button"); usage.type = "button"; usage.className = "row-button"; usage.textContent = "Usage"; usage.addEventListener("click", () => showKeyUsage(key.id));
        const budget = document.createElement("button"); budget.type = "button"; budget.className = "row-button"; budget.textContent = "Budget"; budget.addEventListener("click", () => openBudgetDialog({ scope_type: "key", scope_id: key.id, period: "month", currency: "USD", enabled: true }));
        const revoke = document.createElement("button"); revoke.type = "button"; revoke.className = "row-button danger"; revoke.textContent = "Revoke"; revoke.addEventListener("click", () => confirmChange("Revoke virtual key?", `${key.id} will stop authorizing requests immediately.`, () => revokeKey(key.id)));
        actions.append(edit, rotate, toggle, usage, budget, revoke);
      }
      row.appendChild(actions); body.appendChild(row);
    }
  }

  function renderModels() {
    const body = $("models-table"); clear(body);
    const query = $("model-search").value.trim().toLowerCase();
    const union = new Map(state.models.map((model) => [`${model.owned_by}\u0000${model.id}`, model]));
    for (const item of state.catalog?.models || []) { const key = `${item.provider}\u0000${item.model}`; if (!union.has(key)) union.set(key, { id: item.model, object: "catalog", owned_by: item.provider }); }
    const visible = Array.from(union.values()).filter((model) => `${model.id} ${model.owned_by || ""}`.toLowerCase().includes(query));
    $("models-empty").hidden = visible.length !== 0;
    for (const model of visible) {
      const catalogItem = catalogEntry(model);
      const row = document.createElement("tr");
      row.appendChild(textCell(model.id, model.object));
      row.appendChild(plainCell(model.owned_by));
      const capabilities = document.createElement("td");
      const list = document.createElement("div"); list.className = "capabilities";
      for (const capability of catalogItem.capabilities || []) { const tag = document.createElement("span"); tag.className = "capability"; tag.textContent = capability; list.appendChild(tag); }
      if (!list.childElementCount) list.textContent = "—";
      capabilities.appendChild(list); row.appendChild(capabilities);
      row.appendChild(plainCell(formatMoney(catalogItem.input_cost_per_1m, catalogItem.currency)));
      row.appendChild(plainCell(formatMoney(catalogItem.output_cost_per_1m, catalogItem.currency)));
      const actions = document.createElement("td"); actions.className = "row-actions";
      if (catalogItem.model) {
        const edit = document.createElement("button"); edit.className = "row-button"; edit.type = "button"; edit.textContent = "Edit"; edit.addEventListener("click", () => openModelDialog(catalogItem));
        const remove = document.createElement("button"); remove.className = "row-button danger"; remove.type = "button"; remove.textContent = "Remove"; remove.addEventListener("click", () => confirmChange("Remove catalog entry?", `${catalogItem.provider}/${catalogItem.model} will be removed from the runtime registry.`, () => removeCatalogEntry(catalogItem)));
        actions.append(edit, remove);
      } else { const hint = document.createElement("small"); hint.textContent = "Not cataloged"; actions.appendChild(hint); }
      row.appendChild(actions);
      body.appendChild(row);
    }
  }

  function renderUsers() {
    const body = $("users-table"); clear(body); $("users-empty").hidden = state.users.length !== 0;
    for (const user of state.users) { const row=document.createElement("tr");row.appendChild(textCell(user.name||user.id,user.id));row.appendChild(plainCell(user.email));row.appendChild(plainCell((user.roles||[]).join(", ")||"—"));row.appendChild(plainCell((user.team_ids||[]).join(", ")||"—"));const statusCell=document.createElement("td");const status=document.createElement("span");status.className=`outcome ${user.status==="active"?"succeeded":"failed"}`;status.textContent=user.status;statusCell.appendChild(status);row.appendChild(statusCell);const actions=document.createElement("td");actions.className="row-actions";const edit=document.createElement("button");edit.type="button";edit.className="row-button";edit.textContent="Edit";edit.addEventListener("click",()=>openUserDialog(user));actions.appendChild(edit);row.appendChild(actions);body.appendChild(row); }
  }

  function renderTeams() {
    const body = $("teams-table"); clear(body); $("teams-empty").hidden = state.teams.length !== 0;
    for (const team of state.teams) { const row=document.createElement("tr");row.appendChild(textCell(team.name,team.id));row.appendChild(plainCell(team.description));row.appendChild(plainCell(formatNumber(team.member_count)));const statusCell=document.createElement("td");const status=document.createElement("span");status.className=`outcome ${team.status==="active"?"succeeded":"failed"}`;status.textContent=team.status;statusCell.appendChild(status);row.appendChild(statusCell);const actions=document.createElement("td");actions.className="row-actions";const edit=document.createElement("button");edit.type="button";edit.className="row-button";edit.textContent="Edit";edit.addEventListener("click",()=>openTeamDialog(team));const member=document.createElement("button");member.type="button";member.className="row-button";member.textContent="Add member";member.addEventListener("click",()=>openMembershipDialog(team.id));actions.append(edit,member);row.appendChild(actions);body.appendChild(row); }
  }

  function renderOrganizations(){const body=$("organizations-table");clear(body);$("organizations-empty").hidden=state.organizations.length!==0;for(const organization of state.organizations){const row=document.createElement("tr");row.appendChild(textCell(organization.name,organization.id));row.appendChild(plainCell((organization.team_ids||[]).join(", ")||"—"));row.appendChild(statusCell(organization.status==="active"));const actions=document.createElement("td");actions.className="row-actions";const edit=document.createElement("button");edit.type="button";edit.className="row-button";edit.textContent="Edit";edit.addEventListener("click",()=>openOrganizationDialog(organization));const assign=document.createElement("button");assign.type="button";assign.className="row-button";assign.textContent="Assign team";assign.addEventListener("click",()=>openOrganizationTeamDialog(organization.id));actions.append(edit,assign);row.appendChild(actions);body.appendChild(row)}}
  function openOrganizationDialog(item=null){$("organization-id").value=item?.id||"";$("organization-id").readOnly=Boolean(item);$("organization-name").value=item?.name||"";$("organization-description").value=item?.description||"";$("organization-status").value=item?.status||"active";$("organization-error").hidden=true;$("organization-dialog").showModal()}
  async function saveOrganization(event){event.preventDefault();const error=$("organization-error"),id=$("organization-id").value.trim();error.hidden=true;try{await apiJSON(`/admin/v1/organizations/${encodeURIComponent(id)}`,"PUT",{name:$("organization-name").value.trim(),description:$("organization-description").value.trim(),status:$("organization-status").value});$("organization-dialog").close();await loadData();showToast("Organization saved")}catch(requestError){error.textContent=requestError.message;error.hidden=false}}
  function openOrganizationTeamDialog(id){$("organization-team-org-id").value=id;$("organization-team-id").value="";$("organization-team-error").hidden=true;$("organization-team-dialog").showModal()}
  async function saveOrganizationTeam(event){event.preventDefault();const error=$("organization-team-error"),id=$("organization-team-org-id").value,teamID=$("organization-team-id").value.trim();error.hidden=true;try{await apiJSON(`/admin/v1/organizations/${encodeURIComponent(id)}/teams/${encodeURIComponent(teamID)}`,"PUT",{});$("organization-team-dialog").close();await loadData();showToast("Team assigned")}catch(requestError){error.textContent=requestError.message;error.hidden=false}}

  function renderAIHub(){const hub=$("ai-hub-table");clear(hub);$("ai-hub-empty").hidden=state.aiHub.length!==0;for(const model of state.aiHub){const row=document.createElement("tr");row.appendChild(textCell(model.model,(model.deployments||[]).join(", ")||"No deployment"));row.appendChild(plainCell(model.provider));row.appendChild(plainCell((model.capabilities||[]).join(", ")||"—"));row.appendChild(textCell(formatMoney(model.input_cost_per_1m,model.currency),`output ${formatMoney(model.output_cost_per_1m,model.currency)}`));row.appendChild(statusCell(model.available));hub.appendChild(row)}const recommendations=$("cost-recommendations-table");clear(recommendations);$("cost-recommendations-empty").hidden=state.costRecommendations.length!==0;for(const item of state.costRecommendations){const row=document.createElement("tr");row.appendChild(textCell(item.type.replaceAll("_"," "),item.current_provider));row.appendChild(plainCell(item.model));row.appendChild(textCell(item.recommended_provider||"—",item.summary));row.appendChild(plainCell(item.estimated_savings_percent?`${item.estimated_savings_percent.toFixed(1)}%`:"—"));recommendations.appendChild(row)}}

  function actionButton(label, callback, danger=false){const button=document.createElement("button");button.type="button";button.className=`row-button${danger?" danger":""}`;button.textContent=label;button.addEventListener("click",callback);return button}
  function fillSelect(id,items,selected,emptyLabel=""){const select=$(id);clear(select);if(emptyLabel){const option=document.createElement("option");option.value="";option.textContent=emptyLabel;select.appendChild(option)}for(const item of items){const option=document.createElement("option");option.value=item.id;option.textContent=item.id;select.appendChild(option)}if([...select.options].some(option=>option.value===selected))select.value=selected}

  function renderProviders(){const body=$("providers-table");clear(body);$("providers-empty").hidden=state.providers.length!==0;for(const item of state.providers){const row=document.createElement("tr");row.appendChild(textCell(item.id));row.appendChild(plainCell(item.type));row.appendChild(plainCell(item.base_url||"Local runtime"));row.appendChild(statusCell(item.enabled));const actions=document.createElement("td");actions.className="row-actions";actions.append(actionButton("Test",()=>probeProvider(item,false)),actionButton("Models",()=>probeProvider(item,true)),actionButton("Edit",()=>openProviderDialog(item)),actionButton("Delete",()=>confirmChange("Delete provider?",`Delete ${item.id}. Providers used by deployments are protected.`,async()=>{await api(`/admin/v1/providers/${encodeURIComponent(item.id)}`,{method:"DELETE"});await loadData();showToast("Provider deleted")}),true));row.appendChild(actions);body.appendChild(row)}}
  function openProviderDialog(item=null){$("provider-mode").value=item?"edit":"create";setText("provider-dialog-title",item?"Edit provider":"Add provider");$("provider-id").value=item?.id||"";$("provider-id").readOnly=Boolean(item);$("provider-type").value=item?.type||"ollama";$("provider-base-url").value=item?.base_url||"";$("provider-enabled").checked=item?.enabled??true;$("provider-form-error").hidden=true;$("provider-dialog").showModal()}
  async function saveProvider(event){event.preventDefault();const error=$("provider-form-error"),id=$("provider-id").value.trim(),editing=$("provider-mode").value==="edit";error.hidden=true;try{await apiJSON(editing?`/admin/v1/providers/${encodeURIComponent(id)}`:"/admin/v1/providers",editing?"PUT":"POST",{id,type:$("provider-type").value,base_url:$("provider-base-url").value.trim(),enabled:$("provider-enabled").checked});$("provider-dialog").close();await loadData();showToast("Provider saved")}catch(requestError){error.textContent=requestError.message;error.hidden=false}}
  function providerCredential(item){return state.credentials.find(credential=>credential.provider_id===item.id)||state.credentials.find(credential=>!credential.provider_id)}
  async function probeProvider(item,discover){try{const credential=providerCredential(item);const body={credential_id:credential?.id||""};if(discover){const response=await apiJSON(`/admin/v1/providers/${encodeURIComponent(item.id)}/discover-models`,"POST",body);const models=(response.data||[]).map(model=>model.id);showToast(models.length?`Models: ${models.slice(0,8).join(", ")}${models.length>8?"…":""}`:"Connected; no models returned")}else{const response=await apiJSON(`/admin/v1/providers/${encodeURIComponent(item.id)}/test`,"POST",body);showToast(`${item.id} available · ${response.latency_ms} ms · ${response.model_count} models`)}}catch(error){globalError.textContent=error.message;globalError.hidden=false}}

  function renderCredentials(){const body=$("credentials-table");clear(body);$("credentials-empty").hidden=state.credentials.length!==0;for(const item of state.credentials){const row=document.createElement("tr");row.appendChild(textCell(item.id));row.appendChild(plainCell(item.provider_id||"Reusable"));row.appendChild(plainCell(item.description));row.appendChild(plainCell(formatDate(item.updated_at)));const actions=document.createElement("td");actions.className="row-actions";actions.append(actionButton("Rotate",()=>openCredentialDialog(item)),actionButton("Delete",()=>confirmChange("Delete credential?",`Delete ${item.id}. Credentials used by deployments are protected.`,async()=>{await api(`/admin/v1/credentials/${encodeURIComponent(item.id)}`,{method:"DELETE"});await loadData();showToast("Credential deleted")}),true));row.appendChild(actions);body.appendChild(row)}}
  function openCredentialDialog(item=null){$("credential-mode").value=item?"edit":"create";setText("credential-dialog-title",item?"Rotate credential":"Add credential");$("credential-id").value=item?.id||"";$("credential-id").readOnly=Boolean(item);fillSelect("credential-provider",state.providers,item?.provider_id||"","Reusable credential");$("credential-description").value=item?.description||"";$("credential-secret").value="";$("credential-form-error").hidden=true;$("credential-dialog").showModal()}
  async function saveCredential(event){event.preventDefault();const error=$("credential-form-error"),id=$("credential-id").value.trim(),editing=$("credential-mode").value==="edit";error.hidden=true;try{await apiJSON(editing?`/admin/v1/credentials/${encodeURIComponent(id)}`:"/admin/v1/credentials",editing?"PUT":"POST",{id,provider_id:$("credential-provider").value,description:$("credential-description").value.trim(),secret:$("credential-secret").value});$("credential-secret").value="";$("credential-dialog").close();await loadData();showToast(editing?"Credential rotated":"Credential stored")}catch(requestError){$("credential-secret").value="";error.textContent=requestError.message;error.hidden=false}}

  function renderDeployments(){const body=$("deployments-table");clear(body);$("deployments-empty").hidden=state.deployments.length!==0;for(const deployment of state.deployments){const row=document.createElement("tr");row.appendChild(textCell(deployment.id,deployment.provider_type));row.appendChild(textCell(deployment.provider_id,deployment.credential_id||"No credential"));row.appendChild(textCell((deployment.models||[]).join(", "),deployment.upstream_model?`upstream: ${deployment.upstream_model}`:"same upstream name"));row.appendChild(textCell(`P${deployment.priority}`,`weight ${deployment.weight||1}`));const deploymentStatus=document.createElement("td"),status=document.createElement("span");status.className=`outcome ${deployment.runtime_state==="available"?"succeeded":"failed"}`;status.textContent=(deployment.runtime_state||(deployment.enabled?"available":"disabled")).replaceAll("_"," ");deploymentStatus.appendChild(status);row.appendChild(deploymentStatus);const actions=document.createElement("td");actions.className="row-actions";actions.append(actionButton("Edit",()=>openDeploymentDialog(deployment)),actionButton("Delete",()=>confirmChange("Delete deployment?",`Delete ${deployment.id}. Group members are protected.`,async()=>{await api(`/admin/v1/model-deployments/${encodeURIComponent(deployment.id)}`,{method:"DELETE"});await loadData();showToast("Deployment deleted")}),true));row.appendChild(actions);body.appendChild(row)}}
  function openDeploymentDialog(item=null){$("deployment-mode").value=item?"edit":"create";setText("deployment-dialog-title",item?"Edit deployment":"Add deployment");$("deployment-id").value=item?.id||"";$("deployment-id").readOnly=Boolean(item);fillSelect("deployment-provider",state.providers,item?.provider_id||"");fillSelect("deployment-credential",state.credentials,item?.credential_id||"","No credential");$("deployment-upstream").value=item?.upstream_model||"";$("deployment-models").value=(item?.models||[]).join(", ");$("deployment-capabilities").value=(item?.capabilities||[]).join(", ");$("deployment-priority").value=item?.priority||0;$("deployment-weight").value=item?.weight||1;$("deployment-guardrail").value=item?.guardrail_policy||"";$("deployment-enabled").checked=item?.enabled??true;$("deployment-form-error").hidden=true;$("deployment-dialog").showModal()}
  async function saveDeployment(event){event.preventDefault();const error=$("deployment-form-error"),id=$("deployment-id").value.trim(),editing=$("deployment-mode").value==="edit";error.hidden=true;try{await apiJSON(editing?`/admin/v1/model-deployments/${encodeURIComponent(id)}`:"/admin/v1/model-deployments",editing?"PUT":"POST",{id,provider_id:$("deployment-provider").value,credential_id:$("deployment-credential").value,upstream_model:$("deployment-upstream").value.trim(),models:commaList("deployment-models"),capabilities:commaList("deployment-capabilities"),priority:Number($("deployment-priority").value||0),weight:Number($("deployment-weight").value||1),guardrail_policy:$("deployment-guardrail").value.trim(),enabled:$("deployment-enabled").checked});$("deployment-dialog").close();await loadData();showToast("Deployment saved")}catch(requestError){error.textContent=requestError.message;error.hidden=false}}

  function renderModelGroups(){const body=$("model-groups-table");clear(body);$("model-groups-empty").hidden=state.modelGroups.length!==0;for(const item of state.modelGroups){const row=document.createElement("tr");row.appendChild(textCell(item.id));row.appendChild(plainCell((item.deployment_ids||[]).join(", ")));row.appendChild(plainCell(item.strategy));row.appendChild(statusCell(item.enabled));const actions=document.createElement("td");actions.className="row-actions";actions.append(actionButton("Edit",()=>openModelGroupDialog(item)),actionButton("Delete",()=>confirmChange("Delete model group?",`Delete public model group ${item.id}.`,async()=>{await api(`/admin/v1/model-groups/${encodeURIComponent(item.id)}`,{method:"DELETE"});await loadData();showToast("Model group deleted")}),true));row.appendChild(actions);body.appendChild(row)}}
  function openModelGroupDialog(item=null){$("model-group-mode").value=item?"edit":"create";setText("model-group-dialog-title",item?"Edit model group":"Add model group");$("model-group-id").value=item?.id||"";$("model-group-id").readOnly=Boolean(item);$("model-group-strategy").value=item?.strategy||"weighted";$("model-group-deployments").value=(item?.deployment_ids||[]).join(", ");$("model-group-enabled").checked=item?.enabled??true;$("model-group-form-error").hidden=true;$("model-group-dialog").showModal()}
  async function saveModelGroup(event){event.preventDefault();const error=$("model-group-form-error"),id=$("model-group-id").value.trim(),editing=$("model-group-mode").value==="edit";error.hidden=true;try{await apiJSON(editing?`/admin/v1/model-groups/${encodeURIComponent(id)}`:"/admin/v1/model-groups",editing?"PUT":"POST",{id,deployment_ids:commaList("model-group-deployments"),strategy:$("model-group-strategy").value,enabled:$("model-group-enabled").checked});$("model-group-dialog").close();await loadData();showToast("Model group saved")}catch(requestError){error.textContent=requestError.message;error.hidden=false}}

  function renderGuardrails(){const body=$("guardrails-table");clear(body);$("guardrails-empty").hidden=state.guardrails.length!==0;const select=$("compliance-policy"),selected=select.value;clear(select);for(const policy of state.guardrails){const row=document.createElement("tr");row.appendChild(textCell(policy.name,policy.description));row.appendChild(plainCell([policy.dlp?"DLP":"",policy.av?"AV":""].filter(Boolean).join(" + ")));const statusCell=document.createElement("td");const status=document.createElement("span");status.className=`outcome ${policy.enabled?"succeeded":"failed"}`;status.textContent=policy.enabled?"Enabled":"Disabled";statusCell.appendChild(status);row.appendChild(statusCell);const actions=document.createElement("td");const edit=document.createElement("button");edit.type="button";edit.className="row-button";edit.textContent="Edit";edit.addEventListener("click",()=>openGuardrailDialog(policy));actions.appendChild(edit);row.appendChild(actions);body.appendChild(row);if(policy.enabled){const option=document.createElement("option");option.value=policy.name;option.textContent=policy.name;select.appendChild(option)}}if([...select.options].some(option=>option.value===selected))select.value=selected}
  function openGuardrailDialog(policy=null){$("guardrail-name").value=policy?.name||"";$("guardrail-name").readOnly=Boolean(policy);$("guardrail-description").value=policy?.description||"";$("guardrail-dlp").checked=Boolean(policy?.dlp);$("guardrail-av").checked=Boolean(policy?.av);$("guardrail-enabled").checked=policy?.enabled??true;$("guardrail-form-error").hidden=true;$("guardrail-dialog").showModal()}
  async function saveGuardrail(event){event.preventDefault();const error=$("guardrail-form-error");error.hidden=true;const name=$("guardrail-name").value.trim();if(!$("guardrail-dlp").checked&&!$("guardrail-av").checked){error.textContent="Enable DLP, AV, or both.";error.hidden=false;return}try{await apiJSON(`/admin/v1/guardrail-policies/${encodeURIComponent(name)}`,"PUT",{description:$("guardrail-description").value.trim(),dlp:$("guardrail-dlp").checked,av:$("guardrail-av").checked,enabled:$("guardrail-enabled").checked});$("guardrail-dialog").close();await loadData();showToast("Guardrail policy saved")}catch(requestError){error.textContent=requestError.message;error.hidden=false}}
  async function runCompliance(event){event.preventDefault();const error=$("compliance-error");error.hidden=true;try{const result=await apiJSON("/admin/v1/compliance/check","POST",{policy:$("compliance-policy").value,text:$("compliance-text").value});$("compliance-result").textContent=JSON.stringify(result,null,2)}catch(requestError){error.textContent=requestError.message;error.hidden=false}}

  function statusCell(enabled) { const cell=document.createElement("td"),status=document.createElement("span");status.className=`outcome ${enabled?"succeeded":"failed"}`;status.textContent=enabled?"Enabled":"Disabled";cell.appendChild(status);return cell; }
  function editAction(label, callback) { const cell=document.createElement("td");cell.className="row-actions";const button=document.createElement("button");button.type="button";button.className="row-button";button.textContent=label;button.addEventListener("click",callback);cell.appendChild(button);return cell; }
  function renderMCP(){
    const servers=$("mcp-servers-table");clear(servers);$("mcp-servers-empty").hidden=state.mcpServers.length!==0;
    for(const server of state.mcpServers){const row=document.createElement("tr");row.appendChild(textCell(server.label,server.id));row.appendChild(textCell(server.transport,server.server_url));row.appendChild(plainCell((server.tools||[]).join(", ")||"—"));row.appendChild(statusCell(server.enabled));row.appendChild(editAction("Edit",()=>openMCPServerDialog(server)));servers.appendChild(row)}
    const toolsets=$("mcp-toolsets-table");clear(toolsets);$("mcp-toolsets-empty").hidden=state.mcpToolsets.length!==0;
    for(const toolset of state.mcpToolsets){const row=document.createElement("tr");row.appendChild(textCell(`toolset:${toolset.id}`,toolset.name));row.appendChild(plainCell((toolset.tools||[]).join(", ")));row.appendChild(statusCell(toolset.enabled));row.appendChild(editAction("Edit",()=>openMCPToolsetDialog(toolset)));toolsets.appendChild(row)}
  }
  function openMCPServerDialog(server=null){$("mcp-server-id").value=server?.id||"";$("mcp-server-id").readOnly=Boolean(server);$("mcp-server-label").value=server?.label||"";$("mcp-server-description").value=server?.description||"";$("mcp-server-url").value=server?.server_url||"";$("mcp-server-transport").value=server?.transport||"streamable-http";$("mcp-server-tools").value=(server?.tools||[]).join(", ");$("mcp-server-enabled").checked=server?.enabled??true;$("mcp-server-error").hidden=true;$("mcp-server-dialog").showModal()}
  async function saveMCPServer(event){event.preventDefault();const error=$("mcp-server-error"),id=$("mcp-server-id").value.trim();error.hidden=true;try{await apiJSON(`/admin/v1/mcp/servers/${encodeURIComponent(id)}`,"PUT",{label:$("mcp-server-label").value.trim(),description:$("mcp-server-description").value.trim(),server_url:$("mcp-server-url").value.trim(),transport:$("mcp-server-transport").value,tools:commaList("mcp-server-tools"),enabled:$("mcp-server-enabled").checked});$("mcp-server-dialog").close();await loadData();showToast("MCP server saved")}catch(requestError){error.textContent=requestError.message;error.hidden=false}}
  function openMCPToolsetDialog(toolset=null){$("mcp-toolset-id").value=toolset?.id||"";$("mcp-toolset-id").readOnly=Boolean(toolset);$("mcp-toolset-name").value=toolset?.name||"";$("mcp-toolset-description").value=toolset?.description||"";$("mcp-toolset-tools").value=(toolset?.tools||[]).join(", ");$("mcp-toolset-enabled").checked=toolset?.enabled??true;$("mcp-toolset-error").hidden=true;$("mcp-toolset-dialog").showModal()}
  async function saveMCPToolset(event){event.preventDefault();const error=$("mcp-toolset-error"),id=$("mcp-toolset-id").value.trim();error.hidden=true;try{await apiJSON(`/admin/v1/mcp/toolsets/${encodeURIComponent(id)}`,"PUT",{name:$("mcp-toolset-name").value.trim(),description:$("mcp-toolset-description").value.trim(),tools:commaList("mcp-toolset-tools"),enabled:$("mcp-toolset-enabled").checked});$("mcp-toolset-dialog").close();await loadData();showToast("MCP toolset saved")}catch(requestError){error.textContent=requestError.message;error.hidden=false}}

  function openUserDialog(user=null){$("user-id").value=user?.id||"";$("user-id").readOnly=Boolean(user);$("user-email").value=user?.email||"";$("user-name").value=user?.name||"";$("user-status").value=user?.status||"active";$("user-roles").value=(user?.roles||[]).join(", ");$("user-form-error").hidden=true;$("user-dialog").showModal()}
  async function saveUser(event){event.preventDefault();const error=$("user-form-error");error.hidden=true;const id=$("user-id").value.trim();try{await apiJSON(`/admin/v1/users/${encodeURIComponent(id)}`,"PUT",{email:$("user-email").value.trim(),name:$("user-name").value.trim(),status:$("user-status").value,roles:commaList("user-roles")});$("user-dialog").close();await loadData();showToast("User saved")}catch(requestError){error.textContent=requestError.message;error.hidden=false}}
  function openTeamDialog(team=null){$("team-id").value=team?.id||"";$("team-id").readOnly=Boolean(team);$("team-name").value=team?.name||"";$("team-description").value=team?.description||"";$("team-status").value=team?.status||"active";$("team-form-error").hidden=true;$("team-dialog").showModal()}
  async function saveTeam(event){event.preventDefault();const error=$("team-form-error");error.hidden=true;const id=$("team-id").value.trim();try{await apiJSON(`/admin/v1/teams/${encodeURIComponent(id)}`,"PUT",{name:$("team-name").value.trim(),description:$("team-description").value.trim(),status:$("team-status").value});$("team-dialog").close();await loadData();showToast("Team saved")}catch(requestError){error.textContent=requestError.message;error.hidden=false}}
  function openMembershipDialog(teamID){$("membership-team-id").value=teamID;$("membership-user-id").value="";$("membership-roles").value="member";$("membership-form-error").hidden=true;$("membership-dialog").showModal()}
  async function saveMembership(event){event.preventDefault();const error=$("membership-form-error");error.hidden=true;const teamID=$("membership-team-id").value,userID=$("membership-user-id").value.trim();try{await apiJSON(`/admin/v1/teams/${encodeURIComponent(teamID)}/members/${encodeURIComponent(userID)}`,"PUT",{roles:commaList("membership-roles")});$("membership-dialog").close();await loadData();showToast("Membership saved")}catch(requestError){error.textContent=requestError.message;error.hidden=false}}

  function renderBudgets() {
    const body = $("budgets-table"); clear(body); $("budgets-empty").hidden = state.budgets.length !== 0;
    for (const budget of state.budgets) {
      const row = document.createElement("tr");
      row.appendChild(textCell(`${budget.scope_type}: ${budget.scope_id}`, `Policy #${budget.id}`));
      row.appendChild(plainCell(budget.period));
      row.appendChild(plainCell(formatMoney(budget.max_cost, budget.currency)));
      row.appendChild(plainCell(formatNumber(budget.max_tokens)));
      const statusCell = document.createElement("td"); const status = document.createElement("span"); status.className = `outcome ${budget.enabled ? "succeeded" : "failed"}`; status.textContent = budget.enabled ? "Active" : "Disabled"; statusCell.appendChild(status); row.appendChild(statusCell);
      row.appendChild(plainCell(formatDate(budget.updated_at)));
      const actions = document.createElement("td"); actions.className = "row-actions";
      const edit = document.createElement("button"); edit.className = "row-button"; edit.type = "button"; edit.textContent = "Edit"; edit.addEventListener("click", () => openBudgetDialog(budget)); actions.appendChild(edit);
      if (budget.enabled) { const disable = document.createElement("button"); disable.className = "row-button danger"; disable.type = "button"; disable.textContent = "Disable"; disable.addEventListener("click", () => confirmChange("Disable budget policy?", `${budget.scope_type}: ${budget.scope_id} will stop enforcing limits.`, () => disableBudget(budget.id))); actions.appendChild(disable); }
      row.appendChild(actions);
      body.appendChild(row);
    }
  }

  function renderAudit() {
    const body = $("audit-table"); clear(body); $("audit-empty").hidden = state.audit.length !== 0;
    for (const event of state.audit) {
      const row = document.createElement("tr");
      row.appendChild(plainCell(formatDate(event.occurred_at)));
      row.appendChild(textCell(event.action));
      row.appendChild(textCell(event.target_type, event.target_id));
      row.appendChild(textCell(event.actor_id, event.actor_credential_id));
      const outcomeCell = document.createElement("td"); const outcome = document.createElement("span"); outcome.className = `outcome ${event.outcome || "attempted"}`; outcome.textContent = event.outcome || "unknown"; outcomeCell.appendChild(outcome); row.appendChild(outcomeCell);
      row.appendChild(plainCell(event.request_id)); body.appendChild(row);
    }
  }

  function switchView(name) {
    for (const view of document.querySelectorAll(".view")) { const active = view.id === `view-${name}`; view.hidden = !active; view.classList.toggle("active-view", active); }
    for (const item of document.querySelectorAll(".nav-item[data-view]")) item.classList.toggle("active", item.dataset.view === name);
    setText("page-title", pageTitles[name] || "Console");
    history.replaceState(null, "", `#${name}`);
  }

  function numberOrUndefined(id) {
    const value = $(id).value.trim();
    return value === "" ? undefined : Number(value);
  }

  function commaList(id) { return [...new Set($(id).value.split(",").map((value) => value.trim()).filter(Boolean))]; }

  function localDateTime(value) {
    if (!value) return "";
    const date = new Date(value); if (Number.isNaN(date.getTime())) return "";
    const local = new Date(date.getTime() - date.getTimezoneOffset() * 60000);
    return local.toISOString().slice(0, 16);
  }

  function openKeyDialog(key = null, mode = "create") {
    setText("key-dialog-title", mode === "edit" ? "Edit virtual key" : mode === "rotate" ? "Rotate virtual key" : "Create virtual key");
    $("key-id").value = key?.id || ""; $("key-mode").value = mode;
    $("key-alias").value = key?.alias || ""; $("key-description").value = key?.description || ""; $("key-tags").value = (key?.tags || []).join(", ");
    $("key-user-id").value = key?.user_id || "";
    $("key-team-id").value = key?.team_id || "";
    $("key-roles").value = (key?.roles || []).join(", ");
    $("key-models").value = (key?.allowed_models || []).join(", ");
    $("key-tools").value = (key?.allowed_tools || []).join(", ");
    $("key-rpm").value = key?.rate_limit_rpm || 0;
    $("key-tpm").value = key?.rate_limit_tpm || 0;
    $("key-expires").value = localDateTime(key?.expires_at);
    $("key-form-error").hidden = true;
    $("key-dialog").showModal();
  }

  async function saveKey(event) {
    event.preventDefault();
    const error = $("key-form-error"); error.hidden = true;
    const payload = {
      alias: $("key-alias").value.trim(), description: $("key-description").value.trim(), tags: commaList("key-tags"),
      user_id: $("key-user-id").value.trim(), team_id: $("key-team-id").value.trim(),
      roles: commaList("key-roles"), allowed_models: commaList("key-models"), allowed_tools: commaList("key-tools"),
      rate_limit_rpm: Number($("key-rpm").value || 0), rate_limit_tpm: Number($("key-tpm").value || 0),
    };
    const expires = $("key-expires").value; if (expires) payload.expires_at = new Date(expires).toISOString();
    const id = $("key-id").value; const mode = $("key-mode").value;
    try {
      if (mode === "edit") { await apiJSON(`/admin/v1/keys/${encodeURIComponent(id)}`, "PUT", payload); $("key-dialog").close(); await loadData(); showToast("Virtual key updated"); return; }
      const issued = await apiJSON(mode === "rotate" ? `/admin/v1/keys/${encodeURIComponent(id)}/rotate` : "/admin/v1/keys", "POST", payload);
      $("key-dialog").close();
      $("issued-key-id").value = issued.id;
      $("issued-key-token").value = issued.token;
      $("issued-key-dialog").showModal();
      await loadData();
    } catch (requestError) { error.textContent = requestError.message; error.hidden = false; }
  }

  async function revokeKey(id) { await api(`/admin/v1/keys/${encodeURIComponent(id)}`, { method: "DELETE" }); await loadData(); showToast("Virtual key revoked"); }
  async function setKeyDisabled(id, disabled) { await api(`/admin/v1/keys/${encodeURIComponent(id)}/${disabled ? "disable" : "enable"}`, { method: "POST" }); await loadData(); showToast(disabled ? "Virtual key disabled" : "Virtual key enabled"); }
  async function showKeyUsage(id) { $("request-log-credential").value = id; switchView("request-logs"); await loadRequestLogs(false); }

  function clearIssuedKey() { $("issued-key-id").value = ""; $("issued-key-token").value = ""; }

  function openModelDialog(item = null) {
    if (!state.catalog) { showToast("Runtime catalog is unavailable"); return; }
    setText("model-dialog-title", item ? "Edit catalog entry" : "Add catalog entry");
    $("model-original-key").value = item ? `${item.provider}\u0000${item.model}` : "";
    $("model-provider").value = item?.provider || "";
    $("model-name").value = item?.model || "";
    $("model-capabilities").value = (item?.capabilities || []).join(", ");
    $("model-max-input").value = item?.max_input_tokens || "";
    $("model-max-output").value = item?.max_output_tokens || "";
    $("model-input-cost").value = item?.input_cost_per_1m ?? "";
    $("model-output-cost").value = item?.output_cost_per_1m ?? "";
    $("model-currency").value = item?.currency || "";
    $("model-form-error").hidden = true;
    $("model-dialog").showModal();
  }

  async function saveModel(event) {
    event.preventDefault();
    const error = $("model-form-error"); error.hidden = true;
    const provider = $("model-provider").value.trim();
    const model = $("model-name").value.trim();
    const capabilities = [...new Set($("model-capabilities").value.split(",").map((value) => value.trim()).filter(Boolean))];
    const entry = { provider, model };
    const optional = { max_input_tokens: numberOrUndefined("model-max-input"), max_output_tokens: numberOrUndefined("model-max-output"), input_cost_per_1m: numberOrUndefined("model-input-cost"), output_cost_per_1m: numberOrUndefined("model-output-cost") };
    for (const [key, value] of Object.entries(optional)) if (value !== undefined) entry[key] = value;
    if (capabilities.length) entry.capabilities = capabilities;
    const currency = $("model-currency").value.trim().toUpperCase(); if (currency) entry.currency = currency;
    if ((entry.input_cost_per_1m != null || entry.output_cost_per_1m != null) && !currency) { error.textContent = "Currency is required when pricing is set."; error.hidden = false; return; }
    const original = $("model-original-key").value;
    const models = (state.catalog.models || []).filter((item) => `${item.provider}\u0000${item.model}` !== original && !(item.provider === provider && item.model === model));
    models.push(entry);
    const payload = { ...state.catalog, version: `ui-${Date.now()}`, models };
    try { state.catalog = await apiJSON("/admin/v1/model-catalog", "PUT", payload); $("model-dialog").close(); await loadData(); showToast("Runtime catalog updated"); }
    catch (requestError) { error.textContent = requestError.message; error.hidden = false; }
  }

  async function removeCatalogEntry(item) {
    const payload = { ...state.catalog, version: `ui-${Date.now()}`, models: (state.catalog.models || []).filter((candidate) => candidate.provider !== item.provider || candidate.model !== item.model) };
    state.catalog = await apiJSON("/admin/v1/model-catalog", "PUT", payload); await loadData(); showToast("Catalog entry removed");
  }

  function openBudgetDialog(item = null) {
    setText("budget-dialog-title", item?.id ? "Edit budget" : "Create budget");
    $("budget-id").value = item?.id || "";
    $("budget-scope-type").value = item?.scope_type || "team";
    $("budget-scope-id").value = item?.scope_id || "";
    $("budget-period").value = item?.period || "month";
    $("budget-currency").value = item?.currency || "USD";
    $("budget-max-cost").value = item?.max_cost ?? "";
    $("budget-max-tokens").value = item?.max_tokens ?? "";
    $("budget-enabled").checked = item?.enabled ?? true;
    $("budget-form-error").hidden = true;
    $("budget-dialog").showModal();
  }

  async function saveBudget(event) {
    event.preventDefault();
    const error = $("budget-form-error"); error.hidden = true;
    const maxCost = numberOrUndefined("budget-max-cost"); const maxTokens = numberOrUndefined("budget-max-tokens");
    if (maxCost === undefined && maxTokens === undefined) { error.textContent = "Set a maximum cost, maximum tokens, or both."; error.hidden = false; return; }
    const payload = { scope_type: $("budget-scope-type").value, scope_id: $("budget-scope-id").value.trim(), period: $("budget-period").value, currency: $("budget-currency").value.trim().toUpperCase(), enabled: $("budget-enabled").checked };
    if (maxCost !== undefined) payload.max_cost = maxCost; if (maxTokens !== undefined) payload.max_tokens = maxTokens;
    const id = $("budget-id").value;
    try { await apiJSON(id ? `/admin/v1/budgets/${id}` : "/admin/v1/budgets", id ? "PUT" : "POST", payload); $("budget-dialog").close(); await loadData(); showToast(id ? "Budget updated" : "Budget created"); }
    catch (requestError) { error.textContent = requestError.message; error.hidden = false; }
  }

  async function disableBudget(id) { await api(`/admin/v1/budgets/${id}`, { method: "DELETE" }); await loadData(); showToast("Budget disabled"); }

  function confirmChange(title, message, action) {
    setText("confirm-title", title); setText("confirm-message", message); pendingConfirmation = action; $("confirm-dialog").showModal();
  }

  function showLogin(message = "") {
    consoleView.hidden = true; loginView.hidden = false; loginError.hidden = !message; loginError.textContent = message; tokenInput.value = ""; tokenInput.focus();
  }
  function showConsole() { const requested = location.hash.slice(1); loginView.hidden = true; consoleView.hidden = false; switchView(Object.prototype.hasOwnProperty.call(pageTitles, requested) ? requested : "overview"); }
  function showToast(message) { const toast = $("toast"); toast.textContent = message; toast.hidden = false; window.setTimeout(() => { toast.hidden = true; }, 2500); }

  loginForm.addEventListener("submit", async (event) => {
    event.preventDefault(); loginError.hidden = true; state.token = tokenInput.value.trim();
    if (!state.token) return;
    const submit = loginForm.querySelector("button[type=submit]"); submit.disabled = true; submit.textContent = "Connecting…";
    try { await loadData(); sessionStorage.setItem("ai_gateway_admin_token", state.token); showConsole(); }
    catch (error) { state.token = ""; showLogin(error.message || "Unable to connect to the gateway."); }
    finally { submit.disabled = false; submit.textContent = "Open console"; }
  });
  $("toggle-token").addEventListener("click", () => { const visible = tokenInput.type === "text"; tokenInput.type = visible ? "password" : "text"; $("toggle-token").textContent = visible ? "Show" : "Hide"; });
  $("logout-button").addEventListener("click", () => { sessionStorage.removeItem("ai_gateway_admin_token"); state.token = ""; showLogin(); });
  $("refresh-button").addEventListener("click", async () => { try { await loadData({ action: $("audit-action").value }); showToast("Console data refreshed"); } catch (error) { if (error.auth) { sessionStorage.removeItem("ai_gateway_admin_token"); showLogin(error.message); } else { globalError.textContent = error.message; globalError.hidden = false; } } });
  $("audit-filter-button").addEventListener("click", async () => { try { await loadData({ action: $("audit-action").value }); } catch (error) { globalError.textContent = error.message; globalError.hidden = false; } });
  $("usage-days").addEventListener("change", async () => { try { await loadData(); } catch (error) { globalError.textContent = error.message; globalError.hidden = false; } });
  $("customer-filter-form").addEventListener("submit",loadCustomer);
  $("request-log-filter-form").addEventListener("submit", async (event) => { event.preventDefault(); try { await loadRequestLogs(false); } catch (error) { globalError.textContent = error.message; globalError.hidden = false; } });
  $("request-logs-more").addEventListener("click", async () => { try { await loadRequestLogs(true); } catch (error) { globalError.textContent = error.message; globalError.hidden = false; } });
  $("playground-form").addEventListener("submit", runPlayground);
  $("model-search").addEventListener("input", renderModels);
  $("add-key-button").addEventListener("click", () => openKeyDialog());
  $("key-form").addEventListener("submit", saveKey);
  $("add-user-button").addEventListener("click",()=>openUserDialog());
  $("user-form").addEventListener("submit",saveUser);
  $("add-team-button").addEventListener("click",()=>openTeamDialog());
  $("team-form").addEventListener("submit",saveTeam);
  $("membership-form").addEventListener("submit",saveMembership);
  $("add-organization-button").addEventListener("click",()=>openOrganizationDialog());
  $("organization-form").addEventListener("submit",saveOrganization);
  $("organization-team-form").addEventListener("submit",saveOrganizationTeam);
  $("copy-issued-key").addEventListener("click", async () => { try { await navigator.clipboard.writeText($("issued-key-token").value); showToast("Token copied"); } catch (_) { $("issued-key-token").select(); showToast("Select and copy the token manually"); } });
  $("issued-key-dialog").addEventListener("close", clearIssuedKey);
  $("add-model-button").addEventListener("click", () => openModelDialog());
  $("model-form").addEventListener("submit", saveModel);
  $("add-provider-button").addEventListener("click",()=>openProviderDialog());
  $("provider-form").addEventListener("submit",saveProvider);
  $("add-credential-button").addEventListener("click",()=>openCredentialDialog());
  $("credential-form").addEventListener("submit",saveCredential);
  $("add-deployment-button").addEventListener("click",()=>openDeploymentDialog());
  $("deployment-form").addEventListener("submit",saveDeployment);
  $("add-group-button").addEventListener("click",()=>openModelGroupDialog());
  $("model-group-form").addEventListener("submit",saveModelGroup);
  $("add-guardrail-button").addEventListener("click",()=>openGuardrailDialog());
  $("guardrail-form").addEventListener("submit",saveGuardrail);
  $("compliance-form").addEventListener("submit",runCompliance);
  $("add-mcp-server-button").addEventListener("click",()=>openMCPServerDialog());
  $("mcp-server-form").addEventListener("submit",saveMCPServer);
  $("add-mcp-toolset-button").addEventListener("click",()=>openMCPToolsetDialog());
  $("mcp-toolset-form").addEventListener("submit",saveMCPToolset);
  $("add-budget-button").addEventListener("click", () => openBudgetDialog());
  $("budget-form").addEventListener("submit", saveBudget);
  for (const button of document.querySelectorAll(".close-dialog")) button.addEventListener("click", () => $(button.dataset.dialog).close());
  $("confirm-cancel").addEventListener("click", () => { pendingConfirmation = null; $("confirm-dialog").close(); });
  $("confirm-form").addEventListener("submit", async (event) => { event.preventDefault(); const action = pendingConfirmation; pendingConfirmation = null; $("confirm-dialog").close(); if (!action) return; try { await action(); } catch (error) { globalError.textContent = error.message; globalError.hidden = false; } });
  for (const item of document.querySelectorAll("[data-view]")) item.addEventListener("click", () => switchView(item.dataset.view));
  for (const item of document.querySelectorAll("[data-open-view]")) item.addEventListener("click", () => switchView(item.dataset.openView));

  if (state.token) {
    loadData().then(showConsole).catch((error) => { sessionStorage.removeItem("ai_gateway_admin_token"); state.token = ""; showLogin(error.message); });
  } else showLogin();
})();
