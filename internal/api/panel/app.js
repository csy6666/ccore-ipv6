const byId = (id) => document.getElementById(id);
const managedRows = byId("managed-rows");
const interfaceList = byId("interface-list");
const notice = byId("notice");
let currentState;
const probeResults = new Map();

function node(tag, className, text) {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (text !== undefined) element.textContent = text;
  return element;
}

function setNotice(message, kind = "") {
  notice.textContent = message;
  notice.dataset.kind = kind;
}

async function request(path, options = {}) {
  const response = await fetch(path, {
    cache: "no-store",
    headers: { "Content-Type": "application/json", ...options.headers },
    ...options,
  });
  if (!response.ok) {
    let message = `请求失败 (${response.status})`;
    try {
      const payload = await response.json();
      if (payload.error) message = payload.error;
    } catch (_) {}
    throw new Error(message);
  }
  if (response.status === 204) return null;
  return response.json();
}

function managedKey(iface, prefix) {
  return `${iface}|${prefix}`;
}

function renderProbe(item) {
  const result = probeResults.get(managedKey(item.interface, item.prefix));
  if (!result) return node("span", "probe-status", "未检测");
  const status = result.http_ok ? "通过" : result.tcp_ok ? "仅 TCP 可达" : "失败";
  const kind = result.http_ok ? "ok" : result.tcp_ok ? "partial" : "failed";
  const detail = result.error || `${result.http_status ? `HTTP ${result.http_status} · ` : ""}${result.latency_ms} ms`;
  const badge = node("span", `probe-status ${kind}`, status);
  badge.title = detail;
  return badge;
}

function renderManaged(state) {
  const rows = state.addresses || [];
  byId("managed-count").textContent = String(rows.length);
  byId("table-count").textContent = String(rows.length);
  managedRows.replaceChildren();
  if (!rows.length) {
    const row = node("tr");
    const cell = node("td", "empty", "还没有托管地址");
    cell.colSpan = 5;
    row.append(cell);
    managedRows.append(row);
    return;
  }
  for (const item of rows) {
    const row = node("tr");
    row.append(node("td", "", item.prefix));
    row.append(node("td", "", item.interface));
    row.append(node("td", "", new Date(item.created_at).toLocaleString()));
    const probeCell = node("td");
    probeCell.append(renderProbe(item));
    row.append(probeCell);
    const actionCell = node("td");
    const button = node("button", "button button-remove", "删除");
    button.type = "button";
    button.title = `删除托管地址 ${item.prefix}`;
    button.addEventListener("click", () => removeAddress(item));
    actionCell.append(button);
    row.append(actionCell);
    managedRows.append(row);
  }
}

function fillInterfaces(interfaces) {
  const select = byId("interface");
  const previous = select.value;
  select.replaceChildren();
  const usable = interfaces.filter((item) => item.up && item.addresses.some((address) => address.scope === "global"));
  for (const item of usable) {
    const option = node("option", "", `${item.name} · ${item.addresses.filter((address) => address.scope === "global").length} 个全局地址`);
    option.value = item.name;
    select.append(option);
  }
  if (usable.some((item) => item.name === previous)) select.value = previous;
  else if (usable.some((item) => item.route)) select.value = usable.find((item) => item.route).name;
  updatePrefixHint(usable);
}

function updatePrefixHint(interfaces = currentState?.interfaces || []) {
  const selected = interfaces.find((item) => item.name === byId("interface").value);
  const globalAddress = selected?.addresses.find((address) => address.scope === "global");
  const prefixInput = byId("prefix");
  if (globalAddress) {
    const prefix = `${globalAddress.addr}/${globalAddress.prefix_len}`;
    prefixInput.value = prefix;
    byId("prefix-hint").textContent = `地址将从接口前缀 ${prefix} 中随机生成。`;
  } else {
    byId("prefix-hint").textContent = "此接口没有可用于生成地址的全局 IPv6 前缀。";
  }
}

function renderInterfaces(interfaces, state) {
  const managed = new Set((state.addresses || []).map((item) => managedKey(item.interface, item.prefix)));
  const globalCount = interfaces.reduce((sum, item) => sum + item.addresses.filter((address) => address.scope === "global").length, 0);
  const routeCount = interfaces.filter((item) => item.route).length;
  byId("global-count").textContent = String(globalCount);
  byId("route-count").textContent = String(routeCount);
  byId("interface-count").textContent = `${interfaces.length} 个接口`;
  interfaceList.replaceChildren();
  if (!interfaces.length) {
    interfaceList.append(node("div", "empty-block", "没有检测到可用网络接口。"));
    return;
  }
  for (const item of interfaces) {
    const section = node("article", "interface-row");
    const heading = node("div", "interface-head");
    const name = node("div", "interface-name");
    name.append(node("i", `status-dot${item.up ? " up" : ""}`));
    name.append(node("span", "", item.name));
    heading.append(name, node("span", "interface-meta", `index ${item.index} · ${item.up ? "运行中" : "已关闭"}`));
    section.append(heading);
    const addresses = node("div", "address-list");
    for (const address of item.addresses) {
      const line = node("div", "address-line");
      line.append(node("span", "", `${address.addr}/${address.prefix_len}`));
      const isManaged = managed.has(managedKey(item.name, `${address.addr}/${address.prefix_len}`));
      line.append(node("span", `address-badge${isManaged ? " managed" : ""}`, isManaged ? "托管" : address.scope));
      addresses.append(line);
    }
    if (!item.addresses.length) addresses.append(node("div", "muted", "暂无 IPv6 地址"));
    section.append(addresses);
    if (item.route) {
      const route = item.route;
      const parts = ["默认路由"];
      if (route.gateway) parts.push(`网关 ${route.gateway}`);
      if (route.source) parts.push(`源地址 ${route.source}`);
      else parts.push("源地址由内核选择");
      section.append(node("div", "route-line", parts.join(" · ")));
    }
    interfaceList.append(section);
  }
}

async function refresh() {
  byId("refresh").disabled = true;
  try {
    const state = await request("/v1/state");
    currentState = state;
    fillInterfaces(state.interfaces || []);
    renderManaged(state.state || {});
    renderInterfaces(state.interfaces || [], state.state || {});
    byId("updated").textContent = `更新于 ${new Date().toLocaleTimeString()}`;
  } catch (error) {
    setNotice(`无法读取服务状态：${error.message}`, "error");
    byId("updated").textContent = "连接失败";
  } finally {
    byId("refresh").disabled = false;
  }
}

async function removeAddress(item) {
  if (!window.confirm(`删除 ${item.prefix}（${item.interface}）？\n此操作会从网卡移除地址。`)) return;
  setNotice("正在删除地址…");
  try {
    await request("/v1/addresses", {
      method: "DELETE",
      body: JSON.stringify({ interface: item.interface, prefix: item.prefix }),
    });
    setNotice(`已删除 ${item.prefix}`, "success");
    await refresh();
  } catch (error) {
    setNotice(`删除失败：${error.message}`, "error");
  }
}

byId("add-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = byId("add-submit");
  button.disabled = true;
  setNotice("正在生成地址并等待重复地址检测…");
  try {
    const result = await request("/v1/addresses", {
      method: "POST",
      body: JSON.stringify({
        interface: byId("interface").value,
        prefix: byId("prefix").value.trim(),
        count: Number(byId("count").value),
      }),
    });
    setNotice(`已添加 ${result.added.length} 个地址。`, "success");
    await refresh();
  } catch (error) {
    setNotice(`添加失败：${error.message}`, "error");
  } finally {
    button.disabled = false;
  }
});

byId("interface").addEventListener("change", () => updatePrefixHint());
byId("refresh").addEventListener("click", refresh);
byId("probe").addEventListener("click", async () => {
  const button = byId("probe");
  button.disabled = true;
  setNotice("正在从每个托管地址发起 IPv6 出站检测…");
  try {
    const result = await request("/v1/probes", { method: "POST" });
    for (const item of result.results || []) probeResults.set(managedKey(item.interface, item.prefix), item);
    renderManaged(currentState?.state || {});
    const partial = result.tcp_only || 0;
    setNotice(`检测完成：${result.passed || 0} 个通过，${partial} 个仅 TCP 可达，${result.failed || 0} 个失败。`, result.failed ? "error" : "success");
  } catch (error) {
    setNotice(`检测失败：${error.message}`, "error");
  } finally {
    button.disabled = false;
  }
});
byId("restore").addEventListener("click", async () => {
  const button = byId("restore");
  button.disabled = true;
  setNotice("正在恢复托管地址…");
  try {
    const result = await request("/v1/restore", { method: "POST" });
    const failed = result.failed || 0;
    setNotice(`恢复完成：新增 ${result.restored}，已存在 ${result.present}，失败 ${failed}。`, failed ? "error" : "success");
    await refresh();
  } catch (error) {
    setNotice(`恢复失败：${error.message}`, "error");
  } finally {
    button.disabled = false;
  }
});

refresh();
