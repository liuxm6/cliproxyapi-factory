"use strict";

(function () {
  let managementKey = "";
  let busy = false;
  let generation = 0;
  let sensitiveWords = [];
  let compatibilityLoaded = false;
  let cachedCredentials = [];

  const $ = (id) => document.getElementById(id);

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined && text !== null) node.textContent = String(text);
    return node;
  }

  function showToast(message, type = "info") {
    const container = $("toast-container");
    if (!container) return;
    const toast = el("div", "toast");
    const icon = type === "success" ? "✅" : type === "error" ? "❌" : "ℹ️";
    toast.append(el("span", null, icon), el("span", null, message));
    container.appendChild(toast);
    setTimeout(() => {
      toast.style.opacity = "0";
      toast.style.transform = "translateY(10px)";
      toast.style.transition = "all 200ms ease";
      setTimeout(() => toast.remove(), 200);
    }, 3200);
  }

  function setConnected(connected) {
    const badge = $("conn-badge");
    const text = $("conn-text");
    const refreshBtn = $("btn-refresh-all");
    const disconnectBtn = $("btn-disconnect");

    if (connected) {
      badge.className = "badge badge-success";
      text.textContent = "已连接网关";
      refreshBtn.disabled = false;
      disconnectBtn.classList.remove("hidden");
      $("login-modal").classList.add("hidden");
    } else {
      badge.className = "badge badge-neutral";
      text.textContent = "未连接";
      refreshBtn.disabled = true;
      disconnectBtn.classList.add("hidden");
      $("login-modal").classList.remove("hidden");
    }
  }

  async function api(path, options = {}) {
    const headers = {
      Authorization: "Bearer " + managementKey,
      ...(options.headers || {}),
    };
    if (options.body && !(options.body instanceof FormData)) {
      headers["Content-Type"] = "application/json";
    }

    const res = await fetch("/v8/management/" + path, {
      method: options.method || (options.body ? "POST" : "GET"),
      headers,
      body:
        options.body instanceof FormData
          ? options.body
          : options.body
            ? JSON.stringify(options.body)
            : undefined,
      credentials: "omit",
      cache: "no-store",
      redirect: "error",
    });

    if (res.status === 401) {
      disconnect();
      throw new Error("管理密钥验证失败，请重新输入");
    }

    if (!res.ok) {
      let detail = `HTTP ${res.status}`;
      try {
        const errJson = await res.json();
        detail = errJson.message || errJson.error || detail;
      } catch (_) {}
      throw new Error(detail);
    }

    return res.json();
  }

  function disconnect() {
    generation++;
    managementKey = "";
    cachedCredentials = [];
    compatibilityLoaded = false;
    $("btn-save-compat").disabled = true;
    setConnected(false);
    $("quotas-container").replaceChildren();
    $("cred-list-container").replaceChildren();
    $("quotas-empty").classList.remove("hidden");
  }

  // ── Tab 导航切换 ────────────────────────────────────────────────────────────
  function initTabs() {
    const tabBtns = document.querySelectorAll(".tab-btn");
    tabBtns.forEach((btn) => {
      btn.addEventListener("click", () => {
        tabBtns.forEach((b) => b.classList.remove("active"));
        document
          .querySelectorAll(".tab-content")
          .forEach((c) => c.classList.add("hidden"));
        btn.classList.add("active");
        const targetId = btn.dataset.tab;
        const targetEl = $(targetId);
        if (targetEl) targetEl.classList.remove("hidden");
      });
    });
  }

  // ── Tab 1: 额度监控逻辑 ──────────────────────────────────────────────────────
  async function loadQuotas() {
    if (busy || !managementKey) return;
    busy = true;
    const curGen = generation;
    const loadingEl = $("quota-loading");
    const container = $("quotas-container");
    const emptyEl = $("quotas-empty");

    loadingEl.classList.remove("hidden");
    emptyEl.classList.add("hidden");
    container.replaceChildren();

    try {
      const data = await api("credentials");
      if (curGen !== generation) return;

      const files = (data.files || []).filter(
        (f) =>
          (f.quota_provider || f.provider || f.type) === "factory" &&
          !f.disabled,
      );
      cachedCredentials = data.files || [];
      renderCredentialsList();

      if (!files.length) {
        emptyEl.classList.remove("hidden");
        return;
      }

      for (const file of files) {
        if (curGen !== generation) return;
        const card = el("div", "quota-card");
        const authIndex = String(file.auth_index ?? file.authIndex ?? "");
        const label = file.label || file.name || "Factory Account";

        const top = el("div", "quota-card-top");
        const titleBox = el("div");
        titleBox.append(
          el("div", "account-title", label),
          el("div", "account-plan", `Auth #${authIndex || file.name} · 正在查询配额...`),
        );

        const singleRefreshBtn = el("button", "btn btn-sm", "🔄");
        singleRefreshBtn.title = "刷新该账号";
        singleRefreshBtn.dataset.auth = authIndex;
        singleRefreshBtn.addEventListener("click", () => {
          fetchSingleQuota(file, card, curGen);
        });

        top.append(titleBox, singleRefreshBtn);

        const bucketGroup = el("div", "bucket-group");
        bucketGroup.append(el("div", null, "获取配额中..."));

        card.append(top, bucketGroup);
        container.appendChild(card);

        // 单独查询配额
        fetchSingleQuota(file, card, curGen);
      }
    } catch (err) {
      if (curGen !== generation) return;
      showToast("获取额度列表失败: " + err.message, "error");
    } finally {
      busy = false;
      loadingEl.classList.add("hidden");
    }
  }

  async function fetchSingleQuota(file, cardEl, curGen) {
    const authIndex = String(file.auth_index ?? file.authIndex ?? "");
    const planDescEl = cardEl.querySelector(".account-plan");
    const bucketGroup = cardEl.querySelector(".bucket-group");

    try {
      let quota;
      try {
        quota = await api("plugins/factory/quota", {
          body: { auth_index: authIndex },
        });
      } catch (err) {
        // CPA v8.0.44 移除了 credentials/quota/fetch 路由；旧版 CPA 只有它，按序回退
        if (!/404/.test(err.message)) throw err;
        quota = await api("credentials/quota/fetch", {
          body: { auth_index: authIndex },
        });
      }
      if (curGen !== generation) return;

      const sub = quota.subscription || {};

      bucketGroup.replaceChildren();

      const groups = quota.groups || [];
      if (!groups.length) {
        bucketGroup.append(el("div", null, "未发现配额窗口限制"));
        return;
      }

      function formatWindowName(window) {
        switch (window) {
          case "5h":
            return "5 小时滚动窗口";
          case "weekly":
            return "每周额度 (Weekly)";
          case "monthly":
            return "每月额度 (Monthly)";
          default:
            return window || "配额窗口";
        }
      }

      function formatResetTime(isoStr) {
        if (!isoStr) return "";
        try {
          const d = new Date(isoStr);
          if (Number.isNaN(d.getTime())) return isoStr;
          return d.toLocaleString("zh-CN", {
            hour12: false,
            month: "2-digit",
            day: "2-digit",
            hour: "2-digit",
            minute: "2-digit",
          });
        } catch (_) {
          return isoStr;
        }
      }

      const hasCore = groups.some((g) => (g.displayName || "").toLowerCase().includes("core"));
      const planText = [
        sub.plan,
        sub.tierName,
        hasCore ? "Standard + Droid Core 双额度" : "Standard 单额度",
      ]
        .filter(Boolean)
        .join(" · ");
      if (planDescEl) {
        planDescEl.textContent = `Auth #${authIndex || file.name} · ${planText}`;
      }

      for (const g of groups) {
        const isCore = (g.displayName || "").toLowerCase().includes("core");
        const groupEl = el("div", "quota-tier-section");

        const tierHeader = el("div", "quota-tier-header");
        const tierTitle = el("span", "quota-tier-title", g.displayName || "配额");
        const tierBadge = el(
          "span",
          `badge quota-tier-badge ${isCore ? "badge-core" : "badge-standard"}`,
          isCore ? "Droid Core 独立额度" : "Standard 通用额度",
        );
        tierHeader.append(tierTitle, tierBadge);
        groupEl.appendChild(tierHeader);

        const bucketsList = el("div", "bucket-list");
        for (const bucket of g.buckets || []) {
          const frac = Number(bucket.remainingFraction);
          const valid = Number.isFinite(frac);
          const remaining = valid ? Math.max(0, Math.min(1, frac)) : 0;
          const percent = valid ? (remaining * 100).toFixed(1) : "0.0";
          const tierClass = remaining >= 0.5 ? "high" : remaining >= 0.2 ? "mid" : "low";

          const bucketEl = el("div", "bucket-item");
          const header = el("div", "bucket-header");
          const windowTitle = formatWindowName(bucket.window);
          const statusText = remaining <= 0 ? "已耗尽 (0.0% 剩余)" : `${percent}% 剩余`;
          header.append(
            el("span", "bucket-title", windowTitle),
            el("span", `bucket-percent ${remaining <= 0 ? "exhausted" : ""}`, statusText),
          );

          const track = el("div", "progress-track");
          const fill = el("div", `progress-fill ${tierClass}`);
          fill.style.width = `${percent}%`;
          track.appendChild(fill);

          bucketEl.append(header, track);
          if (bucket.resetTime) {
            const formattedTime = formatResetTime(bucket.resetTime);
            bucketEl.append(el("span", "bucket-reset", `重置时间: ${formattedTime} (${bucket.resetTime})`));
          }
          bucketsList.appendChild(bucketEl);
        }
        groupEl.appendChild(bucketsList);
        bucketGroup.appendChild(groupEl);
      }
    } catch (err) {
      if (curGen !== generation) return;
      bucketGroup.replaceChildren(el("div", null, `查询失败: ${err.message}`));
    }
  }

  // ── Tab 2: 凭证管理逻辑 ──────────────────────────────────────────────────────
  function renderCredentialsList() {
    const listEl = $("cred-list-container");
    if (!listEl) return;

    const factoryFiles = cachedCredentials.filter(
      (f) => (f.quota_provider || f.provider || f.type) === "factory",
    );

    if (!factoryFiles.length) {
      const empty = el("div", "empty-box");
      empty.append(el("p", null, "当前网关尚未配置任何 Factory 凭证"));
      listEl.replaceChildren(empty);
      return;
    }

    listEl.replaceChildren();
    factoryFiles.forEach((f) => {
      const item = el("div", "cred-item");
      const isDisabled = Boolean(f.disabled);
      const label = f.label || f.name || "Factory Account";
      const authIdx = String(f.auth_index ?? f.authIndex ?? "");

      const info = el("div", "cred-info");
      const titleRow = el("div", "cred-label", label + " ");
      const badge = el("span", `badge ${isDisabled ? "badge-neutral" : "badge-success"}`);
      badge.append(el("span", "badge-dot"), el("span", null, isDisabled ? "已停用" : "运行中"));
      titleRow.appendChild(badge);

      const metaRow = el("div", "cred-meta", `${f.name} · Auth Index: ${authIdx || "-"}`);
      info.append(titleRow, metaRow);

      const actions = el("div", "cred-actions");
      const toggleBtn = el("button", "btn btn-sm btn-toggle", isDisabled ? "启用" : "停用");
      toggleBtn.addEventListener("click", () => {
        toggleCredentialStatus(f.name, !isDisabled, authIdx);
      });

      const delBtn = el("button", "btn btn-sm btn-danger btn-del", "删除");
      delBtn.addEventListener("click", () => {
        deleteCredential(f.name);
      });

      actions.append(toggleBtn, delBtn);
      item.append(info, actions);
      listEl.appendChild(item);
    });
  }

  async function toggleCredentialStatus(name, disable, authIndex) {
    try {
      await api("credentials/status", {
        method: "PATCH",
        body: {
          name,
          disabled: disable,
          ...(authIndex ? { auth_index: authIndex } : {}),
        },
      });
      showToast(`凭证已${disable ? "停用" : "启用"}`, "success");
      await loadQuotas();
    } catch (err) {
      showToast("更新凭证状态失败: " + err.message, "error");
    }
  }

  async function deleteCredential(name) {
    if (!confirm(`确定要从网关中彻底删除凭证「${name}」吗？`)) return;
    try {
      await api("credentials", {
        method: "DELETE",
        body: { names: [name] },
      });
      showToast("凭证已删除", "success");
      await loadQuotas();
    } catch (err) {
      showToast("删除凭证失败: " + err.message, "error");
    }
  }

  async function handleAddCredential(e) {
    e.preventDefault();
    const labelInput = $("input-cred-label");
    const keyInput = $("input-cred-key");
    const submitBtn = $("btn-submit-cred");

    const label = labelInput.value.trim();
    const apiKey = keyInput.value.trim();

    if (!label || !apiKey) {
      showToast("请填写完整的凭证别名与 API Key", "error");
      return;
    }

    const safeSlug = label.toLowerCase().replace(/[^a-z0-9_-]/g, "-") || "primary";
    const filename = `factory-${safeSlug}-${Date.now().toString(36)}.json`;
    const payload = JSON.stringify(
      {
        type: "factory",
        label: label,
        api_key: apiKey,
      },
      null,
      2,
    );

    const formData = new FormData();
    const fileBlob = new Blob([payload], { type: "application/json" });
    formData.append("file", fileBlob, filename);

    submitBtn.disabled = true;
    submitBtn.replaceChildren(el("span", null, "⏳ "), el("span", null, "保存中..."));

    try {
      await api("credentials", {
        method: "POST",
        body: formData,
      });
      showToast("Factory 凭证添加成功！", "success");
      labelInput.value = "";
      keyInput.value = "";
      await loadQuotas();
    } catch (err) {
      showToast("添加凭证失败: " + err.message, "error");
    } finally {
      submitBtn.disabled = false;
      submitBtn.replaceChildren(el("span", null, "➕ "), el("span", null, "保存并添加凭证"));
    }
  }

  // ── Tab 3: 兼容策略与敏感词逻辑 ──────────────────────────────────────────
  function renderTags() {
    const box = $("tags-box");
    box.replaceChildren();
    if (!sensitiveWords.length) {
      box.append(el("span", null, "尚未添加任何敏感词"));
      return;
    }
    sensitiveWords.forEach((word, idx) => {
      const pill = el("span", "tag-pill");
      const delBtn = el("button", "tag-remove", "×");
      delBtn.title = "删除";
      delBtn.addEventListener("click", () => {
        sensitiveWords.splice(idx, 1);
        renderTags();
      });
      pill.append(el("span", null, word), delBtn);
      box.appendChild(pill);
    });
  }

  function addWord() {
    const input = $("input-new-word");
    const val = input.value.trim();
    if (!val) return;
    if (!sensitiveWords.includes(val)) {
      sensitiveWords.push(val);
      renderTags();
    }
    input.value = "";
  }

  async function loadCompatibilityConfig() {
    const current = generation;
    compatibilityLoaded = false;
    $("btn-save-compat").disabled = true;
    try {
      const data = await api("config/plugins/configs/factory");
      if (current !== generation) return;
      const compat = data.compatibility || {};
      const mode = compat.mode || "factory";
      if (!["factory", "passthrough"].includes(mode)) {
        throw new Error("服务器返回不支持的兼容模式，请检查配置");
      }
      sensitiveWords = Array.isArray(compat.sensitive_words) ? [...compat.sensitive_words] : [];
      $("select-compat-mode").value = mode;
      $("check-obfuscate-user").checked = Boolean(compat.obfuscate_user_text);
      renderTags();
      compatibilityLoaded = true;
      $("btn-save-compat").disabled = false;
    } catch (err) {
      if (current !== generation) return;
      showToast("读取兼容策略失败，已禁止保存: " + err.message, "error");
    }
  }

  async function saveCompatibilityConfig() {
    if (!managementKey || !compatibilityLoaded) return;
    const current = generation;
    const btn = $("btn-save-compat");
    if (btn.disabled) return;
    btn.disabled = true;
    btn.replaceChildren(el("span", null, "⏳ "), el("span", null, "保存中..."));
    const mode = $("select-compat-mode").value;
    const compat = {
      mode,
      sensitive_words: mode === "passthrough" ? [] : [...sensitiveWords],
      obfuscate_user_text: mode === "passthrough" ? false : $("check-obfuscate-user").checked,
    };
    try {
      if (!["factory", "passthrough"].includes(mode)) throw new Error("不支持的兼容模式");
      // PATCH only the owned settings. Never replace enabled/cache/unknown options.
      await api("config/plugins/configs/factory", {
        method: "PATCH",
        body: { compatibility: compat },
      });
      if (current !== generation) return;
      const stored = await api("config/plugins/configs/factory");
      if (current !== generation) return;
      const actual = stored.compatibility || {};
      if ((actual.mode || "factory") !== compat.mode ||
          JSON.stringify(actual.sensitive_words || []) !== JSON.stringify(compat.sensitive_words) ||
          Boolean(actual.obfuscate_user_text) !== compat.obfuscate_user_text) {
        throw new Error("保存后回读不一致，请重新加载配置");
      }
      sensitiveWords = [...compat.sensitive_words];
      $("check-obfuscate-user").checked = compat.obfuscate_user_text;
      renderTags();
      showToast("兼容性策略已保存并回读确认", "success");
    } catch (err) {
      if (current !== generation) return;
      const message = /read-only|readonly/i.test(err.message)
        ? "CPA 配置为只读，无法保存。请将配置文件挂载设为可写后重新加载。"
        : "保存配置失败: " + err.message;
      showToast(message, "error");
    } finally {
      if (current === generation) {
        btn.disabled = !compatibilityLoaded;
        btn.replaceChildren(el("span", null, "💾 "), el("span", null, "保存策略配置"));
      }
    }
  }

  // ── 初始化与事件绑定 ────────────────────────────────────────────────────────
  function init() {
    initTabs();

    // 绑定按钮
    $("btn-refresh-all").addEventListener("click", () => loadQuotas());
    $("btn-disconnect").addEventListener("click", () => disconnect());
    $("form-add-cred").addEventListener("submit", handleAddCredential);
    $("btn-add-word").addEventListener("click", addWord);
    $("input-new-word").addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        addWord();
      }
    });
    $("btn-reset-words").addEventListener("click", () => {
      sensitiveWords = ["Hermes", "Nous", "pi"];
      renderTags();
      showToast("已重置为推荐敏感词", "info");
    });
    $("btn-save-compat").addEventListener("click", saveCompatibilityConfig);

    // 登录弹窗表单提交
    $("form-login").addEventListener("submit", (e) => {
      e.preventDefault();
      const input = $("input-mgmt-key");
      const val = input.value.trim();
      if (!val) return;
      managementKey = val;
      input.value = "";
      setConnected(true);
      loadQuotas();
      loadCompatibilityConfig();
    });

    // 尝试通过 panel-auth 自动无缝认证
    tryAutoAuth();
  }

  function tryAutoAuth() {
    try {
      if (globalThis.FactoryPanelAuth && typeof globalThis.FactoryPanelAuth.read === "function") {
        const auth = globalThis.FactoryPanelAuth.read(window);
        if (auth && auth.key) {
          managementKey = auth.key;
          setConnected(true);
          loadQuotas();
          loadCompatibilityConfig();
          return;
        }
      }
    } catch (_) {}
    setConnected(false);
  }

  window.addEventListener("DOMContentLoaded", init);
})();
