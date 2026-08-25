(() => {
  "use strict";

  const state = { token: sessionStorage.getItem("ai_gateway_admin_token") || "", models: [], catalog: null, budgets: [], audit: [] };
  const $ = (id) => document.getElementById(id);
  const loginView = $("login-view");
  const consoleView = $("console-view");
  const loginForm = $("login-form");
  const tokenInput = $("admin-token");
  const loginError = $("login-error");
  const globalError = $("global-error");
  const pageTitles = { overview: "Overview", models: "Models", budgets: "Budgets", audit: "Audit log" };

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

  async function loadData({ action = "" } = {}) {
    globalError.hidden = true;
    const query = new URLSearchParams({ limit: "100" });
    if (action.trim()) query.set("action", action.trim());
    const requests = [
      api("/v1/models"),
      api("/admin/v1/model-catalog"),
      api("/admin/v1/budgets"),
      api(`/admin/v1/audit/events?${query}`),
    ];
    const results = await Promise.allSettled(requests);
    const authFailure = results.find((result) => result.status === "rejected" && result.reason?.auth);
    if (authFailure) throw authFailure.reason;
    const errors = [];
    if (results[0].status === "fulfilled") state.models = results[0].value?.data || []; else errors.push(`Models: ${results[0].reason.message}`);
    if (results[1].status === "fulfilled") state.catalog = results[1].value; else errors.push(`Catalog: ${results[1].reason.message}`);
    if (results[2].status === "fulfilled") state.budgets = results[2].value?.data || []; else errors.push(`Budgets: ${results[2].reason.message}`);
    if (results[3].status === "fulfilled") state.audit = results[3].value?.data || []; else errors.push(`Audit: ${results[3].reason.message}`);
    renderAll();
    setText("console-health", errors.length ? "Degraded" : "Operational");
    if (errors.length) { globalError.textContent = errors.join(" · "); globalError.hidden = false; }
    const now = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date());
    setText("last-refresh", `Updated ${now}`);
  }

  function catalogByModel() {
    const index = new Map();
    for (const item of state.catalog?.models || []) index.set(item.model, item);
    return index;
  }

  function renderAll() {
    const activeBudgets = state.budgets.filter((item) => item.enabled).length;
    setText("stat-models", formatNumber(state.models.length));
    setText("stat-catalog", formatNumber(state.catalog?.models?.length || 0));
    setText("stat-budgets", formatNumber(activeBudgets));
    setText("stat-audit", formatNumber(state.audit.length));
    setText("models-badge", formatNumber(state.models.length));
    setText("budgets-badge", formatNumber(activeBudgets));
    setText("catalog-version", state.catalog?.version ? `Version ${state.catalog.version}` : "Runtime registry");
    renderOverview();
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

  function renderModels() {
    const body = $("models-table"); clear(body);
    const query = $("model-search").value.trim().toLowerCase();
    const catalog = catalogByModel();
    const visible = state.models.filter((model) => `${model.id} ${model.owned_by || ""}`.toLowerCase().includes(query));
    $("models-empty").hidden = visible.length !== 0;
    for (const model of visible) {
      const catalogItem = catalog.get(model.id) || {};
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
  $("model-search").addEventListener("input", renderModels);
  for (const item of document.querySelectorAll("[data-view]")) item.addEventListener("click", () => switchView(item.dataset.view));
  for (const item of document.querySelectorAll("[data-open-view]")) item.addEventListener("click", () => switchView(item.dataset.openView));

  if (state.token) {
    loadData().then(showConsole).catch((error) => { sessionStorage.removeItem("ai_gateway_admin_token"); state.token = ""; showLogin(error.message); });
  } else showLogin();
})();
