// ═══════════════════════════════════════════════════════════════════
// DataScope — Provenance Analytics UI
// Reads ProvenanceReport JSON from the backend pipeline
// ═══════════════════════════════════════════════════════════════════

const USE_MOCK = false;
const MOCK_DATA_URL = "mock-data.json";
const API_URL = "/api/analyze";

let reportData = null;
let chartInstances = {};

// ===== IMAGE HELPER =====
// Uses loremflickr.com with topic keywords extracted from the analysis results.
// The backend returns _ui_topics (most prevalent words from scraped content).
// We use those for more relevant, distinguishing imagery.
function getTopicImageUrl(keywords, w = 800, h = 600, seed = 1) {
  // keywords can be a string or array
  let kw;
  if (Array.isArray(keywords)) {
    kw = keywords.slice(0, 3).join(",");
  } else {
    kw = keywords.split(" ").slice(0, 3).join(",");
  }
  return `https://loremflickr.com/${w}/${h}/${encodeURIComponent(kw)}?lock=${seed}`;
}

/**
 * Extract the most interesting/prevalent topics from the report data.
 * Uses _ui_topics from backend if available, otherwise extracts from titles.
 */
function extractTopics(data) {
  // Backend provides extracted topics
  if (data._ui_topics && data._ui_topics.length > 0) {
    return data._ui_topics;
  }

  // Fallback: extract from titles in the results
  const texts = (data.ai_signature_results || [])
    .map(r => r.ranked_result?.scraped_result?.title || "")
    .join(" ");

  const stopWords = new Set(["the","a","an","is","are","was","were","and","or","but","in","on","at","to","for","of","with","by","from","that","this","it","its","not","has","have","had","been","will","would","could","should","can","may","might","shall","about","into","through","during","before","after","between","out","off","over","under","up","down","no","all","each","every","both","few","more","most","other","some","such","only","own","same","so","than","too","very","just","also","how","what","which","who","whom","whose","where","when","why"]);

  const wordCounts = {};
  texts.toLowerCase().split(/\W+/).forEach(w => {
    if (w.length > 3 && !stopWords.has(w)) {
      wordCounts[w] = (wordCounts[w] || 0) + 1;
    }
  });

  return Object.entries(wordCounts)
    .sort((a, b) => b[1] - a[1])
    .slice(0, 5)
    .map(e => e[0]);
}

// ===== ENTRY POINTS =====
function startScrape() {
  const query = document.getElementById("queryInput").value.trim();
  if (!query) return;
  runAnalysis(query);
}

function startScrapeFromTop() {
  const query = document.getElementById("queryInputTop").value.trim();
  if (!query) return;
  runAnalysis(query);
}

function goHome() {
  document.getElementById("dashboardView").classList.add("hidden");
  document.getElementById("landingView").classList.remove("hidden");
  destroyCharts();
}

// ===== MAIN FLOW =====
async function runAnalysis(query) {
  showLoading(query);

  try {
    let data;
    if (USE_MOCK) {
      await sleep(2000);
      const resp = await fetch(MOCK_DATA_URL);
      if (!resp.ok) throw new Error("Failed to load data");
      data = await resp.json();
    } else {
      // Trigger the real backend pipeline — this runs the full provenance analysis
      const resp = await fetch(API_URL, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ query }),
      });
      if (!resp.ok) {
        const err = await resp.json().catch(() => ({}));
        throw new Error(err.error || `Pipeline failed (${resp.status})`);
      }
      data = await resp.json();
    }

    reportData = data;

    // Extract prevalent topics for imagery
    const topics = extractTopics(data);

    document.getElementById("landingView").classList.add("hidden");
    document.getElementById("dashboardView").classList.remove("hidden");
    document.getElementById("topbarDate").textContent = new Date().toLocaleDateString("en-US", { weekday: "short", month: "short", day: "numeric", year: "numeric" });

    applyTopicVisuals(topics, data);
    populateSummary(data);
    renderSimilarityChart(data);
    renderDomainChart(data);
    renderContentTypeChart(data);
    renderBotChart(data);
    renderFeaturesChart(data);
    renderTimelineChart(data);
    renderConfidenceChart(data);
    renderScatterChart(data);
    renderArticlesList(data);

    hideLoading();
  } catch (err) {
    hideLoading();
    showError(err.message);
  }
}

function showError(msg) {
  // Show error in a styled overlay instead of alert()
  const overlay = document.createElement("div");
  overlay.id = "errorOverlay";
  overlay.style.cssText = "position:fixed;inset:0;background:rgba(0,0,0,0.8);z-index:10000;display:flex;align-items:center;justify-content:center;padding:20px;";
  overlay.innerHTML = `
    <div style="background:#1a2332;border:1px solid #ff4757;border-radius:16px;padding:32px;max-width:500px;width:100%;text-align:center;">
      <div style="font-size:2rem;margin-bottom:12px;">⚠️</div>
      <h3 style="color:#ff4757;margin-bottom:12px;font-size:1.1rem;">Analysis Failed</h3>
      <p style="color:#7a8ba0;font-size:0.9rem;line-height:1.6;margin-bottom:20px;">${msg}</p>
      <button onclick="document.getElementById('errorOverlay').remove()" style="background:#ff4757;color:#fff;border:none;border-radius:8px;padding:10px 24px;font-size:0.9rem;font-weight:600;cursor:pointer;">OK</button>
    </div>
  `;
  document.body.appendChild(overlay);
}

// ===== LOADING =====
function showLoading(query) {
  document.getElementById("loadingOverlay").classList.remove("hidden");
  if (query) {
    // Use the raw query for loading screen images (topics not yet available)
    const keywords = query.split(" ").filter(w => w.length > 3).slice(0, 3);
    const imgKeywords = keywords.length > 0 ? keywords : ["analysis", "data", "research"];
    document.getElementById("loadingBgImage").style.backgroundImage = `url('${getTopicImageUrl(imgKeywords, 1200, 800, 99)}')`;
    document.getElementById("loadingSubtext").textContent = `Running provenance pipeline for "${query.slice(0, 60)}${query.length > 60 ? "..." : ""}"`;
  }
}
function hideLoading() { document.getElementById("loadingOverlay").classList.add("hidden"); }

// ===== TOPIC VISUALS =====
function applyTopicVisuals(topics, data) {
  // Use extracted topics for all imagery — these are the most prevalent
  // and distinguishing words from the actual scraped content
  const imgTopics = topics.length > 0 ? topics : ["news", "analysis"];

  // Dashboard background — use first 2 topics for a broad image
  const bgEl = document.getElementById("dashboardBgImage");
  bgEl.style.backgroundImage = `url('${getTopicImageUrl(imgTopics.slice(0, 2), 1400, 900, 1)}')`;

  // Floating images — each uses a different subset of topics for variety
  const floatingContainer = document.getElementById("floatingImages");
  floatingContainer.innerHTML = "";
  for (let i = 0; i < 3; i++) {
    const div = document.createElement("div");
    div.className = "floating-img";
    // Rotate through different topic combinations for each floating image
    const topicSlice = [imgTopics[i % imgTopics.length], imgTopics[(i + 1) % imgTopics.length]];
    div.style.backgroundImage = `url('${getTopicImageUrl(topicSlice, 400, 300, i + 10)}')`;
    floatingContainer.appendChild(div);
  }

  // Risk banner
  const risk = data.risk_label || "LOW";
  const gauge = document.getElementById("riskGauge");
  gauge.className = `risk-gauge ${risk.toLowerCase()}`;
  gauge.querySelector(".risk-gauge-label").textContent = risk;

  const headline = data.source_item?.headline || imgTopics.join(" ");
  document.getElementById("topicBannerTitle").textContent = headline.length > 80 ? headline.slice(0, 80) + "..." : headline;
  document.getElementById("topicBannerSub").textContent = `${data.ai_signature_results.length} sources analyzed • Pipeline v${data.pipeline_version} • ${data.total_duration_seconds.toFixed(1)}s • Topics: ${imgTopics.slice(0, 3).join(", ")}`;
}

// ===== SUMMARY =====
function populateSummary(data) {
  const results = data.ai_signature_results || [];
  const riskPct = Math.round(data.disinformation_risk * 100);
  const aiCount = results.filter(r => r.is_ai_generated).length;
  const avgBot = results.length > 0
    ? (results.reduce((s, r) => s + r.ensemble_score, 0) / results.length).toFixed(2)
    : "0.00";

  document.getElementById("summaryRisk").textContent = `${riskPct}%`;
  const badge = document.getElementById("summaryRiskBadge");
  badge.textContent = data.risk_label;
  badge.className = `risk-badge ${data.risk_label.toLowerCase()}`;

  document.getElementById("summarySources").textContent = results.length;
  document.getElementById("summaryAI").textContent = `${aiCount} / ${results.length}`;
  document.getElementById("summaryBotAvg").textContent = avgBot;
}

// ===== CHARTS =====
function destroyCharts() { Object.values(chartInstances).forEach(c => c.destroy()); chartInstances = {}; }

function chartDefaults() {
  return { responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false } } };
}

// 1. Similarity & Composite Scores (horizontal bar)
function renderSimilarityChart(data) {
  const ctx = document.getElementById("similarityChart").getContext("2d");
  if (chartInstances.similarity) chartInstances.similarity.destroy();

  const results = data.ai_signature_results;
  const labels = results.map(r => truncate(r.ranked_result.scraped_result.domain, 20));
  const similarity = results.map(r => r.ranked_result.similarity_score);
  const composite = results.map(r => r.ranked_result.composite_score);

  chartInstances.similarity = new Chart(ctx, {
    type: "bar",
    data: {
      labels,
      datasets: [
        { label: "Similarity", data: similarity, backgroundColor: "rgba(0,229,255,0.7)", borderRadius: 4 },
        { label: "Composite", data: composite, backgroundColor: "rgba(0,212,170,0.7)", borderRadius: 4 },
      ],
    },
    options: {
      ...chartDefaults(),
      indexAxis: "y",
      plugins: { legend: { display: true, position: "top", labels: { color: "#7a8ba0", font: { size: 10 }, usePointStyle: true, pointStyle: "circle" } } },
      scales: {
        x: { min: 0, max: 1, grid: { color: "rgba(31,46,63,0.5)" }, ticks: { color: "#7a8ba0", font: { size: 10 } } },
        y: { grid: { display: false }, ticks: { color: "#7a8ba0", font: { size: 10 } } },
      },
      onClick: (e, els) => { if (els.length) openSourceModal(els[0].index); },
      onHover: (e, els) => { e.native.target.style.cursor = els.length ? "pointer" : "default"; },
    },
  });
}

// 2. Domain Distribution (doughnut)
function renderDomainChart(data) {
  const ctx = document.getElementById("domainChart").getContext("2d");
  if (chartInstances.domain) chartInstances.domain.destroy();

  const domainCounts = {};
  data.ai_signature_results.forEach(r => {
    const d = r.ranked_result.scraped_result.domain;
    domainCounts[d] = (domainCounts[d] || 0) + 1;
  });

  const labels = Object.keys(domainCounts);
  const values = Object.values(domainCounts);
  const colors = labels.map((_, i) => `hsl(${(i * 60 + 160) % 360}, 65%, 55%)`);

  chartInstances.domain = new Chart(ctx, {
    type: "doughnut",
    data: { labels, datasets: [{ data: values, backgroundColor: colors, borderColor: "#1a2332", borderWidth: 3 }] },
    options: {
      ...chartDefaults(),
      cutout: "60%",
      plugins: { legend: { display: true, position: "bottom", labels: { color: "#7a8ba0", font: { size: 10 }, padding: 12, usePointStyle: true, pointStyle: "circle" } } },
    },
  });
}

// 3. Content Type (pie)
function renderContentTypeChart(data) {
  const ctx = document.getElementById("contentTypeChart").getContext("2d");
  if (chartInstances.contentType) chartInstances.contentType.destroy();

  const typeCounts = {};
  data.ai_signature_results.forEach(r => {
    const t = r.ranked_result.scraped_result.content_type || "unknown";
    typeCounts[t] = (typeCounts[t] || 0) + 1;
  });

  const colorMap = { article: "#4facfe", social_post: "#ff6b9d", forum: "#ffa502", unknown: "#7a8ba0" };
  const labels = Object.keys(typeCounts);
  const values = Object.values(typeCounts);
  const colors = labels.map(l => colorMap[l] || "#7a8ba0");

  chartInstances.contentType = new Chart(ctx, {
    type: "pie",
    data: { labels: labels.map(l => l.replace("_", " ")), datasets: [{ data: values, backgroundColor: colors, borderColor: "#1a2332", borderWidth: 3 }] },
    options: {
      ...chartDefaults(),
      plugins: { legend: { display: true, position: "bottom", labels: { color: "#7a8ba0", font: { size: 10 }, padding: 12, usePointStyle: true, pointStyle: "circle" } } },
    },
  });
}

// 4. Bot Likelihood (bar)
function renderBotChart(data) {
  const ctx = document.getElementById("botChart").getContext("2d");
  if (chartInstances.bot) chartInstances.bot.destroy();

  const results = data.ai_signature_results;
  const labels = results.map(r => truncate(r.ranked_result.scraped_result.domain, 18));
  const scores = results.map(r => r.ensemble_score);
  const colors = scores.map(s => s >= 0.65 ? "rgba(255,71,87,0.8)" : s >= 0.35 ? "rgba(255,165,2,0.8)" : "rgba(0,212,170,0.7)");

  chartInstances.bot = new Chart(ctx, {
    type: "bar",
    data: { labels, datasets: [{ data: scores, backgroundColor: colors, borderRadius: 5, borderSkipped: false }] },
    options: {
      ...chartDefaults(),
      scales: {
        x: { grid: { display: false }, ticks: { color: "#7a8ba0", font: { size: 9 }, maxRotation: 45 } },
        y: { min: 0, max: 1, grid: { color: "rgba(31,46,63,0.5)" }, ticks: { color: "#7a8ba0", font: { size: 10 } } },
      },
      onClick: (e, els) => { if (els.length) openSourceModal(els[0].index); },
      onHover: (e, els) => { e.native.target.style.cursor = els.length ? "pointer" : "default"; },
    },
  });
}

// 5. Statistical Features (radar)
function renderFeaturesChart(data) {
  const ctx = document.getElementById("featuresChart").getContext("2d");
  if (chartInstances.features) chartInstances.features.destroy();

  const featureLabels = ["Sentence Uniformity", "Burstiness", "Transition Density", "Hedging Density", "Paragraph Homogeneity"];
  const datasets = [];
  const colors = ["#00d4aa", "#4facfe", "#ff6b9d", "#ffa502", "#00e5ff"];

  data.ai_signature_results.slice(0, 5).forEach((r, i) => {
    const stat = r.detection_methods.find(m => m.method_name === "statistical");
    if (stat && stat.raw_response && stat.raw_response.features) {
      const f = stat.raw_response.features;
      datasets.push({
        label: truncate(r.ranked_result.scraped_result.domain, 15),
        data: [f.sentence_uniformity, f.burstiness_ai, f.transition_density_ai, f.hedging_density_ai, f.paragraph_homogeneity],
        borderColor: colors[i],
        backgroundColor: colors[i].replace(")", ",0.1)").replace("rgb", "rgba"),
        pointBackgroundColor: colors[i],
        borderWidth: 2,
      });
    }
  });

  chartInstances.features = new Chart(ctx, {
    type: "radar",
    data: { labels: featureLabels, datasets },
    options: {
      ...chartDefaults(),
      plugins: { legend: { display: true, position: "top", labels: { color: "#7a8ba0", font: { size: 9 }, usePointStyle: true, pointStyle: "circle" } } },
      scales: {
        r: {
          min: 0, max: 1,
          grid: { color: "rgba(31,46,63,0.5)" },
          angleLines: { color: "rgba(31,46,63,0.5)" },
          pointLabels: { color: "#7a8ba0", font: { size: 9 } },
          ticks: { display: false },
        },
      },
    },
  });
}

// 6. Publication Timeline (scatter/line)
function renderTimelineChart(data) {
  const ctx = document.getElementById("timelineChart").getContext("2d");
  if (chartInstances.timeline) chartInstances.timeline.destroy();

  const results = data.ai_signature_results
    .filter(r => r.ranked_result.scraped_result.published_at)
    .sort((a, b) => new Date(a.ranked_result.scraped_result.published_at) - new Date(b.ranked_result.scraped_result.published_at));

  const labels = results.map(r => {
    const d = new Date(r.ranked_result.scraped_result.published_at);
    return d.toLocaleDateString("en-US", { month: "short", year: "2-digit" });
  });
  const scores = results.map(r => r.ranked_result.composite_score);

  const gradient = ctx.createLinearGradient(0, 0, 0, 220);
  gradient.addColorStop(0, "rgba(255,215,0,0.2)");
  gradient.addColorStop(1, "rgba(255,215,0,0)");

  chartInstances.timeline = new Chart(ctx, {
    type: "line",
    data: {
      labels,
      datasets: [{
        data: scores,
        borderColor: "#ffd700",
        backgroundColor: gradient,
        fill: true,
        tension: 0.3,
        pointRadius: 5,
        pointBackgroundColor: "#ffd700",
        pointBorderColor: "#0a0e17",
        pointBorderWidth: 2,
        borderWidth: 2,
      }],
    },
    options: {
      ...chartDefaults(),
      scales: {
        x: { grid: { display: false }, ticks: { color: "#7a8ba0", font: { size: 9 } } },
        y: { min: 0, max: 1, grid: { color: "rgba(31,46,63,0.5)" }, ticks: { color: "#7a8ba0", font: { size: 9 } } },
      },
    },
  });
}

// 7. Confidence (horizontal bar)
function renderConfidenceChart(data) {
  const ctx = document.getElementById("confidenceChart").getContext("2d");
  if (chartInstances.confidence) chartInstances.confidence.destroy();

  const results = data.ai_signature_results;
  const labels = results.map(r => truncate(r.ranked_result.scraped_result.domain, 18));
  const values = results.map(r => r.confidence);
  const colors = values.map(v => `rgba(79,172,254,${0.4 + v * 0.6})`);

  chartInstances.confidence = new Chart(ctx, {
    type: "bar",
    data: { labels, datasets: [{ data: values, backgroundColor: colors, borderRadius: 4, borderSkipped: false }] },
    options: {
      ...chartDefaults(),
      indexAxis: "y",
      scales: {
        x: { min: 0, max: 1, grid: { color: "rgba(31,46,63,0.5)" }, ticks: { color: "#7a8ba0", font: { size: 9 } } },
        y: { grid: { display: false }, ticks: { color: "#7a8ba0", font: { size: 9 } } },
      },
    },
  });
}

// 8. Burstiness vs Uniformity (scatter)
function renderScatterChart(data) {
  const ctx = document.getElementById("scatterChart").getContext("2d");
  if (chartInstances.scatter) chartInstances.scatter.destroy();

  const points = [];
  data.ai_signature_results.forEach(r => {
    const stat = r.detection_methods.find(m => m.method_name === "statistical");
    if (stat && stat.raw_response && stat.raw_response.features) {
      points.push({
        x: stat.raw_response.features.burstiness_ai,
        y: stat.raw_response.features.sentence_uniformity,
      });
    }
  });

  chartInstances.scatter = new Chart(ctx, {
    type: "scatter",
    data: {
      datasets: [{
        data: points,
        backgroundColor: "rgba(255,107,157,0.7)",
        pointRadius: 8,
        pointHoverRadius: 11,
        borderColor: "rgba(255,107,157,1)",
        borderWidth: 1,
      }],
    },
    options: {
      ...chartDefaults(),
      scales: {
        x: { min: 0, max: 1, title: { display: true, text: "Burstiness", color: "#7a8ba0", font: { size: 10 } }, grid: { color: "rgba(31,46,63,0.5)" }, ticks: { color: "#7a8ba0", font: { size: 9 } } },
        y: { min: 0, max: 1, title: { display: true, text: "Uniformity", color: "#7a8ba0", font: { size: 10 } }, grid: { color: "rgba(31,46,63,0.5)" }, ticks: { color: "#7a8ba0", font: { size: 9 } } },
      },
    },
  });
}

// ===== ARTICLES LIST =====
function renderArticlesList(data) {
  const container = document.getElementById("articlesList");
  const results = [...data.ai_signature_results].sort((a, b) => b.ranked_result.composite_score - a.ranked_result.composite_score);

  document.getElementById("articleCountBadge").textContent = `${results.length} sources`;

  container.innerHTML = results.map((r, i) => {
    const sr = r.ranked_result.scraped_result;
    const rankClass = i === 0 ? "gold" : i === 1 ? "silver" : i === 2 ? "bronze" : "normal";
    const botClass = r.is_ai_generated ? "bot" : "human";
    const botLabel = r.is_ai_generated ? `AI (${(r.ensemble_score * 100).toFixed(0)}%)` : `Human (${(r.ensemble_score * 100).toFixed(0)}%)`;

    return `
      <div class="article-row" onclick="openSourceModal(${data.ai_signature_results.indexOf(r)})">
        <div class="article-rank ${rankClass}">${i + 1}</div>
        <div class="article-info">
          <div class="article-title"><a href="${escapeHtml(sr.url)}" target="_blank" rel="noopener" onclick="event.stopPropagation()">${escapeHtml(sr.title || sr.url)}</a></div>
          <p class="article-snippet">${escapeHtml(sr.snippet || "")}</p>
          <div class="article-meta">
            <span class="meta-tag domain">${escapeHtml(sr.domain)}</span>
            <span class="meta-tag type">${(sr.content_type || "unknown").replace("_", " ")}</span>
            <span class="meta-tag ${botClass}">${botLabel}</span>
            <span class="meta-tag similarity">Sim: ${r.ranked_result.similarity_score.toFixed(2)}</span>
          </div>
        </div>
      </div>
    `;
  }).join("");
}

// ===== MODAL =====
function openSourceModal(index) {
  const r = reportData.ai_signature_results[index];
  if (!r) return;

  const sr = r.ranked_result.scraped_result;
  const modal = document.getElementById("modal");
  const title = document.getElementById("modalTitle");
  const body = document.getElementById("modalBody");

  title.textContent = sr.title || sr.domain;

  const stat = r.detection_methods.find(m => m.method_name === "statistical");
  const features = stat?.raw_response?.features || {};

  body.innerHTML = `
    <div class="modal-section">
      <h4>Source Info</h4>
      <div class="modal-kv"><span class="key">URL</span><span class="val"><a href="${escapeHtml(sr.url)}" target="_blank" style="color:var(--accent)">${escapeHtml(sr.url.slice(0, 60))}...</a></span></div>
      <div class="modal-kv"><span class="key">Domain</span><span class="val">${escapeHtml(sr.domain)}</span></div>
      <div class="modal-kv"><span class="key">Content Type</span><span class="val">${(sr.content_type || "unknown").replace("_", " ")}</span></div>
      <div class="modal-kv"><span class="key">Published</span><span class="val">${sr.published_at ? new Date(sr.published_at).toLocaleDateString() : "Unknown"}</span></div>
      <div class="modal-kv"><span class="key">Chronological Rank</span><span class="val">#${r.ranked_result.chronological_rank}</span></div>
      <div class="modal-kv"><span class="key">Likely Original</span><span class="val ${r.ranked_result.is_likely_original ? 'green' : ''}">${r.ranked_result.is_likely_original ? "Yes ★" : "No"}</span></div>
    </div>
    <div class="modal-section">
      <h4>Scoring</h4>
      <div class="modal-kv"><span class="key">Similarity Score</span><span class="val">${r.ranked_result.similarity_score.toFixed(4)}</span></div>
      <div class="modal-kv"><span class="key">Composite Score</span><span class="val">${r.ranked_result.composite_score.toFixed(4)}</span></div>
      <div class="modal-kv"><span class="key">Ensemble (Bot) Score</span><span class="val ${r.ensemble_score >= 0.65 ? 'red' : r.ensemble_score >= 0.35 ? 'orange' : 'green'}">${r.ensemble_score.toFixed(3)}</span></div>
      <div class="modal-kv"><span class="key">AI Generated?</span><span class="val ${r.is_ai_generated ? 'red' : 'green'}">${r.is_ai_generated ? "YES" : "NO"}</span></div>
      <div class="modal-kv"><span class="key">Confidence</span><span class="val">${(r.confidence * 100).toFixed(0)}%</span></div>
    </div>
    <div class="modal-section">
      <h4>Statistical Features</h4>
      <div class="modal-kv"><span class="key">Sentence Uniformity</span><span class="val">${features.sentence_uniformity?.toFixed(3) ?? "N/A"}</span></div>
      <div class="modal-kv"><span class="key">Burstiness (AI)</span><span class="val">${features.burstiness_ai?.toFixed(3) ?? "N/A"}</span></div>
      <div class="modal-kv"><span class="key">Transition Density</span><span class="val">${features.transition_density_ai?.toFixed(3) ?? "N/A"}</span></div>
      <div class="modal-kv"><span class="key">Hedging Density</span><span class="val">${features.hedging_density_ai?.toFixed(3) ?? "N/A"}</span></div>
      <div class="modal-kv"><span class="key">Paragraph Homogeneity</span><span class="val">${features.paragraph_homogeneity?.toFixed(3) ?? "N/A"}</span></div>
    </div>
    <div class="modal-section">
      <h4>Detection Methods</h4>
      ${r.detection_methods.map(m => `
        <div class="modal-kv">
          <span class="key">${m.method_name}</span>
          <span class="val ${m.error ? 'orange' : m.score !== null && m.score >= 0.65 ? 'red' : 'green'}">${m.error ? "Skipped" : m.score !== null ? m.score.toFixed(3) + " (" + (m.label || "?") + ")" : "N/A"}</span>
        </div>
      `).join("")}
    </div>
    <div class="modal-section">
      <h4>Explanation</h4>
      <p style="font-size:0.85rem;color:var(--text-secondary);line-height:1.6">${escapeHtml(r.explanation)}</p>
    </div>
  `;

  modal.classList.remove("hidden");
  document.body.style.overflow = "hidden";
}

function closeModal() {
  document.getElementById("modal").classList.add("hidden");
  document.body.style.overflow = "";
}

// ===== UTILITIES =====
function escapeHtml(str) {
  if (!str) return "";
  const div = document.createElement("div");
  div.textContent = str;
  return div.innerHTML;
}

function truncate(str, len) {
  if (!str) return "";
  return str.length > len ? str.slice(0, len) + "…" : str;
}

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

// ===== EVENT LISTENERS =====
document.getElementById("queryInput").addEventListener("keydown", e => { if (e.key === "Enter") startScrape(); });
document.getElementById("queryInputTop").addEventListener("keydown", e => { if (e.key === "Enter") startScrapeFromTop(); });
document.getElementById("modal").addEventListener("click", e => { if (e.target === e.currentTarget) closeModal(); });
document.addEventListener("keydown", e => { if (e.key === "Escape") closeModal(); });
