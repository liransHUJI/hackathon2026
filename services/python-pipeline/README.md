# 🔎 Provenance Pipeline

> **Tracing the origin of news in the age of AI.**

Given any news headline, the Provenance Pipeline fans out across the web to find every prior
occurrence of the story, ranks them chronologically, and then subjects the earliest sources to
multi-method AI-generation detection — surfacing where a piece of disinformation was seeded and
whether it was written by a machine.

---

## Pipeline Overview

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                           PROVENANCE PIPELINE                                │
│                                                                              │
│  ┌─────────────────┐   ┌─────────────────┐   ┌──────────────────────────┐   │
│  │  1. Ingestion   │   │  2. Semantic    │   │   3. Broad Scraper       │   │
│  │     Agent       │──▶│     Agent       │──▶│       Agent              │   │
│  │                 │   │                 │   │                          │   │
│  │ Telegram / RSS  │   │  LLM → ~100     │   │ BasicWebScraper      OR  │   │
│  │  → NewsItem     │   │  permutations   │   │ BrightDataScraper        │   │
│  └─────────────────┘   └────────┬────────┘   └────────────┬─────────────┘   │
│                                 │                         │                 │
│                         PermutationSet            ScrapedResultSet          │
│                                 │                         │                 │
│                          asyncio.Queue             asyncio.Queue            │
│                                                          │                  │
│             ┌────────────────────────────────────────────┘                  │
│             ▼                                                                │
│  ┌──────────────────────┐    ┌──────────────────────────────────────────┐   │
│  │ 4. Chronological &   │    │       5. AI Signature Detector           │   │
│  │  Similarity Analyzer │───▶│              Agent                       │   │
│  │                      │    │                                          │   │
│  │ Sort ▸ Embed ▸ Rank  │    │  GPTZero + Sapling + Statistical +       │   │
│  │ Filter ▸ Top 10      │    │  LLM-Judge → ensemble score              │   │
│  └──────────────────────┘    └────────────────────┬─────────────────────┘   │
│                                                   │                         │
│                                          ProvenanceReport                   │
└──────────────────────────────────────────────────────────────────────────────┘
```

### Stage-by-stage

| # | Agent | Input | Output |
|---|-------|-------|--------|
| 1 | **IngestionAgent** | Telegram channel or RSS feed URL | `NewsItem` |
| 2 | **SemanticAgent** | `NewsItem` | `PermutationSet` (~100 query variants) |
| 3 | **BroadScraperAgent** | `PermutationSet` | `ScrapedResultSet` (deduplicated articles/posts) |
| 4 | **ChronologicalSimilarityAnalyzer** | `ScrapedResultSet` | `AnalyzedSet` (top-10 candidates ranked by date + similarity) |
| 5 | **AISignatureDetectorAgent** | `AnalyzedSet` | `ProvenanceReport` (per-item AI probability + narrative summary) |

---

## Prerequisites

| Requirement | Notes |
|-------------|-------|
| **Python 3.11+** | Required. Tested on 3.11 and 3.12. |
| `ANTHROPIC_API_KEY` | **Required.** Powers the Semantic Agent and LLM-Judge detector. |
| `GPTZERO_API_KEY` | Optional. Free tier: 10k words/month at [gptzero.me](https://gptzero.me). |
| `SAPLING_API_KEY` | Optional. Free tier available at [sapling.ai](https://sapling.ai). |
| `BRIGHTDATA_API_KEY` | Optional. **Hackathon-day only.** $75 in credits. |
| `TELEGRAM_API_ID` / `TELEGRAM_API_HASH` | Optional. Required only for Telegram ingestion. |

The pipeline runs with just `ANTHROPIC_API_KEY`. All other keys unlock additional capabilities;
the `BasicWebScraper` (DuckDuckGo + httpx) requires no paid credentials.

---

## Installation

```bash
# 1. Clone the repository and enter this service
git clone <repo-url> provenance-pipeline
cd provenance-pipeline/services/python-pipeline

# 2. Create and activate a virtual environment
python -m venv .venv
source .venv/bin/activate          # macOS / Linux
# .venv\Scripts\activate           # Windows

# 3. Install the package with dev dependencies
pip install -e ".[dev]"

# 4. Copy the environment template and fill in your keys
cp .env.example .env
$EDITOR .env
```

---

## Configuration

All configuration is read from a `.env` file (loaded by `pydantic-settings`).
Copy `.env.example` to `.env` and set the keys you need:

```dotenv
# ── Required ──────────────────────────────────────────────────────────────────
ANTHROPIC_API_KEY=sk-ant-...

# ── Scraper backend (default: basic — no paid credentials required) ────────────
# Set to "brightdata" on hackathon day to unlock premium scraping.
SCRAPER_BACKEND=basic
# BRIGHTDATA_API_KEY=...
# BRIGHTDATA_BUDGET_USD=75.0          # hard spend cap — never exceed this

# ── AI Detection APIs (optional — pipeline degrades gracefully without them) ───
# GPTZERO_API_KEY=...
# SAPLING_API_KEY=...

# ── Telegram ingestion (optional) ─────────────────────────────────────────────
# TELEGRAM_API_ID=...
# TELEGRAM_API_HASH=...
# TELEGRAM_CHANNELS=channel_handle_1,channel_handle_2

# ── RSS ingestion (optional) ──────────────────────────────────────────────────
# RSS_FEEDS=https://feeds.bbci.co.uk/news/rss.xml,https://rss.nytimes.com/services/xml/rss/nyt/HomePage.xml

# ── Tuning knobs (sensible defaults shown) ────────────────────────────────────
# LLM_MODEL=claude-sonnet-4-6
# PERMUTATION_COUNT=100
# MAX_SCRAPE_RESULTS=200
# SIMILARITY_THRESHOLD=0.45
# TOP_CANDIDATES=10
# SCRAPER_CONCURRENCY=10
# AI_DETECTION_THRESHOLD=0.65
```

---

## Running the Pipeline

### Analyse a single headline with the demo runner

```bash
python run_demo.py --headline "Prime Minister announces retirement"
```

### Exercise the CLI stub

```bash
python main.py --item "Prime Minister announces retirement"
```

Output is a `ProvenanceReport` written as JSON to `data/outputs/<report_id>.json` and
summarised in the terminal.

---

## Project Structure

```
provenance-pipeline/
├── .env.example                    # Template — copy to .env and fill keys
├── pyproject.toml                  # Dependencies, build system, ruff + mypy config
├── README.md                       # ← you are here
│
├── docs/
│   ├── ARCHITECTURE.md             # Deep-dive technical specification
│
├── src/
│   ├── agents/                     # Pipeline agents
│   ├── models/                     # Pydantic v2 contracts
│   ├── pipeline/                   # Queue wiring
│   ├── scraper/                    # Batch scraping layer
│   ├── scrapers/                   # Scraper ABCs/backends
│   └── utils/
│
├── tests/
│   ├── conftest.py                 # Shared fixtures (mock scraper, sample NewsItem)
│   └── scrapers/
├── test_*.py                       # Standalone agent smoke tests
├── main.py                         # CLI stub
├── run_demo.py                     # End-to-end demo runner
│
└── data/
    ├── inputs/                     # Optional: pre-saved NewsItem JSON files
    └── outputs/                    # ProvenanceReport JSON files written here
```

---

## Team / Module Owners

| Stage | Key Files | Owner |
|-------|-----------|-------|
| 1 — Ingestion | `agents/ingestion_agent.py` | — |
| 2 — Semantic Permutations | `agents/semantic_agent.py` | — |
| 3 — Broad Scraper | `scrapers/`, `agents/broad_scraper_agent.py` | — |
| 4 — Similarity Analysis | `agents/similarity_analyzer_agent.py` | — |
| 5 — AI Detection | `agents/ai_signature_detector_agent.py` | — |
| Infrastructure | `models/`, `utils/`, `pipeline/runner.py` | — |

Fill in owner names on hackathon morning so teammates know who to unblock on each module.

---

## Contributing

- **One Pydantic model per stage boundary** — never pass raw dicts between agents.
- **`ScraperFactory` only** — never import `BasicWebScraper` or `BrightDataScraper` directly
  in agent code.
- **No blocking I/O in async functions** — use `asyncio.get_event_loop().run_in_executor()`
  for sync libraries.
- **Rate-limit everything** — wrap all LLM calls with `LLMRateLimiter`; wrap all HTTP calls
  with `DomainRateLimiter`.
- **Tests mock the network** — no live HTTP or LLM calls in `pytest`.

See [ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full technical specification.

---

## License

MIT — see `LICENSE`.
