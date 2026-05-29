"use strict";

let state = {
  campaigns: [],
  currentCampaignId: null,
  currentCampaign: null,
  pollTimer: null,
};

// ---------- Chart helpers ----------
const charts = {};
const COLOR_ORGANIC = "#00d4aa";
const COLOR_BOT = "#ff6b9d";
const COLOR_UNKNOWN = "#5a6b8c";
const COLOR_ACCENT = "#4facfe";

function destroyChart(id) {
  if (charts[id]) {
    charts[id].destroy();
    delete charts[id];
  }
}

function makeChart(id, config) {
  const el = document.getElementById(id);
  if (!el || typeof Chart === "undefined") return;
  destroyChart(id);
  Chart.defaults.color = "#9fb0cc";
  Chart.defaults.font.family = "inherit";
  charts[id] = new Chart(el.getContext("2d"), config);
}

function truncateLabel(s, n) {
  s = String(s || "");
  return s.length > n ? s.slice(0, n - 1) + "…" : s;
}

document.addEventListener("DOMContentLoaded", () => {
  loadCampaigns();
});

// ---------- API helpers ----------
async function api(path, options) {
  const resp = await fetch(path, options);
  const text = await resp.text();
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch (_) {
    data = text;
  }
  if (!resp.ok) {
    const msg = (data && data.error) || `${resp.status} ${resp.statusText}`;
    throw new Error(msg);
  }
  return data;
}

function csv(value) {
  return (value || "")
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
}

function toast(msg, isError) {
  const el = document.getElementById("toast");
  el.textContent = msg;
  el.classList.remove("hidden");
  el.classList.toggle("toast-error", !!isError);
  setTimeout(() => el.classList.add("hidden"), 3500);
}

function showView(id) {
  document.querySelectorAll(".view").forEach((v) => v.classList.add("hidden"));
  document.getElementById(id).classList.remove("hidden");
}

function stopPolling() {
  if (state.pollTimer) {
    clearInterval(state.pollTimer);
    state.pollTimer = null;
  }
}

// ---------- Campaigns list ----------
async function loadCampaigns() {
  try {
    const data = await api("/v1/campaigns");
    state.campaigns = (data && data.campaigns) || [];
  } catch (e) {
    state.campaigns = [];
    toast("Could not load campaigns: " + e.message, true);
  }
  renderSwitcher();
  renderCampaigns();
}

function renderSwitcher() {
  const sel = document.getElementById("campaignSwitcher");
  if (!state.campaigns.length) {
    sel.innerHTML = `<option value="">No campaigns</option>`;
    sel.classList.add("hidden");
    return;
  }
  sel.classList.remove("hidden");
  sel.innerHTML =
    `<option value="">Switch campaign…</option>` +
    state.campaigns
      .map(
        (c) =>
          `<option value="${c.campaign_id}" ${
            c.campaign_id === state.currentCampaignId ? "selected" : ""
          }>${escapeHtml(c.client_name)}</option>`
      )
      .join("");
}

function onSwitchCampaign() {
  const id = document.getElementById("campaignSwitcher").value;
  if (id) openCampaign(id);
}

function renderCampaigns() {
  const list = document.getElementById("campaignsList");
  const empty = document.getElementById("campaignsEmpty");
  if (!state.campaigns.length) {
    list.innerHTML = "";
    empty.classList.remove("hidden");
    return;
  }
  empty.classList.add("hidden");
  list.innerHTML = state.campaigns
    .map((c) => {
      const topics = (c.monitored_topics || []).slice(0, 4);
      return `
      <div class="campaign-card" onclick="openCampaign('${c.campaign_id}')">
        <div class="campaign-card-head">
          <h3>${escapeHtml(c.client_name)}</h3>
          <span class="status-pill status-${c.status}">${c.status}</span>
        </div>
        <p class="campaign-card-meta">${escapeHtml(c.region || "Global")} · ${
        (c.languages || ["en"]).join(", ")
      }</p>
        <div class="chips">${topics
          .map((t) => `<span class="chip">${escapeHtml(t)}</span>`)
          .join("")}</div>
        <div class="campaign-card-foot">Open dashboard &rarr;</div>
      </div>`;
    })
    .join("");
}

function goCampaigns() {
  stopPolling();
  showView("campaignsView");
  loadCampaigns();
}

// ---------- New campaign ----------
function openNewCampaign() {
  showView("newCampaignView");
  document.getElementById("formError").classList.add("hidden");
}

async function submitCampaign(ev) {
  ev.preventDefault();
  const btn = document.getElementById("createBtn");
  const errEl = document.getElementById("formError");
  errEl.classList.add("hidden");

  const keywords = csv(document.getElementById("f_keywords").value);
  const hashtags = csv(document.getElementById("f_hashtags").value);
  const interestGroups = [];
  if (keywords.length || hashtags.length) {
    interestGroups.push({
      name: "Primary tracking",
      keywords,
      hashtags,
      priority: 1,
    });
  }

  const payload = {
    client_name: document.getElementById("f_client_name").value.trim(),
    client_aliases: csv(document.getElementById("f_aliases").value),
    region: document.getElementById("f_region").value.trim(),
    monitored_topics: csv(document.getElementById("f_topics").value),
    opponents: csv(document.getElementById("f_opponents").value),
    client_accounts: csv(document.getElementById("f_client_accounts").value),
    interest_groups: interestGroups,
    languages: ["en"],
    crawl_budget: {
      top_narratives: parseInt(document.getElementById("f_top").value, 10) || 5,
      interactions_per_narrative:
        parseInt(document.getElementById("f_interactions").value, 10) || 40,
      max_collection_results:
        parseInt(document.getElementById("f_maxresults").value, 10) || 60,
    },
  };

  btn.disabled = true;
  btn.textContent = "Creating…";
  try {
    const campaign = await api("/v1/campaigns", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    if (document.getElementById("f_runnow").checked) {
      await api(`/v1/campaigns/${campaign.campaign_id}/crawl/run-once`, {
        method: "POST",
      });
    }
    toast("Campaign created. Gathering started.");
    await loadCampaigns();
    openCampaign(campaign.campaign_id);
  } catch (e) {
    errEl.textContent = e.message;
    errEl.classList.remove("hidden");
  } finally {
    btn.disabled = false;
    btn.textContent = "Create & gather";
  }
}

// ---------- Dashboard ----------
async function openCampaign(id) {
  state.currentCampaignId = id;
  state.currentCampaign = state.campaigns.find((c) => c.campaign_id === id) || null;
  showView("dashboardView");
  renderSwitcher();
  document.getElementById("dashClient").textContent = state.currentCampaign
    ? state.currentCampaign.client_name
    : "Campaign";
  document.getElementById("narrativeCards").innerHTML = "";
  document.getElementById("dashSummary").textContent = "Loading…";
  await refreshDashboard();
  // Poll while a crawl is running.
  stopPolling();
  state.pollTimer = setInterval(refreshDashboard, 6000);
}

async function refreshDashboard() {
  const id = state.currentCampaignId;
  if (!id) return;
  try {
    const [snapshot, status] = await Promise.all([
      api(`/v1/campaigns/${id}/dashboard`),
      api(`/v1/campaigns/${id}/crawl/status`).catch(() => null),
    ]);
    renderDashboard(snapshot, status);
  } catch (e) {
    toast("Dashboard error: " + e.message, true);
  }
}

function renderDashboard(snapshot, status) {
  const statusEl = document.getElementById("dashStatus");
  const effStatus = (status && status.status) || snapshot.status || "unknown";
  statusEl.textContent = effStatus;
  statusEl.className = "status-pill status-" + effStatus;
  const stopBtn = document.getElementById("stopBtn");

  if (effStatus === "running") {
    document.getElementById("runBtn").disabled = true;
    document.getElementById("runBtn").textContent = "Gathering…";
    stopBtn.disabled = false;
    stopBtn.textContent = "Stop campaign";
  } else if (effStatus === "stopped") {
    document.getElementById("runBtn").disabled = false;
    document.getElementById("runBtn").textContent = "Gather now";
    stopBtn.disabled = true;
    stopBtn.textContent = "Stopped";
    stopPolling();
  } else {
    document.getElementById("runBtn").disabled = false;
    document.getElementById("runBtn").textContent = "Gather now";
    stopBtn.disabled = false;
    stopBtn.textContent = "Stop campaign";
    if (effStatus !== "running") stopPolling();
  }

  document.getElementById("dashSummary").textContent =
    snapshot.executive_summary || "";

  const cards = snapshot.narratives || [];
  // Dashboard stats
  const totalInteractions = cards.reduce(
    (a, c) => a + (c.total_interactions || 0),
    0
  );
  const avgInauth =
    cards.length > 0
      ? cards.reduce((a, c) => a + (c.inauthentic_percentage || 0), 0) /
        cards.length
      : 0;
  document.getElementById("dashStats").innerHTML = `
    ${statCard("Narratives", cards.length)}
    ${statCard("Total interactions", totalInteractions.toLocaleString())}
    ${statCard("Avg bot/AI-driven", avgInauth.toFixed(0) + "%", avgInauth >= 40 ? "bad" : "ok")}
    ${statCard(
      "Sources",
      (status && status.sources_collected) || (snapshot.source_counts && snapshot.source_counts.narratives) || 0
    )}
  `;

  const container = document.getElementById("narrativeCards");
  const empty = document.getElementById("dashEmpty");
  if (!cards.length) {
    container.innerHTML = "";
    empty.classList.remove("hidden");
    document.getElementById("dashEmptyMsg").textContent =
      effStatus === "running"
        ? "Gathering narratives in the background… this refreshes automatically."
        : "No narratives collected yet. Click \u201CGather now\u201D to run a crawl.";
    return;
  }
  empty.classList.add("hidden");
  container.innerHTML = cards.map((c, i) => narrativeCardHtml(c, i)).join("");
  renderDashboardCharts(cards);
}

function renderDashboardCharts(cards) {
  const wrap = document.getElementById("dashCharts");
  if (!cards.length) {
    wrap.classList.add("hidden");
    destroyChart("chartBot");
    destroyChart("chartReach");
    return;
  }
  wrap.classList.remove("hidden");
  const labels = cards.map((c) => truncateLabel(c.narrative || "Narrative", 26));

  makeChart("chartBot", {
    type: "bar",
    data: {
      labels,
      datasets: [
        {
          label: "Bot/AI-driven %",
          data: cards.map((c) => Math.round(c.inauthentic_percentage || 0)),
          backgroundColor: COLOR_BOT,
        },
        {
          label: "Organic %",
          data: cards.map((c) => Math.round(c.authentic_percentage || 0)),
          backgroundColor: COLOR_ORGANIC,
        },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      indexAxis: "y",
      scales: { x: { stacked: true, max: 100 }, y: { stacked: true } },
      plugins: { legend: { position: "bottom" } },
    },
  });

  makeChart("chartReach", {
    type: "bar",
    data: {
      labels,
      datasets: [
        {
          label: "Estimated reach",
          data: cards.map((c) => c.reach_estimate || 0),
          backgroundColor: cards.map((c) =>
            (c.inauthentic_percentage || 0) >= 40 ? COLOR_BOT : COLOR_ACCENT
          ),
        },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      scales: { y: { beginAtZero: true } },
      plugins: {
        legend: { display: false },
        tooltip: {
          callbacks: {
            afterLabel: (ctx) =>
              `Bot/AI-driven: ${Math.round(
                cards[ctx.dataIndex].inauthentic_percentage || 0
              )}%`,
          },
        },
      },
    },
  });
}

function statCard(label, value, tone) {
  return `<div class="stat ${tone || ""}"><div class="stat-value">${value}</div><div class="stat-label">${label}</div></div>`;
}

function narrativeCardHtml(c, idx) {
  const auth = c.authentic_percentage || 0;
  const inauth = c.inauthentic_percentage || 0;
  const unknown = c.unknown_percentage || 0;
  const trendIcon =
    c.trend === "rising" ? "↑" : c.trend === "falling" ? "↓" : "→";
  const impact = c.impact_summary || c.why_it_matters || "";
  const loss = c.capital_loss_estimate || {};
  const committee = c.committee_verdict || {};
  const llmTag =
    committee.source === "gemini"
      ? `<span class="ai-chip" title="${escapeAttr(
          committee.consensus_label || "AI committee"
        )}">AI committee</span>`
      : "";
  const lossChip =
    loss.applies && loss.expected_usd
      ? `<span class="loss-chip" title="${escapeAttr(
          loss.explanation || ""
        )}">⚠ ~${formatUSD(loss.expected_usd)} at risk</span>`
      : "";
  const relevance = Math.round((c.relevance_score || 0) * 100);
  return `
  <div class="narrative-card" onclick="openNarrative('${c.narrative_id}')">
    <div class="nc-rank">#${c.popularity_rank || idx + 1}</div>
    <div class="nc-main">
      <div class="nc-title-row">
        <h4>${escapeHtml(c.narrative || "Narrative")}</h4>
        <span class="trend trend-${c.trend || "flat"}">${trendIcon} ${c.trend || "flat"}</span>
      </div>
      <div class="nc-chips">
        ${llmTag}
        ${relevance ? `<span class="rel-chip" title="Relevance to your campaign">${relevance}% relevant</span>` : ""}
        ${lossChip}
      </div>
      ${impact ? `<p class="nc-impact">${escapeHtml(impact)}</p>` : ""}
      <p class="nc-summary">${escapeHtml(c.summary || "")}</p>
      ${authenticityBar(auth, inauth, unknown)}
      <div class="nc-stats">
        <span title="Total classified interactions">💬 ${(c.total_interactions || 0).toLocaleString()} interactions</span>
        <span title="Estimated reach">📡 ${formatReach(c.reach_estimate)}</span>
        <span title="Bot/AI-driven share">🤖 ${inauth.toFixed(0)}% bot/AI-driven</span>
        <span title="Organic share">🌱 ${auth.toFixed(0)}% organic</span>
      </div>
    </div>
  </div>`;
}

function formatUSD(n) {
  n = n || 0;
  if (n >= 1e9) return "$" + (n / 1e9).toFixed(1) + "B";
  if (n >= 1e6) return "$" + (n / 1e6).toFixed(1) + "M";
  if (n >= 1e3) return "$" + (n / 1e3).toFixed(0) + "K";
  return "$" + n;
}

function authenticityBar(auth, inauth, unknown) {
  return `
  <div class="auth-bar" title="Organic ${auth.toFixed(0)}% · Bot/AI ${inauth.toFixed(
    0
  )}% · Unknown ${unknown.toFixed(0)}%">
    <div class="auth-seg auth-organic" style="width:${auth}%"></div>
    <div class="auth-seg auth-bot" style="width:${inauth}%"></div>
    <div class="auth-seg auth-unknown" style="width:${unknown}%"></div>
  </div>`;
}

function formatReach(n) {
  n = n || 0;
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + "K";
  return String(n);
}

async function runCrawl() {
  const id = state.currentCampaignId;
  if (!id) return;
  try {
    await api(`/v1/campaigns/${id}/crawl/run-once`, { method: "POST" });
    toast("Gathering started in the background.");
    stopPolling();
    state.pollTimer = setInterval(refreshDashboard, 6000);
    refreshDashboard();
  } catch (e) {
    toast("Could not start crawl: " + e.message, true);
  }
}

async function stopCampaign() {
  const id = state.currentCampaignId;
  if (!id) return;
  const btn = document.getElementById("stopBtn");
  btn.disabled = true;
  btn.textContent = "Stopping…";
  try {
    await api(`/v1/campaigns/${id}/crawl/stop`, { method: "POST" });
    toast("Campaign stopped. Scheduled/background crawls will no longer run.");
    stopPolling();
    await loadCampaigns();
    await refreshDashboard();
  } catch (e) {
    toast("Could not stop campaign: " + e.message, true);
    btn.disabled = false;
    btn.textContent = "Stop campaign";
  }
}

// ---------- Narrative detail ----------
async function openNarrative(id) {
  stopPolling();
  showView("narrativeView");
  const el = document.getElementById("narrativeDetail");
  el.innerHTML = `<div class="loading-inline">Loading narrative…</div>`;
  try {
    const [detail, interactionsResp, actorsResp] = await Promise.all([
      api(`/v1/narratives/${id}`),
      api(`/v1/narratives/${id}/interactions?limit=500`).catch(() => ({ interactions: [] })),
      api(`/v1/narratives/${id}/actors?limit=500`).catch(() => ({ actors: [] })),
    ]);
    renderNarrative(detail, interactionsResp.interactions || [], actorsResp.actors || []);
  } catch (e) {
    el.innerHTML = `<div class="form-error">Could not load narrative: ${escapeHtml(
      e.message
    )}</div>`;
  }
}

function backToDashboard() {
  showView("dashboardView");
  refreshDashboard();
}

function renderNarrative(detail, interactions, actors) {
  const n = detail.narrative || {};
  const sources = detail.sources || [];
  const auth = n.authentic_percentage || 0;
  const inauth = n.inauthentic_percentage || 0;
  const unknown = n.unknown_percentage || 0;

  // Interaction-type breakdown
  const typeCounts = { reply: 0, quote: 0, repost: 0, subtweet: 0, post: 0 };
  interactions.forEach((it) => {
    const t = it.interaction_type || "post";
    typeCounts[t] = (typeCounts[t] || 0) + 1;
  });

  const primary = n.primary_source_attribution;
  const xSources = sources.filter((s) => s.source_type === "x_post");
  const webSources = sources.filter((s) => s.source_type !== "x_post");

  const el = document.getElementById("narrativeDetail");
  el.innerHTML = `
    <div class="nd-head">
      <h2>${escapeHtml(n.narrative || "Narrative")}</h2>
      <span class="risk-badge risk-${(n.risk_label || "low").toLowerCase()}">${
    n.risk_label || "LOW"
  } risk</span>
    </div>
    <p class="nd-summary">${escapeHtml(n.summary || "")}</p>

    ${committeePanelHtml(n)}

    <div class="card">
      <h3>Narrative spread over time</h3>
      <canvas id="chartTimeline" height="200"></canvas>
      <p class="muted">Posts and interactions over time, split by organic vs bot/AI-driven accounts.</p>
    </div>

    <div class="nd-grid">
      <div class="card">
        <h3>Bot/AI vs organic engagement</h3>
        ${authenticityBar(auth, inauth, unknown)}
        <div class="legend">
          <span><i class="dot organic"></i> Organic ${auth.toFixed(0)}%</span>
          <span><i class="dot bot"></i> Bot/AI-driven ${inauth.toFixed(0)}%</span>
          <span><i class="dot unknown"></i> Unknown ${unknown.toFixed(0)}%</span>
        </div>
        <p class="muted">${escapeHtml(impactSummaryText(n))}</p>
      </div>

      <div class="card">
        <h3>Interaction breakdown</h3>
        <canvas id="chartInteractions" height="160"></canvas>
        <div class="kv-list">
          ${interactionRow("💬 Replies / comments", typeCounts.reply)}
          ${interactionRow("🔁 Reposts / retweets", typeCounts.repost)}
          ${interactionRow("❝ Quotes", typeCounts.quote)}
          ${interactionRow("🫥 Subtweets (indirect)", typeCounts.subtweet)}
          ${interactionRow("📝 Other posts", typeCounts.post)}
        </div>
        <p class="muted">${interactions.length.toLocaleString()} classified interactions</p>
      </div>
    </div>

    <div class="card">
      <h3>Origin / primary source</h3>
      ${
        primary
          ? `<div class="origin">
              <span class="badge badge-${(primary.source_type || "Unknown").toLowerCase()}">${
              primary.source_type || "Unknown"
            }</span>
              <span class="muted">Confidence ${(
                (primary.confidence || 0) * 100
              ).toFixed(0)}%${
              primary.first_seen_at
                ? " · first seen " + formatDate(primary.first_seen_at)
                : ""
            }</span>
              <div class="evidence">${(primary.evidence || [])
                .map((e) => `<div>• ${escapeHtml(e)}</div>`)
                .join("")}</div>
            </div>`
          : `<p class="muted">No primary source attribution available.</p>`
      }
    </div>

    <div class="card">
      <h3>X / Twitter sources <span class="card-badge">${xSources.length}</span></h3>
      <div class="source-list">
        ${xSources.map(sourceRow).join("") || `<p class="muted">No X posts collected.</p>`}
      </div>
    </div>

    <div class="card">
      <h3>Accounts interacting <span class="card-badge">${actors.length}</span></h3>
      <div class="actor-list">
        ${
          actors.map(actorRow).join("") ||
          `<p class="muted">No actor classifications yet.</p>`
        }
      </div>
    </div>

    ${
      webSources.length
        ? `<div class="card">
            <h3>Supporting web sources <span class="card-badge">${webSources.length}</span></h3>
            <div class="source-list">${webSources.map(sourceRow).join("")}</div>
          </div>`
        : ""
    }
  `;

  initNarrativeCharts(n, typeCounts);
}

function impactSummaryText(n) {
  const v = n.committee_verdict || {};
  return v.impact_summary || n.why_it_matters || "";
}

function committeePanelHtml(n) {
  const v = n.committee_verdict;
  const loss = (v && v.capital_loss) || n.capital_loss_estimate || {};
  const isLive = v && v.source === "gemini";
  const expertsHtml =
    v && (v.experts || []).length
      ? v.experts
          .map(
            (e) => `
        <div class="expert">
          <div class="expert-head">
            <strong>${escapeHtml(e.expert || "Expert")}</strong>
            <span class="sev sev-${e.severity >= 0.66 ? "high" : e.severity >= 0.33 ? "mid" : "low"}">severity ${Math.round(
              (e.severity || 0) * 100
            )}%</span>
          </div>
          <p>${escapeHtml(e.opinion || "")}</p>
        </div>`
          )
          .join("")
      : `<p class="muted">No expert committee output (LLM unavailable — deterministic scoring used).</p>`;

  const lossBlock =
    loss && loss.applies && (loss.expected_usd || loss.max_usd)
      ? `<div class="loss-box">
          <div class="loss-head">Estimated capital at risk</div>
          <div class="loss-range">
            <span class="loss-lo">${formatUSD(loss.min_usd)}</span>
            <span class="loss-exp">${formatUSD(loss.expected_usd)}</span>
            <span class="loss-hi">${formatUSD(loss.max_usd)}</span>
          </div>
          <div class="loss-labels"><span>low</span><span>likely</span><span>high</span></div>
          <p class="muted">${escapeHtml(loss.explanation || "")}</p>
          <p class="loss-conf">Confidence ${Math.round((loss.confidence || 0) * 100)}% · ${escapeHtml(
          loss.disclaimer || "Directional estimate."
        )}</p>
        </div>`
      : `<p class="muted">No material capital loss estimated for this narrative.</p>`;

  return `
  <div class="card committee-card">
    <div class="committee-head">
      <h3>Expert committee assessment</h3>
      <span class="${isLive ? "ai-chip" : "heur-chip"}">${
    isLive ? "Live AI committee" : "Heuristic fallback"
  }</span>
    </div>
    ${
      v
        ? `<div class="committee-grid">
            <div>
              ${v.consensus_label ? `<p><span class="muted">Consensus:</span> <strong>${escapeHtml(v.consensus_label)}</strong></p>` : ""}
              ${v.audience_effect ? `<p><span class="muted">Audience effect:</span> ${escapeHtml(v.audience_effect)}</p>` : ""}
              ${v.recommended_action ? `<p class="rec-action"><span class="muted">Recommended action:</span> ${escapeHtml(v.recommended_action)}</p>` : ""}
              ${v.origin_rationale ? `<p class="muted origin-note">Origin check: ${escapeHtml(v.origin_rationale)}</p>` : ""}
            </div>
            <div>${lossBlock}</div>
          </div>
          <div class="experts">${expertsHtml}</div>`
        : `<div class="committee-grid"><div><p class="muted">Awaiting committee assessment.</p></div><div>${lossBlock}</div></div>`
    }
  </div>`;
}

function initNarrativeCharts(n, typeCounts) {
  // Spread-over-time line: organic vs bot/AI vs unknown.
  const tl = n.spread_timeline || [];
  if (tl.length) {
    const labels = tl.map((b) => formatTimeShort(b.t));
    makeChart("chartTimeline", {
      type: "line",
      data: {
        labels,
        datasets: [
          {
            label: "Organic",
            data: tl.map((b) => b.authentic || 0),
            borderColor: COLOR_ORGANIC,
            backgroundColor: COLOR_ORGANIC + "33",
            fill: true,
            tension: 0.3,
          },
          {
            label: "Bot/AI-driven",
            data: tl.map((b) => b.inauthentic || 0),
            borderColor: COLOR_BOT,
            backgroundColor: COLOR_BOT + "33",
            fill: true,
            tension: 0.3,
          },
          {
            label: "Unknown",
            data: tl.map((b) => b.unknown || 0),
            borderColor: COLOR_UNKNOWN,
            backgroundColor: COLOR_UNKNOWN + "22",
            fill: true,
            tension: 0.3,
          },
        ],
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        interaction: { mode: "index", intersect: false },
        scales: { y: { beginAtZero: true, stacked: true }, x: { stacked: true } },
        plugins: { legend: { position: "bottom" } },
      },
    });
  } else {
    destroyChart("chartTimeline");
  }

  // Interaction breakdown doughnut.
  const breakdown = n.interaction_breakdown || typeCounts;
  const order = ["reply", "repost", "quote", "subtweet", "post"];
  const labelMap = {
    reply: "Replies",
    repost: "Reposts",
    quote: "Quotes",
    subtweet: "Subtweets",
    post: "Other posts",
  };
  const present = order.filter((k) => (breakdown[k] || 0) > 0);
  if (present.length) {
    makeChart("chartInteractions", {
      type: "doughnut",
      data: {
        labels: present.map((k) => labelMap[k]),
        datasets: [
          {
            data: present.map((k) => breakdown[k] || 0),
            backgroundColor: [
              COLOR_ACCENT,
              COLOR_ORGANIC,
              "#ffa502",
              COLOR_BOT,
              COLOR_UNKNOWN,
            ],
          },
        ],
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        plugins: { legend: { position: "right" } },
      },
    });
  } else {
    destroyChart("chartInteractions");
  }
}

function formatTimeShort(s) {
  try {
    const d = new Date(s);
    return d.toLocaleString([], {
      month: "short",
      day: "numeric",
      hour: "2-digit",
    });
  } catch (_) {
    return s;
  }
}

function interactionRow(label, count) {
  return `<div class="kv"><span>${label}</span><strong>${(count || 0).toLocaleString()}</strong></div>`;
}

function sourceRow(s) {
  const a = s.author || {};
  const url = s.url || s.canonical_url || "";
  const eng = s.engagement || {};
  const botPct = Math.round((a.bot_likelihood || 0) * 100);
  return `
  <div class="source-row">
    <div class="source-author">
      <div class="avatar">${escapeHtml((a.handle || a.display_name || "?").slice(0, 1).toUpperCase())}</div>
      <div>
        <div class="author-name">
          ${escapeHtml(a.display_name || a.handle || "Unknown")}
          ${a.verified ? '<span class="verified" title="Verified">✓</span>' : ""}
        </div>
        <div class="author-handle">${a.handle ? "@" + escapeHtml(a.handle) : ""} · ${formatReach(
    a.followers_count
  )} followers</div>
      </div>
      <span class="bot-tag ${botPct >= 65 ? "bot" : "human"}">${
    botPct >= 65 ? "Bot-like" : "Likely human"
  } ${botPct}%</span>
    </div>
    <p class="source-text">${escapeHtml(s.text || s.snippet || "")}</p>
    <div class="source-meta">
      ${url ? `<a href="${escapeAttr(url)}" target="_blank" rel="noopener">View on X ↗</a>` : ""}
      <span>❤ ${num(eng.likes)}</span>
      <span>🔁 ${num(eng.reposts)}</span>
      <span>💬 ${num(eng.replies)}</span>
      <span>❝ ${num(eng.quotes)}</span>
      ${s.published_at ? `<span>${formatDate(s.published_at)}</span>` : ""}
    </div>
  </div>`;
}

function actorRow(a) {
  const pct = Math.round((a.bot_score || 0) * 100);
  return `
  <div class="actor-row">
    <span class="actor-id">${escapeHtml(a.account_id || "unknown")}</span>
    <span class="bot-tag ${a.class === "Bot" ? "bot" : "human"}">${a.class || "Unknown"} ${pct}%</span>
    <span class="actor-evidence muted">${escapeHtml((a.evidence || []).slice(0, 2).join("; "))}</span>
  </div>`;
}

// ---------- utils ----------
function num(v) {
  if (v === undefined || v === null) return 0;
  const n = typeof v === "number" ? v : parseInt(v, 10);
  return isNaN(n) ? 0 : n.toLocaleString();
}

function formatDate(s) {
  try {
    return new Date(s).toLocaleString();
  } catch (_) {
    return s;
  }
}

function escapeHtml(s) {
  return String(s == null ? "" : s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

function escapeAttr(s) {
  return escapeHtml(s).replace(/"/g, "&quot;");
}
