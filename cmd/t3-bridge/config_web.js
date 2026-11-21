"use strict";

const form = document.querySelector("#config-form");
const removedRoutes = [];
const removedAccounts = [];
let importingConfig = false;

const workbenchTabs = [...document.querySelectorAll("[data-workbench-tab]")];
function showWorkbenchPage() {
  const hash = location.hash;
  const target = document.getElementById(hash.slice(1));
  const directTab = workbenchTabs.find((tab) => tab.hash === hash);
  const page = directTab?.hash.slice(1) ??
    target?.closest("[data-workbench-page]")?.dataset.workbenchPage ??
    form.dataset.workbenchDefault;
  for (const panel of form.querySelectorAll("[data-workbench-page]")) {
    panel.toggleAttribute("data-pane-active", panel.dataset.workbenchPage === page);
  }
  for (const tab of workbenchTabs) {
    if (tab.hash === `#${page}`) tab.setAttribute("aria-current", "page");
    else tab.removeAttribute("aria-current");
  }
  document.documentElement.classList.add("ready-page");
  for (let details = target?.closest("details"); details; details = details.parentElement.closest("details")) {
    details.open = true;
  }
  if (target?.id === "primary-routes") {
    const route = target.querySelector("[data-route]");
    if (route) route.open = true;
  }
  if (target) target.scrollIntoView({ block: "start" });
  else if (directTab) window.scrollTo(0, 0);
}
window.addEventListener("hashchange", showWorkbenchPage);
showWorkbenchPage();

const draftImport = document.querySelector("#draft-import");
draftImport.hidden = false;
draftImport.querySelector("input").addEventListener("change", async (event) => {
  const file = event.target.files[0];
  if (!file) return;
  const status = document.querySelector("#draft-import-status");
  event.target.value = "";
  if (file.size > 1024 * 1024) {
    status.textContent = workspaceText("配置文件超过 1 MiB。");
    return;
  }
  try {
    form.elements.config.value = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(await file.arrayBuffer());
  } catch {
    status.textContent = workspaceText("无法读取 UTF-8 配置文件。");
    return;
  }
  markEdited();
  importingConfig = true;
  form.requestSubmit(form.querySelector('button[value="check"]'));
});

const bindStart = document.querySelector("#bind-start");
const refreshBindTargets = bindStart && configureBinding(bindStart);
const manualReplaceApp = form.elements.replaceAppId;
const replacementSecret = document.querySelector("#replacement-secret");
const replacementSecretSubmit = document.querySelector("#replacement-secret-submit");
refreshReplacementTargets(manualReplaceApp);

function refreshReplacementTargets(select) {
  if (!select) return;
  const fields = Array.from(form.querySelectorAll('[data-feishu] [data-key="appId"]'))
    .filter((field) => /^cli_[A-Za-z0-9_-]{1,124}$/.test(field.value));
  const selected = fields.find((field) => field.value === select.value);
  const options = fields.map((field) => new Option(
    `${field.closest("[data-account]") ? workspaceText("附加账号") : workspaceText("主账号")} · ${field.value}`, field.value,
  ));
  select.replaceChildren(new Option(workspaceText("请选择账号"), ""), ...options);
  select.value = selected?.value ?? "";
  select.disabled = !fields.length;
  return selected;
}

function configureBinding(bindStart) {
  const cancel = document.querySelector("#bind-cancel");
  const image = document.querySelector("#bind-image");
  const link = document.querySelector("#bind-url");
  const apply = document.querySelector("#bind-apply");
  const status = document.querySelector("#bind-status");
  const comments = document.querySelector("#bind-comments");
  const replaceMode = document.querySelector("#bind-replace");
  const replaceOptions = document.querySelector("#bind-replace-options");
  const replaceApp = document.querySelector("#bind-replace-app");
  const bindingID = form.elements.bindingId;
  const replacementKey = "bindReplacementId";
  let timer = 0;
  let generation = 0;
  let replacement;

  function refreshTargets() {
    if (bindStart.disabled) return;
    const selected = refreshReplacementTargets(replaceApp);
    replaceMode.closest("label").hidden = replaceApp.disabled && !replaceMode.checked;
    replaceOptions.hidden = !replaceMode.checked;
    return selected;
  }

  function lockSelection(locked) {
    bindStart.disabled = replaceMode.disabled = replaceApp.disabled = comments.disabled = locked;
  }

  function reset() {
    clearTimeout(timer);
    bindingID.value = "";
    replacement = undefined;
    replaceMode.checked = false;
    replaceApp.value = "";
    image.hidden = link.hidden = apply.hidden = cancel.hidden = true;
    image.removeAttribute("src");
    link.removeAttribute("href");
    lockSelection(false);
    refreshTargets();
  }

  function cancelBinding() {
    generation++;
    if (bindingID.value) {
      fetch("/bind/cancel?id=" + encodeURIComponent(bindingID.value), { method: "POST" }).catch(() => {});
    }
    reset();
  }

  async function poll(id, attempt) {
    const response = await fetch("/bind/status?id=" + encodeURIComponent(id));
    if (!response.ok) throw new Error();
    const data = await response.json();
    if (attempt !== generation) return;
    if (data.state === "starting" || data.state === "pending") {
      if (data.state === "pending") {
        image.src = data.image;
        link.href = data.url;
        image.hidden = link.hidden = false;
        status.textContent = workspaceText("请在飞书完成授权，二维码约 ") + Math.ceil(data.expireIn / 60) + workspaceText(" 分钟内有效。");
      }
      timer = setTimeout(() => poll(id, attempt).catch(() => failed(attempt)), 1000);
    } else {
      if (data.state !== "scanned") throw new Error();
      image.hidden = link.hidden = true;
      apply.hidden = false;
      status.textContent = replacement ?
        `${workspaceText("替换账号 ")}${replacement.id} → ${data.appId}${workspaceText("。请确认保存凭据并审阅草稿差异。")}` :
        `${workspaceText("已收到应用 ")}${data.appId}${workspaceText("，请确认保存凭据。")}`;
    }
  }

  function failed(attempt) {
    if (attempt !== generation) return;
    if (replacement) cancelBinding();
    else reset();
    status.textContent = workspaceText("扫码未完成或已过期，请重新生成二维码。");
  }

  replaceMode.checked = false;
  replaceMode.addEventListener("change", () => {
    if (!replaceMode.checked) replaceApp.value = "";
    status.textContent = "";
    refreshTargets();
  });

  bindStart.addEventListener("click", async () => {
    const selected = refreshTargets();
    if (replaceMode.checked) {
      if (changed(form.elements.config)) {
        status.textContent = workspaceText("完整 JSON 草稿已更改，请先校验再选择要替换的账号。");
        return;
      }
      if (!selected) {
        status.textContent = workspaceText("请选择要替换的账号。");
        replaceApp.focus();
        return;
      }
      replacement = { field: selected, id: selected.value };
    }
    const attempt = ++generation;
    lockSelection(true);
    status.textContent = workspaceText("正在生成二维码…");
    try {
      const query = new URLSearchParams({ comments: comments.checked });
      if (replacement) query.set("replaceAppId", replacement.id);
      const response = await fetch("/bind/start?" + query, { method: "POST" });
      if (!response.ok) throw new Error();
      const { id } = await response.json();
      if (attempt !== generation) return;
      bindingID.value = id;
      cancel.hidden = false;
      await poll(id, attempt);
    } catch {
      failed(attempt);
    }
  });

  cancel.addEventListener("click", () => {
    cancelBinding();
    status.textContent = workspaceText("扫码已取消。");
  });
  form.addEventListener("submit", (event) => {
    if (event.submitter !== apply || !replacement) return;
    if (changed(form.elements.config) || !form.contains(replacement.field) ||
      replacement.field.value !== replacement.id) {
      event.preventDefault();
      cancelBinding();
      status.textContent = workspaceText("草稿中的原账号或完整 JSON 已更改，请重新选择账号并扫码。");
      return;
    }
    // A rejected bind POST re-renders the page with its binding ID for ordinary retries.
    sessionStorage.setItem(replacementKey, bindingID.value);
  });

  refreshTargets();
  if (bindingID.value) {
    if (sessionStorage.getItem(replacementKey) === bindingID.value) {
      cancelBinding();
      status.textContent = workspaceText("替换未完成，请核对草稿后重新扫码。");
    } else {
      lockSelection(true);
      cancel.hidden = false;
      const attempt = generation;
      poll(bindingID.value, attempt).catch(() => failed(attempt));
    }
  }
  sessionStorage.removeItem(replacementKey);
  return refreshTargets;
}

function markEdited() {
  const status = document.querySelector("#check-status");
  status.textContent = workspaceText("待校验");
  status.className = "";
  for (const button of form.querySelectorAll('button[value="download"], button[value="save"]')) {
    button.disabled = true;
  }
}

function accountPath(element) {
  const account = element.closest("[data-account]");
  return account ? ["additionalAccounts", account.dataset.account] : [];
}

const feishuFields = ["appId", "appSecretFile", "tenant"];

function feishuForm(account) {
  return new URLSearchParams(feishuFields.map((key) => [
    key, account.querySelector(`[data-key="${key}"]`).value,
  ])).toString();
}

async function checkFeishuApp(button) {
  const account = button.closest("[data-feishu]");
  const status = account.querySelector("[data-feishu-status]");
  const body = feishuForm(account);
  status.textContent = workspaceText("正在检查应用 API…");
  button.disabled = true;
  let message;
  try {
    const response = await fetch("/feishu/check", { method: "POST", body: new URLSearchParams(body) });
    message = response.ok ? workspaceText("应用 API 已联通。") : (await response.text()).trim();
  } catch {
    message = workspaceText("无法完成应用 API 检查。");
  } finally {
    button.disabled = false;
  }
  status.textContent = body === feishuForm(account) ? message : workspaceText("应用信息已更改，请重新检查。");
  const connectionStatus = document.querySelector("#connection-status");
  connectionStatus.textContent = workspaceText("见各账号检查结果");
  connectionStatus.parentElement.hidden = false;
}

const groupQueries = new WeakMap();

function resetFeishuGroups(access) {
  groupQueries.delete(access);
  access.querySelector("[data-groups-picker]").hidden = true;
  access.querySelector("[data-groups-filter]").value = "";
  access.querySelector("[data-groups-options]").replaceChildren();
  access.querySelector("[data-groups-status]").textContent = "";
}

async function listFeishuGroups(button) {
  const access = button.closest("[data-group-access]");
  const account = access.closest(".workspace").querySelector("[data-feishu]");
  resetFeishuGroups(access);
  const status = access.querySelector("[data-groups-status]");
  if (changed(form.elements.config)) {
    status.textContent = workspaceText("完整 JSON 草稿已更改，请先校验再查询群聊。");
    return;
  }
  const body = feishuForm(account);
  status.textContent = workspaceText("正在查询机器人所在群…");
  button.disabled = true;
  const request = { body };
  groupQueries.set(access, request);
  try {
    const response = await fetch("/feishu/groups", { method: "POST", body: new URLSearchParams(body) });
    if (!response.ok) throw new Error();
    const groups = await response.json();
    if (groupQueries.get(access) !== request || body !== feishuForm(account)) return;
    const options = access.querySelector("[data-groups-options]");
    for (const { id, name } of groups) {
      const label = document.createElement("label");
      label.className = "group-option";
      const checkbox = document.createElement("input");
      checkbox.type = "checkbox";
      checkbox.value = id;
      const text = document.createElement("span");
      text.textContent = name ? `${name} · ${id}` : id;
      label.append(checkbox, text);
      options.append(label);
    }
    access.querySelector("[data-groups-picker]").hidden = groups.length === 0;
    status.textContent = groups.length ? `${workspaceText("找到 ")}${groups.length}${workspaceText(" 个群。")}` : workspaceText("该机器人当前没有可列出的群。");
  } catch {
    if (groupQueries.get(access) === request) status.textContent = workspaceText("无法读取群列表。请核对应用信息、群聊权限和网络。");
  } finally {
    button.disabled = false;
  }
}

function appendFeishuGroupDraft(access, ids) {
  const field = access.querySelector('[data-key="chats"]');
  const current = new Set(field.value.split("\n").map((id) => id.trim()));
  const next = ids.filter((id) => !current.has(id));
  if (next.length) {
    field.value = [field.value.trimEnd(), ...next].filter(Boolean).join("\n");
    markEdited();
  }
  return next.length;
}

function addFeishuGroups(button) {
  const access = button.closest("[data-group-access]");
  const account = access.closest(".workspace").querySelector("[data-feishu]");
  if (changed(form.elements.config) || groupQueries.get(access)?.body !== feishuForm(account)) return;
  const ids = Array.from(access.querySelectorAll("[data-groups-options] input:checked"), (input) => input.value);
  const added = appendFeishuGroupDraft(access, ids);
  const status = access.querySelector("[data-groups-status]");
  if (!added) {
    status.textContent = workspaceText("请选择尚未加入草稿的群。");
    return;
  }
  resetFeishuGroups(access);
  status.textContent = `${workspaceText("已向群聊草稿加入 ")}${added}${workspaceText(" 个群。")}`;
}

const resetMyGroups = new WeakMap();

for (const panel of form.querySelectorAll("[data-my-groups]")) {
  const field = (name) => panel.querySelector(`[data-my-${name}]`);
  const button = (action) => panel.querySelector(`[data-my-action="${action}"]`);
  const access = panel.closest("[data-group-access]");
  const account = access.closest(".workspace").querySelector("[data-feishu]");
  const controls = field("controls");
  const status = field("status");
  const selection = field("selection");
  const query = field("query");
  const link = field("link");
  const runtime = field("runtime");
  const runtimeLink = runtime.querySelector("a");
  const image = field("image");
  let request;
  let ticket = "";
  let identity = "";
  let authorization = {};
  let page = {};
  let pageQuery = "";
  let confirmedGroup = "";
  const attempted = new Set();

  function closeAuthorization() {
    ticket = "";
    field("authorization").hidden = true;
    link.removeAttribute("href");
    image.removeAttribute("src");
  }

  function updateSelection() {
    confirmedGroup = "";
    field("confirmation").hidden = true;
    const group = page.groups?.find(({ id }) => id === selection.value);
    button("add").hidden = !group?.botJoined;
    button("invite").hidden = !authorization.inviteAuthorized || !group || group.botJoined || attempted.has(group.id);
    runtime.hidden = true;
    runtimeLink.removeAttribute("href");
    if (group?.botJoined === true && identity === feishuForm(account)) {
      const identityFields = new URLSearchParams(identity);
      const appId = identityFields.get("appId").trim();
      const tenant = identityFields.get("tenant").trim() || "feishu";
      runtimeLink.href = "/runtime/groups?" + new URLSearchParams({ appId, tenant, chatId: group.id });
      runtime.hidden = false;
    }
  }

  function showAuthorization(result) {
    authorization = result;
    button("authorize-list").hidden = result.listAuthorized;
    button("authorize-invite").hidden = !result.listAuthorized || result.inviteAuthorized;
  }

  resetMyGroups.set(access, () => {
    request?.abort();
    request = undefined;
    controls.disabled = false;
    identity = "";
    attempted.clear();
    page = {};
    pageQuery = query.value = "";
    selection.replaceChildren();
    field("picker").hidden = true;
    button("authorize-list").hidden = button("authorize-invite").hidden = true;
    authorization = {};
    closeAuthorization();
    updateSelection();
    status.textContent = "";
  });

  function showPage(result, currentQuery) {
    page = result;
    pageQuery = currentQuery;
    const options = (page.groups || []).map((group) => {
      const membership = group.botJoined ? workspaceText("机器人已入群") : (page.botKnown ? workspaceText("机器人未入群") : workspaceText("入群状态未确认"));
      return new Option([group.name, group.id, membership].filter(Boolean).join(" · "), group.id);
    });
    selection.replaceChildren(new Option(workspaceText("请选择群聊"), ""), ...options);
    field("picker").hidden = false;
    button("next").hidden = !page.nextPageToken;
    updateSelection();
    status.textContent = options.length ? `${workspaceText("本页有 ")}${options.length}${workspaceText(" 个群。")}` : workspaceText("本页没有群聊。");
  }

  async function operate(action) {
    if (request) return;
    if (changed(form.elements.config)) {
      status.textContent = workspaceText("完整 JSON 草稿已更改，请先校验再查询群聊。");
      return;
    }
    const body = feishuForm(account);
    if (identity && identity !== body) {
      resetMyGroups.get(access)();
    }
    identity = body;
    const credentials = Object.fromEntries(new URLSearchParams(body));
    const group = page.groups?.find(({ id }) => id === selection.value);
    if (action === "add" && group?.botJoined) {
      status.textContent = appendFeishuGroupDraft(access, [group.id]) ?
        workspaceText("已加入群聊草稿。请校验后保存。") : workspaceText("该群已在草稿中。");
      return;
    }
    if (action === "invite" && group && !button("invite").hidden) {
      confirmedGroup = group.id;
      field("target").textContent = `${workspaceText("邀请应用 ")}${credentials.appId}${workspaceText(" 的机器人加入 ")}${group.name || group.id}（${group.id}）？`;
      field("confirmation").hidden = false;
      return;
    }
    if (action === "back") {
      updateSelection();
      return;
    }
    if (action === "confirm" && (!group || confirmedGroup !== group.id || button("invite").hidden)) return;

    const controller = request = new AbortController();
    controls.disabled = true;
    status.textContent = workspaceText("正在处理…");
    async function call(action, parameters = {}) {
      const response = await fetch("/feishu/my-groups", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        referrerPolicy: "same-origin",
        signal: controller.signal,
        body: JSON.stringify({ ...credentials, action, ...parameters }),
      });
      if (!response.ok) throw new Error(await response.text());
      const result = await response.json();
      if (request !== controller || body !== feishuForm(account) || !panel.isConnected ||
        changed(form.elements.config)) throw new Error();
      return result;
    }

    try {
      if (action === "open" || action === "finish") {
        const result = await call(action === "open" ? "status" : "finish", action === "finish" ? { ticket } : {});
        if (action === "finish") closeAuthorization();
        showAuthorization(result);
        if (result.listAuthorized) showPage(await call("list", { query: query.value.trim() }), query.value.trim());
        else status.textContent = workspaceText("请先授权当前应用的所有者查群。");
      } else if (action === "authorize-list" || action === "authorize-invite") {
        const result = await call("start", { purpose: action === "authorize-list" ? "list" : "invite" });
        ticket = result.ticket;
        link.href = result.url;
        image.src = result.image;
        field("authorization").hidden = false;
        status.textContent = workspaceText("请用飞书扫码，或在浏览器打开授权链接。");
      } else if (action === "cancel") {
        await call("cancel", { ticket });
        closeAuthorization();
        status.textContent = workspaceText("已取消授权。");
      } else if (action === "search" || action === "next") {
        const currentQuery = action === "next" ? pageQuery : query.value.trim();
        if (action === "next") query.value = pageQuery;
        showPage(await call("list", {
          query: currentQuery, pageToken: action === "next" ? page.nextPageToken : "",
        }), currentQuery);
      } else if (action === "confirm") {
        attempted.add(group.id);
        updateSelection();
        const result = await call("invite", { chatId: group.id, confirm: true });
        if (result.state === "joined") {
          group.botJoined = true;
          const selected = group.id;
          showPage(page, pageQuery);
          selection.value = selected;
          updateSelection();
        }
        status.textContent = {
          joined: workspaceText("已确认机器人入群。现在可以加入群聊草稿。"),
          pending: workspaceText("邀请已提交，等待群审批。"),
          rejected: workspaceText("飞书拒绝了邀请。"),
          unknown: workspaceText("邀请结果未确认，请先核对机器人入群状态。"),
          "confirmation-required": workspaceText("lark-cli 要求额外确认，请在飞书中核对入群操作。"),
        }[result.state] || workspaceText("邀请结果未确认，请先核对机器人入群状态。");
      }
    } catch (error) {
      if (request === controller) status.textContent = error.message?.trim() || workspaceText("无法完成操作。");
    } finally {
      if (request === controller) {
        request = undefined;
        controls.disabled = false;
      }
    }
  }

  selection.addEventListener("change", updateSelection);
  query.addEventListener("keydown", (event) => {
    if (event.key === "Enter") {
      event.preventDefault();
      operate("search");
    }
  });
  panel.addEventListener("click", (event) => {
    const action = event.target.closest("[data-my-action]")?.dataset.myAction;
    if (action) operate(action);
  });
}

const targetChoices = new WeakMap();

function readT3Connection(mcp) {
  return Object.fromEntries([...mcp.querySelectorAll("[data-key]")].map((field) => [
    field.dataset.key, field.value.trim(),
  ]));
}

const t3AuthorizationButtons = form.querySelectorAll("[data-t3-auth-start]");
for (const panel of form.querySelectorAll("[data-t3-authorize]")) {
  const start = panel.querySelector("[data-t3-auth-start]");
  const cancel = panel.querySelector("[data-t3-auth-cancel]");
  const link = panel.querySelector("[data-t3-auth-link]");
  const address = panel.querySelector("[data-t3-auth-address]");
  const status = panel.querySelector("[data-t3-auth-status]");
  let id = "";

  function finish(message) {
    id = "";
    for (const button of t3AuthorizationButtons) button.disabled = false;
    link.hidden = address.hidden = cancel.hidden = true;
    link.removeAttribute("href");
    address.querySelector("input").value = "";
    status.textContent = message;
  }

  async function poll() {
    const response = await fetch("/t3/authorize/status?id=" + encodeURIComponent(id));
    if (!response.ok) throw new Error();
    const result = await response.json();
    if (result.state === "starting" || result.state === "pending") {
      if (result.url) {
        link.href = address.querySelector("input").value = result.url;
        link.hidden = address.hidden = false;
        status.textContent = workspaceText("请在本机浏览器批准授权。Bridge 最长等待 5 分钟。");
      }
      setTimeout(() => poll().catch(() => finish(workspaceText("无法读取 T3 授权状态。"))), 750);
      return;
    }
    switch (result.state) {
      case "saved":
        finish(workspaceText("凭据已保存，可查询项目和模型。"));
        break;
      case "expired":
        finish(workspaceText("Bridge 等待 T3 授权已超时。请重新准备授权。"));
        break;
      case "canceled":
        finish(workspaceText("T3 授权已取消。"));
        break;
      default:
        finish(result.problem || workspaceText("T3 授权未完成。"));
    }
  }

  start.addEventListener("click", async () => {
    status.textContent = workspaceText("正在准备授权…");
    for (const button of t3AuthorizationButtons) button.disabled = true;
    cancel.disabled = false;
    try {
      const response = await fetch("/t3/authorize/start", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          connection: readT3Connection(panel.closest('[data-section="mcp"]')),
          replace: panel.querySelector("[data-t3-auth-replace]").checked,
        }),
      });
      if (!response.ok) {
        finish((await response.text()).trim());
        return;
      }
      ({ id } = await response.json());
      cancel.hidden = false;
      await poll();
    } catch {
      finish(workspaceText("无法读取 T3 授权状态。"));
    }
  });

  cancel.addEventListener("click", async () => {
    cancel.disabled = true;
    try {
      const response = await fetch("/t3/authorize/cancel?id=" + encodeURIComponent(id), { method: "POST" });
      if (!response.ok) throw new Error();
    } catch {
      cancel.disabled = false;
    }
  });
}

function targetRequest(chooser) {
  const mcp = chooser.closest(".workspace").querySelector('[data-section="mcp"]');
  const route = chooser.closest("[data-route]");
  const connection = readT3Connection(mcp);
  const environment = route.querySelector('[data-key="environmentId"]').value.trim();
  const placeholder = "REPLACE_ENVIRONMENT_ID";
  if (connection.environmentId === placeholder) connection.environmentId = "";
  return JSON.stringify({
    connection,
    environmentId: environment === placeholder ? "" : environment,
    search: chooser.querySelector("[data-target-search]").value,
    projectId: route.querySelector('[data-key="projectId"]').value,
    workspacePath: route.querySelector('[data-key="projectPath"]').value,
  });
}

function targetDraft(route) {
  return JSON.stringify([...route.querySelectorAll("[data-key]")].map((field) =>
    field.type === "checkbox" ? field.checked : field.value,
  ));
}

function targetChoicesCurrent(chooser, { body, draft }) {
  return form.contains(chooser) && !changed(form.elements.config) &&
    body === targetRequest(chooser) && draft === targetDraft(chooser.closest("[data-route]"));
}

function matchesTargetModel(route, { item, provider }) {
  const read = (key) => route.querySelector(`[data-key="${key}"]`).value;
  return read("model") === item.id &&
    read("driver") === provider.driverKind && read("providerId") === provider.providerInstanceId;
}

function sameTargetBinding(route, catalog, selected) {
  return route.querySelector('[data-key="environmentId"]').value === catalog.environmentId &&
    matchesTargetModel(route, selected);
}

function targetOptionSelect({ id, values }, current) {
  const label = document.createElement("label");
  label.textContent = id;
  const select = document.createElement("select");
  select.dataset.targetOption = id;
  select.add(new Option(workspaceText("未设置（使用默认值）"), ""));
  for (const value of values || []) {
    if (typeof value !== "string" && typeof value !== "boolean") continue;
    const json = JSON.stringify(value);
    select.add(new Option(json, json));
  }
  if (Object.prototype.hasOwnProperty.call(current, id)) {
    select.value = JSON.stringify(current[id]);
    if (select.selectedIndex < 0) {
      select.add(new Option(workspaceText("保留当前值（目录未提供）"), "keep"));
      select.value = "keep";
    }
  }
  select.options[select.selectedIndex].defaultSelected = true;
  label.append(select);
  return label;
}

function updateTargetSelection(chooser, modelChanged = true) {
  const choices = targetChoices.get(chooser);
  const modelValue = chooser.querySelector("[data-target-model]").value;
  const project = chooser.querySelector("[data-target-project]").value;
  const selected = modelValue === "" ? null : choices?.models[Number(modelValue)];
  const controls = chooser.querySelector("[data-target-option-fields]");
  const options = chooser.querySelector("[data-target-options]");
  const apply = chooser.querySelector("[data-target-apply]");
  if (choices && !targetChoicesCurrent(chooser, choices)) {
    chooser.querySelector("[data-target-results]").hidden = true;
    apply.disabled = apply.hidden = controls.hidden = options.hidden = true;
    chooser.querySelector("[data-target-status]").textContent = workspaceText("查询条件或路由草稿已更改，请重新查询。");
    return;
  }
  const catalog = choices?.catalog;
  const route = chooser.closest("[data-route]");
  const raw = selected && sameTargetBinding(route, catalog, selected)
    ? fieldJSON(route.querySelector('[data-key="options"]')) : "{}";
  apply.disabled = !selected || !project || raw === null;
  if (!modelChanged) return;
  const fields = !apply.hidden && selected && raw !== null ? (selected.item.options || []) : [];
  const current = raw === null ? null : JSON.parse(raw);
  controls.replaceChildren(...fields.map((option) => targetOptionSelect(option, current)));
  controls.hidden = !fields.length;
  options.hidden = !selected && !catalog?.linkAccess && !catalog?.operateAccess;
  if (options.hidden) return;
  const limits = [`${workspaceText("T3 授权上限 ")}${catalog.runtimeMode}`];
  switch (catalog.operateAccess) {
    case "granted":
      limits.push(workspaceText("本机 OAuth 已授予派发权限"));
      break;
    case "missing":
      limits.push(workspaceText("本机 OAuth 无法派发，请重新授权（至少监督权限）"));
      break;
    case "unknown":
      limits.push(workspaceText("本机 OAuth 派发权限未知（旧凭据未记录权限）"));
      break;
  }
  if (catalog.linkAccess) {
    const warning = catalog.linkAccess === "read-only" ? workspaceText("（只读，无法派发任务）") : "";
    limits.push(`${workspaceText("关联权限 ")}${catalog.linkAccess}${warning}`);
  }
  if (selected?.provider.runtimeModes?.length) {
    limits.push(`${workspaceText("Provider 允许 ")}${selected.provider.runtimeModes.join(" / ")}`);
  }
  if (raw === null) limits.push(workspaceText("请修正路由的模型选项 JSON 后重新查询"));
  options.textContent = limits.join(" · ");
}

async function listTargets(chooser, button) {
  const status = chooser.querySelector("[data-target-status]");
  const project = chooser.querySelector("[data-target-project]");
  const model = chooser.querySelector("[data-target-model]");
  const apply = chooser.querySelector("[data-target-apply]");
  const results = chooser.querySelector("[data-target-results]");
  const route = chooser.closest("[data-route]");
  if (changed(form.elements.config)) {
    status.textContent = workspaceText("完整 JSON 草稿已更改，请先校验再查询 T3。");
    return;
  }
  const identity = { body: targetRequest(chooser), draft: targetDraft(route) };
  targetChoices.delete(chooser);
  button.disabled = project.disabled = model.disabled = apply.disabled = true;
  results.hidden = apply.hidden = true;
  chooser.querySelector("[data-target-options]").hidden = true;
  chooser.querySelector("[data-target-option-fields]").hidden = true;
  status.textContent = workspaceText("正在读取 T3…");
  try {
    const response = await fetch("/t3/catalog", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: identity.body,
    });
    if (!response.ok) throw new Error((await response.text()).trim());
    const catalog = await response.json();
    if (!form.contains(chooser)) return;
    if (!targetChoicesCurrent(chooser, identity)) {
      status.textContent = workspaceText("查询条件或路由草稿已更改，请重新查询。");
      return;
    }
    const models = (catalog.providers || []).flatMap((provider) => (provider.models || []).map((item) => ({
      item, provider,
    })));
    project.replaceChildren(new Option(workspaceText("选择项目"), ""), ...catalog.projects.map((item) =>
      new Option(`${item.title || item.id} · ${item.workspaceRoot}`, item.id),
    ));
    model.replaceChildren(new Option(workspaceText("选择模型"), ""), ...models.map(({ item, provider }, index) =>
      new Option(`${item.label || item.id} · ${provider.displayName || provider.providerInstanceId}`, String(index)),
    ));
    project.value = catalog.suggestedProjectId || "";
    const currentModel = models.findIndex((selected) => matchesTargetModel(route, selected));
    model.value = currentModel < 0 ? "" : String(currentModel);
    targetChoices.set(chooser, { ...identity, catalog, models });
    project.disabled = !catalog.projects.length;
    model.disabled = !models.length;
    results.hidden = apply.hidden = project.disabled || model.disabled;
    updateTargetSelection(chooser);
    const omitted = (catalog.providers || []).some((provider) => provider.omittedModels);
    status.textContent = `${catalog.label || catalog.environmentId} · ${catalog.projects.length}${workspaceText(" 个项目 · ")}${models.length}${workspaceText(" 个模型")}` +
      (omitted ? workspaceText("。其余模型可按名称查询。") : "");
  } catch (error) {
    status.textContent = error.message || workspaceText("无法读取 T3 目标。");
  } finally {
    button.disabled = false;
  }
}

function mergeTargetOptions(raw, changes) {
  if (!changes.size) return raw;
  const entries = [];
  let depth = 0;
  let start = raw.indexOf("{") + 1;
  // Copy untouched JSON members verbatim so manual numbers keep their precision.
  for (const match of raw.matchAll(/"(?:\\.|[^"\\])*"|[{},\[\]]/g)) {
    const token = match[0];
    if (depth === 1 && (token === "," || token === "}")) {
      const entry = raw.slice(start, match.index).trim();
      if (entry && !changes.has(JSON.parse(entry.match(/^"(?:\\.|[^"\\])*"/)[0]))) entries.push(entry);
      start = match.index + 1;
    }
    if (token === "{" || token === "[") depth++;
    if (token === "}" || token === "]") depth--;
  }
  for (const [id, value] of changes) {
    if (value !== "") entries.push(`${JSON.stringify(id)}: ${value}`);
  }
  return entries.length ? `{\n  ${entries.join(",\n  ")}\n}` : "{}";
}

function applyTarget(chooser) {
  const status = chooser.querySelector("[data-target-status]");
  const choices = targetChoices.get(chooser);
  if (!choices || !targetChoicesCurrent(chooser, choices)) {
    status.textContent = workspaceText("查询条件或路由草稿已更改，请重新查询。");
    return;
  }
  const projectID = chooser.querySelector("[data-target-project]").value;
  const model = chooser.querySelector("[data-target-model]").value;
  const { catalog } = choices;
  const project = catalog.projects.find((item) => item.id === projectID);
  if (!project || model === "") {
    status.textContent = workspaceText("请选择项目和模型。");
    return;
  }
  const selected = choices.models[Number(model)];
  const { item, provider } = selected;
  const route = chooser.closest("[data-route]");
  const field = (key) => route.querySelector(`[data-key="${key}"]`);
  const options = field("options");
  const raw = sameTargetBinding(route, catalog, selected) ? fieldJSON(options) : "{}";
  if (raw === null) {
    status.textContent = workspaceText("请修正路由的模型选项 JSON 后重新查询。");
    return;
  }
  const changes = new Map();
  for (const select of chooser.querySelectorAll("[data-target-option]")) {
    if (changed(select)) changes.set(select.dataset.targetOption, select.value);
  }
  options.value = mergeTargetOptions(raw, changes);
  field("environmentId").value = catalog.environmentId;
  field("projectId").value = project.id;
  field("projectName").value = project.title || "";
  field("projectPath").value = project.workspaceRoot || "";
  field("driver").value = provider.driverKind;
  field("providerId").value = provider.providerInstanceId;
  field("model").value = item.id;
  chooser.closest(".workspace").querySelector('[data-section="mcp"] [data-key="environmentId"]').value = catalog.localEnvironmentId;
  choices.body = targetRequest(chooser);
  choices.draft = targetDraft(route);
  updateTargetSelection(chooser);
  status.textContent = workspaceText("目标已填入草稿。请核对模型选项和执行权限。");
  refreshRouteSummary(route);
  markEdited();
}

function refreshRouteSummary(route) {
  const project = route.querySelector('[data-key="projectName"]').value;
  const projectID = route.querySelector('[data-key="projectId"]').value;
  route.querySelector("[data-route-summary-project]").textContent = project || (projectID ? workspaceText("已绑定项目") : workspaceText("未选择项目"));
  route.querySelector("[data-route-summary-model]").textContent = route.querySelector('[data-key="model"]').value;
}

form.addEventListener("change", (event) => {
  if (event.target.matches("[data-target-project], [data-target-model]")) {
    updateTargetSelection(event.target.closest("[data-target-chooser]"), event.target.hasAttribute("data-target-model"));
  }
});

async function applyToolBinding(button) {
  const panel = button.closest("[data-tool-binding]");
  const route = panel.closest("[data-route]");
  const status = panel.querySelector("[data-tool-binding-status]");
  const field = panel.querySelector('[data-key="toolContext"]');
  const environment = route.querySelector('[data-key="environmentId"]');
  if (changed(form.elements.config)) {
    status.textContent = workspaceText("完整 JSON 草稿已更改，请先校验再应用工具配置。");
    return;
  }
  const raw = field.value;
  const current = raw.trim() ? fieldJSON(field) : "null";
  if (current === null) {
    status.textContent = workspaceText("请先修正高级工具配置中的 JSON 对象。");
    return;
  }
  const environmentId = environment.value;
  const fields = [...panel.querySelectorAll("[data-tool-binding-field]")];
  const draft = Object.fromEntries(fields.map((input) => [input.dataset.toolBindingField, input.value]));
  const body = `{"draft":${JSON.stringify(draft)},"environmentId":${JSON.stringify(environmentId)},"current":${current}}`;
  let edited = false;
  const noteEdit = () => { edited = true; };
  panel.addEventListener("input", noteEdit);
  button.disabled = true;
  status.textContent = workspaceText("正在生成工具配置…");
  try {
    const response = await fetch("/tool-binding/draft", {
      method: "POST", mode: "same-origin", headers: { "Content-Type": "application/json" }, body,
    });
    if (!response.ok) throw new Error((await response.text()).trim());
    const { toolContext } = await response.json();
    if (!form.contains(route)) return;
    if (edited || raw !== field.value || environmentId !== environment.value || changed(form.elements.config) ||
        fields.some((input) => input.value !== draft[input.dataset.toolBindingField])) {
      status.textContent = workspaceText("草稿已更改，请重新应用工具配置。");
      return;
    }
    field.value = toolContext === null ? "" : JSON.stringify(toolContext, null, 2);
    field.dispatchEvent(new Event("input", { bubbles: true }));
    status.textContent = toolContext === null ? workspaceText("已从草稿移除工具配置。") : workspaceText("已应用到草稿。");
  } catch (error) {
    status.textContent = error.message || workspaceText("无法生成工具配置。");
  } finally {
    panel.removeEventListener("input", noteEdit);
    button.disabled = false;
  }
}

function addRoute(editor) {
  const routes = editor.querySelector("[data-route-list]");
  const routeID = editor.querySelector("[data-route-id]");
  const id = routeID.value.trim();
  const exists = [...routes.children].some(
    (route) => route.dataset.route.toLowerCase() === id.toLowerCase(),
  );
  if (!id || /[ /\r\n]/.test(id) || exists) {
    routeID.setCustomValidity(workspaceText("请输入尚未使用的路由 ID。"));
    routeID.reportValidity();
    return;
  }
  routeID.setCustomValidity("");
  const route = document.querySelector("#route-template").content.firstElementChild.cloneNode(true);
  route.dataset.route = id;
  route.dataset.newRoute = "";
  route.querySelector("[data-route-summary-id]").textContent = id;
  const account = editor.closest(".workspace").querySelector("[data-feishu]");
  route.querySelector('[data-tool-binding-field="appId"]').value = account.querySelector('[data-key="appId"]').value;
  route.querySelector('[data-tool-binding-field="tenant"]').value = account.querySelector('[data-key="tenant"]').value.trim() || "feishu";
  routes.append(route);
  if (!route.closest("[data-account]")) {
    form.querySelector("[data-new-account-route]").add(new Option(id, id));
  }
  routeID.value = "";
  const parameters = route.querySelector("[data-route-parameters]");
  parameters.open = true;
  parameters.querySelector("input").focus();
  markEdited();
}

form.addEventListener("keydown", (event) => {
  if (event.key !== "Enter") return;
  if (event.target.hasAttribute("data-route-id")) {
    event.preventDefault();
    addRoute(event.target.closest("[data-route-editor]"));
  } else if (event.target.hasAttribute("data-target-search")) {
    event.preventDefault();
    event.target.closest("[data-target-chooser]").querySelector("[data-target-list]").click();
  } else if (event.target === replacementSecret || event.target.matches("[data-groups-filter], input[data-tool-binding-field]")) {
    event.preventDefault();
  }
});

form.addEventListener("click", (event) => {
  const toolBindingButton = event.target.closest("[data-tool-binding-apply]");
  if (toolBindingButton) {
    applyToolBinding(toolBindingButton);
    return;
  }
  const groupButton = event.target.closest("[data-groups-list], [data-groups-add]");
  if (groupButton) {
    if (groupButton.hasAttribute("data-groups-list")) listFeishuGroups(groupButton);
    else addFeishuGroups(groupButton);
    return;
  }
  const feishuButton = event.target.closest("[data-feishu-check]");
  if (feishuButton) {
    checkFeishuApp(feishuButton);
    return;
  }
  const chooserButton = event.target.closest("[data-target-list], [data-target-apply]");
  if (chooserButton) {
    const chooser = chooserButton.closest("[data-target-chooser]");
    if (chooserButton.hasAttribute("data-target-list")) {
      listTargets(chooser, chooserButton);
    } else {
      applyTarget(chooser);
    }
    return;
  }
  const button = event.target.closest("[data-add-route], [data-remove-route], [data-remove-account]");
  if (!button) return;
  if (button.hasAttribute("data-add-route")) {
    addRoute(button.closest("[data-route-editor]"));
  } else {
    if (button.hasAttribute("data-remove-route")) {
      const route = button.closest("[data-route]");
      if (!route.hasAttribute("data-new-route")) {
        removedRoutes.push([...accountPath(route), "routes", route.dataset.route]);
      }
      if (!route.closest("[data-account]")) {
        const select = form.querySelector("[data-new-account-route]");
        [...select.options].find((option) => option.value === route.dataset.route)?.remove();
      }
      route.remove();
    } else {
      const account = button.closest("[data-account]");
      removedAccounts.push(Number(account.dataset.account));
      resetMyGroups.get(account.querySelector("[data-group-access]"))?.();
      account.remove();
      refreshBindTargets?.();
      refreshReplacementTargets(manualReplaceApp);
    }
    markEdited();
  }
});

form.addEventListener("input", (event) => {
  event.target.setCustomValidity("");
  if (event.target.hasAttribute("data-target-search")) {
    const chooser = event.target.closest("[data-target-chooser]");
    if (targetChoices.has(chooser)) updateTargetSelection(chooser, false);
  }
  if (event.target.name === "config") {
    for (const access of form.querySelectorAll("[data-group-access]")) {
      resetFeishuGroups(access);
      resetMyGroups.get(access)?.();
    }
  }
  if (event.target.hasAttribute("data-groups-filter")) {
    const filter = event.target.value.trim().toLowerCase();
    const access = event.target.closest("[data-group-access]");
    let visible = 0;
    for (const item of access.querySelector("[data-groups-options]").children) {
      item.hidden = !item.textContent.toLowerCase().includes(filter);
      if (!item.hidden) visible++;
    }
    access.querySelector("[data-groups-status]").textContent = visible ? `${workspaceText("找到 ")}${visible}${workspaceText(" 个群。")}` : workspaceText("没有匹配的群。");
  }
  if (event.target.hasAttribute("data-list-override")) {
    event.target.closest("[data-section]").querySelector("[data-direct-chats]").hidden = !event.target.checked;
    markEdited();
  }
  const account = event.target.closest("[data-feishu]");
  const key = event.target.dataset.key;
  if (account && feishuFields.includes(event.target.dataset.key)) {
    if (key === "appId") {
      account.closest("details").querySelector("[data-account-summary-id]").textContent = event.target.value;
      refreshBindTargets?.();
      refreshReplacementTargets(manualReplaceApp);
    }
    account.querySelector("[data-feishu-status]").textContent = "";
    const access = account.closest(".workspace").querySelector("[data-group-access]");
    resetFeishuGroups(access);
    resetMyGroups.get(access)?.();
  }
  if (account && key === "receiverAuthorized") {
    const status = account.closest("details").querySelector("[data-account-summary-receiver]");
    status.textContent = event.target.checked ? workspaceText("接收已授权") : workspaceText("接收待授权");
    status.classList.toggle("ok", event.target.checked);
  }
  const route = event.target.closest("[data-route]");
  if (route && ["projectName", "projectId", "model"].includes(key)) refreshRouteSummary(route);
  if (key === "defaultRoute") {
    event.target.closest("details").querySelector("[data-default-route-summary]").textContent = event.target.value;
  }
  if (key === "endpoint" || key === "runtimeFile") {
    const panel = event.target.closest("details");
    const endpoint = panel.querySelector('[data-key="endpoint"]').value;
    const runtimeFile = panel.querySelector('[data-key="runtimeFile"]').value;
    panel.querySelector("[data-mcp-summary]").textContent =
      endpoint || (runtimeFile ? workspaceText("通过本机运行文件连接") : workspaceText("待配置"));
  }
  if (event.target.name === "config" || event.target.hasAttribute("data-key")) {
    markEdited();
  }
});

function changed(field) {
  if (field.type === "checkbox") {
    return field.checked !== field.defaultChecked;
  }
  if (field.tagName === "SELECT") {
    const initial = Array.from(field.options).find((option) => option.defaultSelected) ?? field.options[0];
    return field.value !== (initial?.value ?? "");
  }
  return field.value !== field.defaultValue;
}

function fieldJSON(field) {
  if (field.type === "checkbox") {
    return JSON.stringify(field.checked);
  }
  if (field.hasAttribute("data-list")) {
    const lines = field.value.split("\n").map((line) => line.trim()).filter(Boolean);
    return JSON.stringify(lines);
  }
  if (field.hasAttribute("data-object")) {
    let object;
    try {
      object = JSON.parse(field.value);
    } catch {
      return null;
    }
    // Preserve number literals in options instead of serializing JS numbers.
    return object && typeof object === "object" && !Array.isArray(object) ? field.value : null;
  }
  return JSON.stringify(field.value);
}

form.addEventListener("submit", (event) => {
  replacementSecret?.removeAttribute("name");
  const replaceWithSecret = replacementSecretSubmit && event.submitter === replacementSecretSubmit;
  if (replacementSecret && (replaceWithSecret || event.submitter?.value === "replace-app")) {
    const hasFile = form.elements.replacementAppSecretFile.value.trim() !== "";
    const hasSecret = replacementSecret.value !== "";
    if ((replaceWithSecret && !hasSecret) || (hasSecret && (hasFile || !replaceWithSecret))) {
      event.preventDefault();
      document.querySelector("#replacement-secret-status").textContent =
        workspaceText("请只填写所选操作需要的密钥或文件路径。");
      return;
    }
  }
  if (event.submitter?.value === "load-backup") {
    form.elements.edits.value = "";
    return;
  }
  if (importingConfig) {
    importingConfig = false;
    form.elements.edits.value = "";
    form.elements.rawOpen.value = "true";
    return;
  }
  const useLocalT3 = event.submitter?.hasAttribute("data-use-local-t3");
  const registerDraftProject = event.submitter?.value === "register-draft-project";
  if (registerDraftProject && changed(form.elements.config)) {
    event.preventDefault();
    event.submitter.parentElement.querySelector("[data-draft-project-status]").textContent =
      workspaceText("完整 JSON 草稿已更改，请先校验再选择要登记项目的路由。");
    return;
  }
  if (useLocalT3 && changed(form.elements.config)) {
    event.preventDefault();
    event.submitter.closest("[data-section]").querySelector("[data-local-t3-status]").textContent =
      workspaceText("完整 JSON 草稿已更改，请先校验再选择要配置的账号。");
    return;
  }
  const edits = removedRoutes.map((path) =>
    JSON.stringify({ path, delete: true }),
  );
  for (const section of form.querySelectorAll("[data-section]")) {
    const route = section.hasAttribute("data-route");
    const newRoute = section.hasAttribute("data-new-route");
    const scope = route ? ["routes", section.dataset.route] :
      section.dataset.section ? [section.dataset.section] : [];
    const prefix = [...accountPath(section), ...scope];
    const fields = [];
    for (const field of section.querySelectorAll("[data-key]")) {
      if (field.closest("[data-section]") !== section) continue;
      const override = field.dataset.key === "directChats" ? section.querySelector("[data-list-override]") : null;
      if (override && !override.checked) {
        if (override.defaultChecked) edits.push(JSON.stringify({ path: [...prefix, "directChats"], delete: true }));
        continue;
      }
      if (!newRoute && !changed(field) && !(override && changed(override))) continue;
      if (field.hasAttribute("data-optional-object") && !field.value.trim()) {
        if (!newRoute) edits.push(JSON.stringify({ path: [...prefix, field.dataset.key], delete: true }));
        continue;
      }
      const value = fieldJSON(field);
      if (value === null) {
        field.setCustomValidity(workspaceText("请输入 JSON 对象。"));
        field.reportValidity();
        event.preventDefault();
        return;
      }
      if (newRoute) {
        fields.push(`${JSON.stringify(field.dataset.key)}:${value}`);
      } else {
        const path = JSON.stringify([...prefix, field.dataset.key]);
        edits.push(`{"path":${path},"value":${value}}`);
      }
    }
    if (newRoute) {
      edits.push(`{"path":${JSON.stringify(prefix)},"value":{${fields.join(",")}}}`);
    }
  }
  for (const index of [...removedAccounts].sort((a, b) => b - a)) {
    edits.push(JSON.stringify({ path: ["additionalAccounts", String(index)], delete: true }));
  }
  form.elements.edits.value = `[${edits.join(",")}]`;
  form.elements.rawOpen.value = String(document.querySelector("#raw-config").open);
  if (registerDraftProject) {
    const route = event.submitter.closest("[data-route]");
    form.elements.projectRouteId.value = route.dataset.route;
    form.elements.projectAppId.value = route.closest(".workspace").querySelector('[data-feishu] [data-key="appId"]').value;
  }
  if (useLocalT3) {
    // The server selects the account after applying any pending removals.
    const index = Number(event.submitter.closest("[data-account]")?.dataset.account ?? -1);
    const removedBefore = removedAccounts.filter((removed) => removed < index).length;
    event.submitter.value = `use-local-t3:${index + 1 - removedBefore}`;
  }
  if (replaceWithSecret && !event.defaultPrevented) {
    replacementSecret.name = "replacementSecret";
    // Native submission collects the form fields before the next task.
    setTimeout(() => {
      replacementSecret.removeAttribute("name");
      replacementSecret.value = "";
    }, 0);
  }
});

if (replacementSecret) {
  const controls = document.querySelector("#replacement-secret-controls");
  const status = document.querySelector("#replacement-secret-status");
  replacementSecret.closest("details").addEventListener("input", () => {
    status.textContent = "";
  });
  replacementSecret.disabled = replacementSecretSubmit.disabled = controls.hidden = false;
}
