import { useCallback, useEffect, useMemo, useState } from "react";
import { Bar, Doughnut, Line } from "react-chartjs-2";
import {
  CategoryScale,
  Chart as ChartJS,
  Filler,
  Legend,
  LinearScale,
  ArcElement,
  PointElement,
  LineElement,
  BarElement,
  Title,
  Tooltip,
} from "chart.js";

ChartJS.register(
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  BarElement,
  ArcElement,
  Title,
  Tooltip,
  Legend,
  Filler,
);

ChartJS.defaults.color = "#8a96b0";
ChartJS.defaults.borderColor = "rgba(38, 49, 73, 0.7)";
ChartJS.defaults.font.family = "Inter, 'Segoe UI', Roboto, sans-serif";

const COLORS = {
  organic: "#2ecc8f",
  bot: "#ff5c6c",
  unknown: "#46506b",
  accent: "#4facfe",
  orange: "#ffa502",
  violet: "#9d7bff",
  teal: "#00d4aa",
};

function toCSV(value) {
  return String(value || "")
    .split(",")
    .map((v) => v.trim())
    .filter(Boolean);
}

async function api(path, options) {
  const response = await fetch(path, options);
  const text = await response.text();
  let data;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = text;
  }
  if (!response.ok) {
    throw new Error((data && data.error) || `${response.status} ${response.statusText}`);
  }
  return data;
}

function statusClass(status) {
  return `status-pill status-${String(status || "unknown").toLowerCase().replace(/\s+/g, "_")}`;
}

function num(value) {
  const n = Number(value || 0);
  return Number.isFinite(n) ? n.toLocaleString() : "0";
}

function pct(value) {
  const n = Number(value || 0);
  return `${n.toFixed(0)}%`;
}

function shortReach(value) {
  const n = Number(value || 0);
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}K`;
  return `${n}`;
}

function formatUSD(n) {
  const value = Number(n || 0);
  if (value >= 1e9) return `$${(value / 1e9).toFixed(1)}B`;
  if (value >= 1e6) return `$${(value / 1e6).toFixed(1)}M`;
  if (value >= 1e3) return `$${(value / 1e3).toFixed(0)}K`;
  return `$${value.toFixed(0)}`;
}

function dateText(value) {
  if (!value) return "";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return String(value);
  return d.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

// Backend-generated strings often embed raw figures (e.g. "estimated reach 57143725" or "$2705K").
// Compact them so copy stays readable for a non-technical PR reader.
function humanize(text) {
  if (text == null) return "";
  let out = String(text);
  out = out.replace(/\$\s?(\d+(?:\.\d+)?)\s?M\b/gi, (_, n) => formatUSD(Number(n) * 1e6));
  out = out.replace(/\$\s?(\d+(?:\.\d+)?)\s?K\b/gi, (_, n) => formatUSD(Number(n) * 1e3));
  out = out.replace(/\b\d{5,}\b/g, (m) => shortReach(Number(m)));
  return out;
}

function capitalize(value) {
  const t = String(value || "").trim();
  if (!t) return t;
  return t.charAt(0).toUpperCase() + t.slice(1);
}

function prettyRole(value) {
  return String(value || "").replace(/_/g, " ").trim();
}

function riskClass(label) {
  return `risk-badge risk-${String(label || "low").toLowerCase()}`;
}

function trendMeta(trend) {
  if (trend === "rising") return { icon: "▲", text: "Rising", cls: "trend-rising" };
  if (trend === "falling") return { icon: "▼", text: "Cooling", cls: "trend-falling" };
  return { icon: "→", text: "Steady", cls: "trend-flat" };
}

// ---------- Small presentational components ----------

function AuthenticityBar({ auth, inauth, unknown }) {
  return (
    <div className="auth-bar" title={`Organic ${pct(auth)} · Bot/AI ${pct(inauth)} · Unknown ${pct(unknown)}`}>
      <div className="auth-seg auth-organic" style={{ width: `${auth}%` }} />
      <div className="auth-seg auth-bot" style={{ width: `${inauth}%` }} />
      <div className="auth-seg auth-unknown" style={{ width: `${unknown}%` }} />
    </div>
  );
}

function Avatar({ name }) {
  const letter = String(name || "?").trim().slice(0, 1).toUpperCase() || "?";
  return <div className="avatar">{letter}</div>;
}

function CapitalLossBox({ loss }) {
  if (!loss || !loss.applies || !(loss.expected_usd || loss.max_usd)) {
    return (
      <div className="loss-box calm">
        <div className="loss-head">Estimated capital at risk</div>
        <p className="loss-calm-text">
          No material financial impact is expected from this narrative. Monitor, but it likely does not warrant a
          dedicated response.
        </p>
      </div>
    );
  }
  return (
    <div className="loss-box">
      <div className="loss-head">Estimated capital at risk</div>
      <div className="loss-range">
        <span className="loss-lo">{formatUSD(loss.min_usd)}</span>
        <span className="loss-exp">{formatUSD(loss.expected_usd)}</span>
        <span className="loss-hi">{formatUSD(loss.max_usd)}</span>
      </div>
      <div className="loss-track" />
      <div className="loss-labels">
        <span>low</span>
        <span>likely</span>
        <span>high</span>
      </div>
      {loss.explanation && <p className="loss-note">{humanize(loss.explanation)}</p>}
      <p className="loss-note">
        Confidence {pct((loss.confidence || 0) * 100)} · {humanize(loss.disclaimer) || "Directional estimate."}
      </p>
    </div>
  );
}

function App() {
  const [view, setView] = useState("campaigns");
  const [campaigns, setCampaigns] = useState([]);
  const [currentCampaignId, setCurrentCampaignId] = useState("");
  const [currentCampaign, setCurrentCampaign] = useState(null);
  const [snapshot, setSnapshot] = useState(null);
  const [crawlStatus, setCrawlStatus] = useState(null);
  const [narrativeDetail, setNarrativeDetail] = useState(null);
  const [narrativeSources, setNarrativeSources] = useState([]);
  const [narrativeActors, setNarrativeActors] = useState([]);
  const [loading, setLoading] = useState(false);
  const [toast, setToast] = useState(null);
  const [formError, setFormError] = useState("");

  const [form, setForm] = useState({
    client_name: "",
    aliases: "",
    region: "",
    topics: "",
    opponents: "",
    client_accounts: "",
    keywords: "",
    hashtags: "",
    top: "5",
    interactions: "40",
    maxresults: "120",
    runnow: true,
  });

  const effectiveStatus = crawlStatus?.status || snapshot?.status || currentCampaign?.status || "unknown";
  const cards = useMemo(() => snapshot?.narratives || [], [snapshot]);

  const showToast = useCallback((message, isError = false) => {
    setToast({ message, isError });
    window.setTimeout(() => setToast(null), 3500);
  }, []);

  const loadCampaigns = useCallback(async () => {
    try {
      const data = await api("/v1/campaigns");
      setCampaigns(data?.campaigns || []);
    } catch (error) {
      showToast(`Could not load campaigns: ${error.message}`, true);
    }
  }, [showToast]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    loadCampaigns();
  }, [loadCampaigns]);

  const refreshDashboard = useCallback(async (campaignId = currentCampaignId) => {
    if (!campaignId) return;
    try {
      const [dash, status, campaign] = await Promise.all([
        api(`/v1/campaigns/${campaignId}/dashboard`),
        api(`/v1/campaigns/${campaignId}/crawl/status`).catch(() => null),
        api(`/v1/campaigns/${campaignId}`).catch(() => null),
      ]);
      setSnapshot(dash);
      setCrawlStatus(status);
      if (campaign) setCurrentCampaign(campaign);
    } catch (error) {
      showToast(`Dashboard error: ${error.message}`, true);
    }
  }, [currentCampaignId, showToast]);

  async function openCampaign(campaignId) {
    setCurrentCampaignId(campaignId);
    const campaign = campaigns.find((c) => c.campaign_id === campaignId) || null;
    setCurrentCampaign(campaign);
    setView("dashboard");
    setNarrativeDetail(null);
    await refreshDashboard(campaignId);
  }

  useEffect(() => {
    if (view !== "dashboard" || !currentCampaignId || effectiveStatus !== "running") return undefined;
    const id = setInterval(() => {
      refreshDashboard(currentCampaignId);
    }, 6000);
    return () => clearInterval(id);
  }, [view, currentCampaignId, effectiveStatus, refreshDashboard]);

  async function runCrawl() {
    if (!currentCampaignId) return;
    try {
      await api(`/v1/campaigns/${currentCampaignId}/crawl/run-once`, { method: "POST" });
      showToast("Gathering started in the background.");
      await refreshDashboard();
      await loadCampaigns();
    } catch (error) {
      showToast(`Could not start crawl: ${error.message}`, true);
    }
  }

  async function stopCampaign() {
    if (!currentCampaignId) return;
    try {
      await api(`/v1/campaigns/${currentCampaignId}/crawl/stop`, { method: "POST" });
      showToast("Monitoring paused.");
      await refreshDashboard();
      await loadCampaigns();
    } catch (error) {
      showToast(`Could not stop campaign: ${error.message}`, true);
    }
  }

  async function submitCampaign(event) {
    event.preventDefault();
    setFormError("");
    setLoading(true);
    try {
      const keywords = toCSV(form.keywords);
      const hashtags = toCSV(form.hashtags);
      const interestGroups = [];
      if (keywords.length || hashtags.length) {
        interestGroups.push({ name: "Primary tracking", keywords, hashtags, priority: 1 });
      }
      const payload = {
        client_name: form.client_name.trim(),
        client_aliases: toCSV(form.aliases),
        region: form.region.trim(),
        monitored_topics: toCSV(form.topics),
        opponents: toCSV(form.opponents),
        client_accounts: toCSV(form.client_accounts),
        interest_groups: interestGroups,
        languages: ["en"],
        crawl_budget: {
          top_narratives: Number(form.top || 5),
          interactions_per_narrative: Number(form.interactions || 40),
          max_collection_results: Number(form.maxresults || 120),
        },
      };
      const campaign = await api("/v1/campaigns", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      if (form.runnow) {
        await api(`/v1/campaigns/${campaign.campaign_id}/crawl/run-once`, { method: "POST" });
      }
      showToast("Campaign created.");
      await loadCampaigns();
      await openCampaign(campaign.campaign_id);
    } catch (error) {
      setFormError(error.message);
    } finally {
      setLoading(false);
    }
  }

  async function openNarrative(narrativeId) {
    setView("narrative");
    setNarrativeDetail(null);
    setNarrativeSources([]);
    setNarrativeActors([]);
    try {
      const detail = await api(`/v1/narratives/${narrativeId}`);
      setNarrativeDetail(detail?.narrative || null);
      setNarrativeSources(detail?.sources || []);
      setNarrativeActors(detail?.actor_classifications || []);
    } catch (error) {
      showToast(`Could not load narrative: ${error.message}`, true);
      setView("dashboard");
    }
  }

  const dashboardStats = useMemo(() => {
    const totalReach = cards.reduce((acc, card) => acc + Number(card.reach_estimate || 0), 0);
    const avgBot = cards.length
      ? cards.reduce((acc, card) => acc + Number(card.inauthentic_percentage || 0), 0) / cards.length
      : 0;
    const needsAction = cards.filter((c) => {
      const label = String(c.risk_label || "").toLowerCase();
      return label === "high" || label === "critical" || Number(c.inauthentic_percentage || 0) >= 40;
    }).length;
    return { totalReach, avgBot, needsAction };
  }, [cards]);

  return (
    <div className="app-shell">
      <header className="topbar">
        <button className="brand" onClick={() => setView("campaigns")}>
          <span className="brand-logo">
            <svg width="22" height="22" viewBox="0 0 40 40" fill="none">
              <rect x="4" y="22" width="6" height="14" rx="2" fill="#00d4aa" />
              <rect x="13" y="16" width="6" height="20" rx="2" fill="#4facfe" />
              <rect x="22" y="10" width="6" height="26" rx="2" fill="#ff6b9d" />
              <rect x="31" y="4" width="6" height="32" rx="2" fill="#ffa502" />
            </svg>
          </span>
          <span className="brand-text">
            <strong>Provenance</strong>
            <small>Campaign Narrative Intelligence</small>
          </span>
        </button>
        <div className="topbar-actions">
          {campaigns.length > 0 && (
            <select value={currentCampaignId} onChange={(e) => e.target.value && openCampaign(e.target.value)}>
              <option value="">Switch campaign…</option>
              {campaigns.map((campaign) => (
                <option value={campaign.campaign_id} key={campaign.campaign_id}>
                  {campaign.client_name}
                </option>
              ))}
            </select>
          )}
          <button className="btn btn-primary" onClick={() => setView("new")}>
            + New Campaign
          </button>
        </div>
      </header>

      {view === "campaigns" && (
        <main className="page">
          <section className="hero">
            <h1>Know what's being said — and what to do about it</h1>
            <p>
              Provenance watches the conversation around your client on X, separates genuine public sentiment from
              bot- and AI-driven amplification, and tells you which narratives need a response first.
            </p>
          </section>
          {campaigns.length === 0 ? (
            <div className="empty-state">
              <p>No campaigns yet. Set one up to start monitoring narratives about your client.</p>
              <button className="btn btn-primary" onClick={() => setView("new")}>
                Create your first campaign
              </button>
            </div>
          ) : (
            <section className="campaign-grid">
              {campaigns.map((campaign) => (
                <button
                  key={campaign.campaign_id}
                  className="campaign-card"
                  onClick={() => openCampaign(campaign.campaign_id)}
                >
                  <div className="campaign-row">
                    <h3>{campaign.client_name}</h3>
                    <span className={statusClass(campaign.status)}>{campaign.status}</span>
                  </div>
                  <p className="campaign-meta">
                    {campaign.region || "Global"} · {(campaign.languages || ["en"]).join(", ")}
                  </p>
                  <div className="chip-row">
                    {(campaign.monitored_topics || []).slice(0, 4).map((topic) => (
                      <span className="chip" key={topic}>
                        {topic}
                      </span>
                    ))}
                  </div>
                  <div className="campaign-foot">Open dashboard →</div>
                </button>
              ))}
            </section>
          )}
        </main>
      )}

      {view === "new" && (
        <main className="page">
          <form className="form-shell" onSubmit={submitCampaign}>
            <div className="panel-head">
              <div>
                <h2>Set up monitoring</h2>
                <p>Tell us who to watch and what matters — we handle the gathering and scoring.</p>
              </div>
              <button type="button" className="btn btn-ghost" onClick={() => setView("campaigns")}>
                Cancel
              </button>
            </div>
            <label>
              Client / brand name <span className="req">*</span>
              <input
                required
                placeholder="e.g. Acme Corporation"
                value={form.client_name}
                onChange={(e) => setForm((old) => ({ ...old, client_name: e.target.value }))}
              />
            </label>
            <div className="grid-2">
              <label>
                Aliases <small>(comma separated)</small>
                <input
                  placeholder="Acme, AcmeCorp"
                  value={form.aliases}
                  onChange={(e) => setForm((old) => ({ ...old, aliases: e.target.value }))}
                />
              </label>
              <label>
                Region
                <input
                  placeholder="United States"
                  value={form.region}
                  onChange={(e) => setForm((old) => ({ ...old, region: e.target.value }))}
                />
              </label>
            </div>
            <label>
              Topics to monitor <small>(comma separated)</small>
              <input
                placeholder="product recall, data breach, layoffs"
                value={form.topics}
                onChange={(e) => setForm((old) => ({ ...old, topics: e.target.value }))}
              />
            </label>
            <label>
              Opponents / rivals <small>(comma separated)</small>
              <input
                placeholder="RivalCo"
                value={form.opponents}
                onChange={(e) => setForm((old) => ({ ...old, opponents: e.target.value }))}
              />
            </label>
            <label>
              Your own accounts to exclude <small>(comma separated handles)</small>
              <input
                placeholder="@acme, @acme_support"
                value={form.client_accounts}
                onChange={(e) => setForm((old) => ({ ...old, client_accounts: e.target.value }))}
              />
              <span className="field-hint">
                Posts from these accounts are filtered out so you only see outside chatter.
              </span>
            </label>
            <div className="grid-2">
              <label>
                Keywords <small>(comma separated)</small>
                <input
                  placeholder="acme scandal, acme outage"
                  value={form.keywords}
                  onChange={(e) => setForm((old) => ({ ...old, keywords: e.target.value }))}
                />
              </label>
              <label>
                Hashtags <small>(comma separated)</small>
                <input
                  placeholder="#acme, #boycottacme"
                  value={form.hashtags}
                  onChange={(e) => setForm((old) => ({ ...old, hashtags: e.target.value }))}
                />
              </label>
            </div>
            <div className="grid-3">
              <label>
                Top narratives
                <input
                  type="number"
                  min="1"
                  value={form.top}
                  onChange={(e) => setForm((old) => ({ ...old, top: e.target.value }))}
                />
              </label>
              <label>
                Interactions / narrative
                <input
                  type="number"
                  min="5"
                  value={form.interactions}
                  onChange={(e) => setForm((old) => ({ ...old, interactions: e.target.value }))}
                />
              </label>
              <label>
                Max posts collected
                <input
                  type="number"
                  min="10"
                  value={form.maxresults}
                  onChange={(e) => setForm((old) => ({ ...old, maxresults: e.target.value }))}
                />
              </label>
            </div>
            <label className="checkbox">
              <input
                type="checkbox"
                checked={form.runnow}
                onChange={(e) => setForm((old) => ({ ...old, runnow: e.target.checked }))}
              />
              Start gathering immediately
            </label>
            {formError && <p className="error-text">{formError}</p>}
            <div className="form-foot">
              <button disabled={loading} className="btn btn-primary" type="submit">
                {loading ? "Creating…" : "Create campaign"}
              </button>
            </div>
          </form>
        </main>
      )}

      {view === "dashboard" && (
        <main className="page">
          <section className="dashboard-head">
            <div>
              <h2>{currentCampaign?.client_name || "Campaign"}</h2>
              <p className="exec-summary">
                {snapshot?.executive_summary ||
                  "Narratives update automatically as monitoring runs in the background."}
              </p>
            </div>
            <div className="dashboard-actions">
              <span className={statusClass(effectiveStatus)}>{effectiveStatus}</span>
              <button className="btn btn-secondary" onClick={runCrawl} disabled={effectiveStatus === "running"}>
                {effectiveStatus === "running" ? "Gathering…" : "Gather now"}
              </button>
              <button className="btn btn-danger" onClick={stopCampaign} disabled={effectiveStatus === "stopped"}>
                Pause
              </button>
              <button className="btn btn-ghost" onClick={() => refreshDashboard()}>
                Refresh
              </button>
            </div>
          </section>

          <section className="stat-grid">
            <div className="stat-card" style={{ "--accent": COLORS.accent }}>
              <h4>Narratives tracked</h4>
              <strong>{cards.length}</strong>
            </div>
            <div className="stat-card" style={{ "--accent": COLORS.teal }}>
              <h4>Combined reach</h4>
              <strong>{shortReach(dashboardStats.totalReach)}</strong>
              <span className="stat-sub">estimated impressions</span>
            </div>
            <div
              className="stat-card"
              style={{ "--accent": dashboardStats.avgBot >= 40 ? COLORS.bot : COLORS.organic }}
            >
              <h4>Avg bot / AI share</h4>
              <strong className={dashboardStats.avgBot >= 40 ? "stat-bad" : "stat-good"}>
                {pct(dashboardStats.avgBot)}
              </strong>
              <span className="stat-sub">{dashboardStats.avgBot >= 40 ? "manufactured pressure" : "mostly organic"}</span>
            </div>
            <div
              className="stat-card"
              style={{ "--accent": dashboardStats.needsAction ? COLORS.orange : COLORS.organic }}
            >
              <h4>Need your attention</h4>
              <strong className={dashboardStats.needsAction ? "stat-warn" : "stat-good"}>
                {dashboardStats.needsAction}
              </strong>
              <span className="stat-sub">high-risk narratives</span>
            </div>
          </section>

          {cards.length > 0 && (
            <section className="chart-grid">
              <article className="panel">
                <h3>Bot / AI-driven vs organic by narrative</h3>
                <div className="chart-wrap">
                  <Bar
                    data={{
                      labels: cards.map((c) => String(c.narrative || "Narrative").slice(0, 30)),
                      datasets: [
                        {
                          label: "Bot / AI-driven %",
                          data: cards.map((c) => Math.round(c.inauthentic_percentage || 0)),
                          backgroundColor: COLORS.bot,
                          borderRadius: 4,
                        },
                        {
                          label: "Organic %",
                          data: cards.map((c) => Math.round(c.authentic_percentage || 0)),
                          backgroundColor: COLORS.organic,
                          borderRadius: 4,
                        },
                      ],
                    }}
                    options={{
                      responsive: true,
                      maintainAspectRatio: false,
                      indexAxis: "y",
                      scales: { x: { stacked: true, max: 100 }, y: { stacked: true } },
                      plugins: { legend: { position: "bottom" } },
                    }}
                  />
                </div>
              </article>
              <article className="panel">
                <h3>Reach — red means bot/AI-amplified</h3>
                <div className="chart-wrap">
                  <Bar
                    data={{
                      labels: cards.map((c) => String(c.narrative || "Narrative").slice(0, 22)),
                      datasets: [
                        {
                          label: "Estimated reach",
                          data: cards.map((c) => c.reach_estimate || 0),
                          backgroundColor: cards.map((c) =>
                            Number(c.inauthentic_percentage || 0) >= 40 ? COLORS.bot : COLORS.accent,
                          ),
                          borderRadius: 4,
                        },
                      ],
                    }}
                    options={{
                      responsive: true,
                      maintainAspectRatio: false,
                      plugins: {
                        legend: { display: false },
                        tooltip: {
                          callbacks: {
                            afterLabel: (ctx) =>
                              `Bot/AI-driven: ${Math.round(cards[ctx.dataIndex].inauthentic_percentage || 0)}%`,
                          },
                        },
                      },
                    }}
                  />
                </div>
              </article>
            </section>
          )}

          <section className="panel">
            <h3>Narratives by priority</h3>
            <p className="panel-sub">Ranked by how much they could move public perception of your client.</p>
            <div className="narrative-list">
              {cards.map((card, i) => {
                const trend = trendMeta(card.trend);
                const relevance = Math.round((card.relevance_score || 0) * 100);
                const isAI = card.committee_verdict?.source === "gemini";
                const loss = card.capital_loss_estimate || {};
                const action =
                  card.recommended_pr_action || card.committee_verdict?.recommended_action || "";
                return (
                  <button className="narrative-card" key={card.narrative_id} onClick={() => openNarrative(card.narrative_id)}>
                    <div className="rank">#{card.popularity_rank || i + 1}</div>
                    <div className="narrative-copy">
                      <div className="narrative-title">
                        <h4>{card.narrative}</h4>
                        <span className={riskClass(card.risk_label)}>{card.risk_label || "LOW"} risk</span>
                      </div>
                      <div className="tag-row">
                        <span className={`trend ${trend.cls}`}>
                          {trend.icon} {trend.text}
                        </span>
                        {relevance > 0 && <span className="tag tag-rel">{relevance}% relevant</span>}
                        <span className={`tag ${isAI ? "tag-ai" : "tag-heur"}`}>
                          {isAI ? "AI committee" : "Heuristic review"}
                        </span>
                        {loss.applies && loss.expected_usd ? (
                          <span className="tag tag-loss">⚠ ~{formatUSD(loss.expected_usd)} at risk</span>
                        ) : null}
                      </div>
                      <p className="narrative-summary">
                        {humanize(card.impact_summary || card.why_it_matters || card.summary)}
                      </p>
                      <AuthenticityBar
                        auth={card.authentic_percentage || 0}
                        inauth={card.inauthentic_percentage || 0}
                        unknown={card.unknown_percentage || 0}
                      />
                      <div className="mini-stats">
                        <span>
                          <b>{num(card.total_interactions)}</b> interactions
                        </span>
                        <span>
                          <b>{shortReach(card.reach_estimate)}</b> reach
                        </span>
                        <span>
                          <b>{pct(card.inauthentic_percentage)}</b> bot/AI
                        </span>
                        <span>
                          <b>{pct(card.authentic_percentage)}</b> organic
                        </span>
                      </div>
                      {action && (
                        <div className="action-line">
                          <span className="action-icon">🎯</span>
                          <span>
                            <span className="action-label">Recommended action</span>
                            <span className="action-text">{capitalize(humanize(action))}</span>
                          </span>
                        </div>
                      )}
                    </div>
                  </button>
                );
              })}
              {cards.length === 0 && (
                <p className="muted">
                  {effectiveStatus === "running"
                    ? "Gathering narratives in the background — this refreshes automatically."
                    : "No narratives collected yet. Click “Gather now” to run a crawl."}
                </p>
              )}
            </div>
          </section>
        </main>
      )}

      {view === "narrative" && (
        <main className="page narrative-page">
          <button className="btn btn-ghost back-btn" onClick={() => setView("dashboard")}>
            ← Back to dashboard
          </button>
          {!narrativeDetail ? (
            <section className="panel">
              <p className="loading-inline">Loading narrative…</p>
            </section>
          ) : (
            <NarrativeDetail
              detail={narrativeDetail}
              sources={narrativeSources}
              actors={narrativeActors}
            />
          )}
        </main>
      )}

      {toast && <div className={`toast ${toast.isError ? "toast-error" : ""}`}>{toast.message}</div>}
    </div>
  );
}

function NarrativeDetail({ detail, sources, actors }) {
  const verdict = detail.committee_verdict || {};
  const loss = verdict.capital_loss?.applies ? verdict.capital_loss : detail.capital_loss_estimate || {};
  const isAI = verdict.source === "gemini";
  const action = detail.recommended_pr_action || verdict.recommended_action || "Monitor — no response needed yet.";
  const whyItMatters = verdict.impact_summary || detail.why_it_matters || detail.summary;

  const auth = detail.authentic_percentage || 0;
  const inauth = detail.inauthentic_percentage || 0;
  const unknown = detail.unknown_percentage || 0;

  const breakdown = detail.interaction_breakdown || {};
  const breakdownOrder = [
    ["reply", "Replies", COLORS.accent],
    ["repost", "Reposts", COLORS.organic],
    ["quote", "Quotes", COLORS.orange],
    ["subtweet", "Subtweets", COLORS.bot],
    ["post", "Other posts", COLORS.unknown],
  ];
  const breakdownPresent = breakdownOrder.filter(([k]) => (breakdown[k] || 0) > 0);

  const timeline = detail.spread_timeline || [];

  // Key amplifiers: prefer the engine's ranked top_sources; fall back to raw X posts.
  const amplifiers = useMemo(() => {
    const ranked = (detail.top_sources || []).filter((s) => s.handle || s.display_name);
    if (ranked.length) return ranked.slice(0, 10);
    return (sources || [])
      .filter((s) => s.source_type === "x_post" && s.author)
      .map((s) => ({
        account_id: s.author.account_id,
        handle: s.author.handle,
        display_name: s.author.display_name,
        reach_estimate: s.author.followers_count,
        authenticity: (s.author.bot_likelihood || 0) >= 0.65 ? "Synthetic" : "Human",
        amplification_role: "",
        verified: s.author.verified,
      }))
      .slice(0, 10);
  }, [detail.top_sources, sources]);

  const xSources = useMemo(
    () => (sources || []).filter((s) => s.source_type === "x_post").slice(0, 8),
    [sources],
  );

  const botCount = useMemo(
    () => (actors || []).filter((a) => a.class === "Bot").length,
    [actors],
  );

  const primary = detail.primary_source_attribution;

  return (
    <>
      <div className="nd-head">
        <h2>{detail.narrative}</h2>
        <span className={riskClass(detail.risk_label)}>{detail.risk_label || "LOW"} risk</span>
        <span className={`tag ${isAI ? "tag-ai" : "tag-heur"}`}>
          {isAI ? "AI committee verdict" : "Heuristic verdict"}
        </span>
        {botCount > 0 && <span className="tag tag-bots">{botCount} bot-like accounts</span>}
      </div>
      {detail.summary && <p className="nd-summary">{detail.summary}</p>}

      {/* Action-first: what should the PR person do, and how much is at stake */}
      <section className="action-panel">
        <div className="action-card">
          <h3>Recommended action</h3>
          <p className="action-headline">{capitalize(humanize(action))}</p>
          {whyItMatters && <p className="why">{humanize(whyItMatters)}</p>}
          {(verdict.consensus_label || verdict.audience_effect) && (
            <p className="consensus">
              {verdict.consensus_label && (
                <>
                  Consensus: <b>{verdict.consensus_label}</b>
                </>
              )}
              {verdict.audience_effect && <> · {humanize(verdict.audience_effect)}</>}
            </p>
          )}
        </div>
        <CapitalLossBox loss={loss} />
      </section>

      {/* Engagement authenticity */}
      <section className="panel">
        <h3>Is this real public sentiment, or manufactured?</h3>
        <AuthenticityBar auth={auth} inauth={inauth} unknown={unknown} />
        <div className="legend">
          <span>
            <i className="dot organic" /> Organic {pct(auth)}
          </span>
          <span>
            <i className="dot bot" /> Bot / AI-driven {pct(inauth)}
          </span>
          <span>
            <i className="dot unknown" /> Unknown {pct(unknown)}
          </span>
        </div>
        <div className="mini-stats">
          <span>
            <b>{num(detail.total_interactions)}</b> classified interactions
          </span>
          <span>
            <b>{shortReach(detail.reach_estimate)}</b> estimated reach
          </span>
          <span>
            <b>{(detail.velocity_per_hour || 0).toFixed(1)}</b> / hour velocity
          </span>
        </div>
      </section>

      {timeline.length > 0 && (
        <section className="panel">
          <h3>How fast it's spreading</h3>
          <p className="panel-sub">Activity over time, split by organic vs bot/AI-driven accounts.</p>
          <div className="chart-wrap tall">
            <Line
              data={{
                labels: timeline.map((t) => dateText(t.t)),
                datasets: [
                  {
                    label: "Organic",
                    data: timeline.map((t) => t.authentic || 0),
                    borderColor: COLORS.organic,
                    backgroundColor: `${COLORS.organic}33`,
                    fill: true,
                    tension: 0.3,
                  },
                  {
                    label: "Bot / AI-driven",
                    data: timeline.map((t) => t.inauthentic || 0),
                    borderColor: COLORS.bot,
                    backgroundColor: `${COLORS.bot}33`,
                    fill: true,
                    tension: 0.3,
                  },
                  {
                    label: "Unknown",
                    data: timeline.map((t) => t.unknown || 0),
                    borderColor: COLORS.unknown,
                    backgroundColor: `${COLORS.unknown}22`,
                    fill: true,
                    tension: 0.3,
                  },
                ],
              }}
              options={{
                responsive: true,
                maintainAspectRatio: false,
                interaction: { mode: "index", intersect: false },
                scales: { y: { beginAtZero: true, stacked: true }, x: { stacked: true } },
                plugins: { legend: { position: "bottom" } },
              }}
            />
          </div>
        </section>
      )}

      <section className="narrative-grid">
        {(verdict.experts || []).length > 0 && (
          <article className="panel">
            <h3>Expert committee read</h3>
            <p className="panel-sub">How a panel of analysts weighs this narrative.</p>
            <div className="committee-grid">
              {verdict.experts.map((expert, idx) => {
                const sev = expert.severity >= 0.66 ? "sev-high" : expert.severity >= 0.33 ? "sev-mid" : "sev-low";
                return (
                  <div className="expert" key={`${expert.expert}-${idx}`}>
                    <div className="expert-head">
                      <strong>{expert.expert || "Expert"}</strong>
                      <span className={`sev ${sev}`}>severity {pct((expert.severity || 0) * 100)}</span>
                    </div>
                    <p>{humanize(expert.opinion)}</p>
                  </div>
                );
              })}
            </div>
          </article>
        )}

        {breakdownPresent.length > 0 && (
          <article className="panel">
            <h3>How people are engaging</h3>
            <div className="chart-wrap">
              <Doughnut
                data={{
                  labels: breakdownPresent.map(([, label]) => label),
                  datasets: [
                    {
                      data: breakdownPresent.map(([k]) => breakdown[k] || 0),
                      backgroundColor: breakdownPresent.map(([, , color]) => color),
                      borderWidth: 0,
                    },
                  ],
                }}
                options={{
                  responsive: true,
                  maintainAspectRatio: false,
                  plugins: { legend: { position: "right" } },
                }}
              />
            </div>
          </article>
        )}
      </section>

      {primary && (
        <section className="panel">
          <h3>Where it started</h3>
          <div className="kv-list">
            <div className="kv">
              <span>Origin type</span>
              <strong>{primary.source_type || "Unknown"}</strong>
            </div>
            <div className="kv">
              <span>Confidence</span>
              <strong>{pct((primary.confidence || 0) * 100)}</strong>
            </div>
            {primary.first_seen_at && (
              <div className="kv">
                <span>First seen</span>
                <strong>{dateText(primary.first_seen_at)}</strong>
              </div>
            )}
          </div>
          {(primary.evidence || []).length > 0 && (
            <p className="loss-note">{humanize((primary.evidence || []).slice(0, 3).join(" · "))}</p>
          )}
        </section>
      )}

      {amplifiers.length > 0 && (
        <section className="panel">
          <h3>Who's driving this</h3>
          <p className="panel-sub">The accounts amplifying the narrative — watch the ones flagged as bot-like.</p>
          <div className="amplifier-list">
            {amplifiers.map((a, idx) => {
              const isBot = String(a.authenticity || "").toLowerCase() === "synthetic";
              return (
                <div className="amplifier-row" key={a.account_id || a.handle || idx}>
                  <Avatar name={a.display_name || a.handle} />
                  <div className="amp-main">
                    <div className="amp-name">
                      {a.display_name || a.handle || "Unknown account"}
                      {a.verified && <span className="verified" title="Verified">✓</span>}
                    </div>
                    <div className="amp-sub">
                      {a.handle ? `@${a.handle}` : ""}
                      {a.reach_estimate ? ` · ${shortReach(a.reach_estimate)} reach` : ""}
                      {a.interaction_count ? ` · ${num(a.interaction_count)} interactions` : ""}
                    </div>
                    {a.amplification_role && <div className="amp-role">{prettyRole(a.amplification_role)}</div>}
                  </div>
                  <span className={`bot-tag ${isBot ? "bot" : "human"}`}>{isBot ? "Bot-like" : "Likely human"}</span>
                </div>
              );
            })}
          </div>
        </section>
      )}

      {xSources.length > 0 && (
        <section className="panel">
          <h3>Representative posts</h3>
          <p className="panel-sub">A sample of what's actually being said — useful raw material for your response.</p>
          <div className="source-list">
            {xSources.map((s, idx) => {
              const a = s.author || {};
              const url = s.url || s.canonical_url || "";
              const eng = s.engagement || {};
              const botPct = Math.round((a.bot_likelihood || 0) * 100);
              return (
                <div className="source-row" key={s.source_id || idx}>
                  <div className="source-author">
                    <Avatar name={a.display_name || a.handle} />
                    <div style={{ flex: 1 }}>
                      <div className="author-name">
                        {a.display_name || a.handle || "Unknown"}
                        {a.verified && <span className="verified" title="Verified">✓</span>}
                      </div>
                      <div className="author-handle">
                        {a.handle ? `@${a.handle}` : ""}
                        {a.followers_count ? ` · ${shortReach(a.followers_count)} followers` : ""}
                      </div>
                    </div>
                    <span className={`bot-tag ${botPct >= 65 ? "bot" : "human"}`}>
                      {botPct >= 65 ? "Bot-like" : "Likely human"} {botPct}%
                    </span>
                  </div>
                  <p className="source-text">{s.text || s.snippet || "—"}</p>
                  <div className="source-meta">
                    {url && (
                      <a href={url} target="_blank" rel="noopener noreferrer">
                        View on X ↗
                      </a>
                    )}
                    {eng.likes != null && <span>❤ {num(eng.likes)}</span>}
                    {eng.reposts != null && <span>🔁 {num(eng.reposts)}</span>}
                    {eng.replies != null && <span>💬 {num(eng.replies)}</span>}
                    {s.published_at && <span>{dateText(s.published_at)}</span>}
                  </div>
                </div>
              );
            })}
          </div>
        </section>
      )}
    </>
  );
}

export default App;
