(() => {
  "use strict";

  const state = { token: sessionStorage.getItem("ai_gateway_admin_token") || "", usage: null, requestLogs: [], requestLogNextBefore: "", requestLogNextRequestID: "", requestLogSettings: null, routing: null, keys: [], models: [], catalog: null, budgets: [], audit: [] };
  const $ = (id) => document.getElementById(id);
  const loginView = $("login-view");
  const consoleView = $("console-view");
  const loginForm = $("login-form");
  const tokenInput = $("admin-token");
  const loginError = $("login-error");
  const globalError = $("global-error");
  const pageTitles = { overview: "Overview", usage: "Usage & spend", "request-logs": "Request logs", routing: "Routing diagnostics", playground: "Chat playground", keys: "Virtual keys", models: "Models", budgets: "Budgets", audit: "Audit log" };
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
    for (const [name, id] of [["status", "request-log-status"], ["request_id", "request-log-request-id"], ["model", "request-log-model"], ["provider", "request-log-provider"], ["team_id", "request-log-team"]]) {
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
    setText("catalog-version", state.catalog?.version ? `Version ${state.catalog.version}` : "Runtime registry");
    renderOverview();
    renderRouting();
    renderPlaygroundModels();
    renderUsage();
    renderRequestLogs();
    renderKeys();
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
      row.appendChild(textCell(key.id, `Created ${formatDate(key.created_at)}`));
      row.appendChild(textCell(key.user_id, key.team_id));
      const grants = [...(key.roles || []), ...(key.allowed_models || []).map((value) => `model:${value}`), ...(key.allowed_tools || []).map((value) => `tool:${value}`)];
      row.appendChild(plainCell(grants.join(", ") || "Unscoped"));
      row.appendChild(textCell(key.rate_limit_rpm ? `${formatNumber(key.rate_limit_rpm)} RPM` : "No RPM limit", key.rate_limit_tpm ? `${formatNumber(key.rate_limit_tpm)} TPM` : "No TPM limit"));
      const expired = key.expires_at && new Date(key.expires_at) <= new Date();
      const active = !key.revoked_at && !expired;
      const statusCell = document.createElement("td"); const status = document.createElement("span"); status.className = `outcome ${active ? "succeeded" : "failed"}`; status.textContent = key.revoked_at ? "Revoked" : expired ? "Expired" : "Active"; statusCell.appendChild(status); row.appendChild(statusCell);
      row.appendChild(plainCell(formatDate(key.last_used_at)));
      const actions = document.createElement("td"); actions.className = "row-actions";
      if (active) {
        const rotate = document.createElement("button"); rotate.type = "button"; rotate.className = "row-button"; rotate.textContent = "Rotate"; rotate.addEventListener("click", () => openKeyDialog(key));
        const revoke = document.createElement("button"); revoke.type = "button"; revoke.className = "row-button danger"; revoke.textContent = "Revoke"; revoke.addEventListener("click", () => confirmChange("Revoke virtual key?", `${key.id} will stop authorizing requests immediately.`, () => revokeKey(key.id)));
        actions.append(rotate, revoke);
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

  function openKeyDialog(key = null) {
    setText("key-dialog-title", key ? "Rotate virtual key" : "Create virtual key");
    $("key-rotate-id").value = key?.id || "";
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
      user_id: $("key-user-id").value.trim(), team_id: $("key-team-id").value.trim(),
      roles: commaList("key-roles"), allowed_models: commaList("key-models"), allowed_tools: commaList("key-tools"),
      rate_limit_rpm: Number($("key-rpm").value || 0), rate_limit_tpm: Number($("key-tpm").value || 0),
    };
    const expires = $("key-expires").value; if (expires) payload.expires_at = new Date(expires).toISOString();
    const rotateID = $("key-rotate-id").value;
    try {
      const issued = await apiJSON(rotateID ? `/admin/v1/keys/${encodeURIComponent(rotateID)}/rotate` : "/admin/v1/keys", "POST", payload);
      $("key-dialog").close();
      $("issued-key-id").value = issued.id;
      $("issued-key-token").value = issued.token;
      $("issued-key-dialog").showModal();
      await loadData();
    } catch (requestError) { error.textContent = requestError.message; error.hidden = false; }
  }

  async function revokeKey(id) { await api(`/admin/v1/keys/${encodeURIComponent(id)}`, { method: "DELETE" }); await loadData(); showToast("Virtual key revoked"); }

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
    setText("budget-dialog-title", item ? "Edit budget" : "Create budget");
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
  $("request-log-filter-form").addEventListener("submit", async (event) => { event.preventDefault(); try { await loadRequestLogs(false); } catch (error) { globalError.textContent = error.message; globalError.hidden = false; } });
  $("request-logs-more").addEventListener("click", async () => { try { await loadRequestLogs(true); } catch (error) { globalError.textContent = error.message; globalError.hidden = false; } });
  $("playground-form").addEventListener("submit", runPlayground);
  $("model-search").addEventListener("input", renderModels);
  $("add-key-button").addEventListener("click", () => openKeyDialog());
  $("key-form").addEventListener("submit", saveKey);
  $("copy-issued-key").addEventListener("click", async () => { try { await navigator.clipboard.writeText($("issued-key-token").value); showToast("Token copied"); } catch (_) { $("issued-key-token").select(); showToast("Select and copy the token manually"); } });
  $("issued-key-dialog").addEventListener("close", clearIssuedKey);
  $("add-model-button").addEventListener("click", () => openModelDialog());
  $("model-form").addEventListener("submit", saveModel);
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
