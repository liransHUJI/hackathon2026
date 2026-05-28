# Architecture: Provenance Pipeline

## System Overview

The Provenance Pipeline is a five-stage async Python system that traces any news headline back
to its earliest online occurrence and then examines those earliest sources for AI-generation
signatures. It is designed for hackathon-day velocity while encoding production-grade patterns:
Pydantic v2 data contracts at every stage boundary, the Strategy Pattern for swappable scraper
backends, and `asyncio.Queue` for backpressured inter-stage communication.

The system answers two questions about any news item:
1. **Provenance** — Where did this story first appear, and how did it propagate?
2. **Authenticity** — Are the earliest sources machine-generated text?

---

## Pipeline Flow Diagram

```
                           PROVENANCE PIPELINE
 ─────────────────────────────────────────────────────────────────────────────

  External Sources
  ┌─────────────────────────────────────┐
  │  Telegram Channel  │  RSS Feed      │
  └──────────────┬──────────────────────┘
                 │
                 ▼
  ┌──────────────────────────────┐
  │     1. IngestionAgent        │  Produces: NewsItem
  │  feedparser / telethon       │
  └──────────────┬───────────────┘
                 │
           ingest_q (maxsize=100)
                 │
                 ▼
  ┌──────────────────────────────┐
  │     2. SemanticAgent         │  Consumes: NewsItem
  │  Anthropic API (Claude)      │  Produces: PermutationSet (~100 variants)
  └──────────────┬───────────────┘
                 │
           semantic_q (maxsize=50) ◄── backpressure before expensive scrape
                 │
                 ▼
  ┌──────────────────────────────┐
  │   3. BroadScraperAgent       │  Consumes: PermutationSet
  │  Scraper (injected via       │  Produces: ScrapedResultSet
  │  ScraperFactory)             │
  │  asyncio.Semaphore(10)       │
  └──────────────┬───────────────┘
                 │
            scrape_q (maxsize=20)
                 │
                 ▼
  ┌──────────────────────────────┐
  │ 4. ChronologicalSimilarity   │  Consumes: ScrapedResultSet
  │     AnalyzerAgent            │  Produces: AnalyzedSet (top-10 candidates)
  │  sentence-transformers       │
  └──────────────┬───────────────┘
                 │
           analyze_q (maxsize=20)
                 │
                 ▼
  ┌──────────────────────────────┐
  │ 5. AISignatureDetectorAgent  │  Consumes: AnalyzedSet
  │  GPTZero + Sapling +         │  Produces: ProvenanceReport
  │  Statistical + LLM-Judge     │
  └──────────────┬───────────────┘
                 │
            report_q (unbounded)
                 │
                 ▼
            ProvenanceReport → data/outputs/<report_id>.json

 ─────────────────────────────────────────────────────────────────────────────
```

---

## Data Models

All models live in `src/provenance/models/`. They use **Pydantic v2** with strict typing.
Every inter-stage data transfer uses these models — never raw `dict`.

### NewsItem
*Stage 1 output / Stage 2 input*

```python
class NewsSource(StrEnum):
    TELEGRAM = "telegram"
    RSS      = "rss"
    MANUAL   = "manual"

class NewsItem(BaseModel):
    item_id:        str                 # UUID4 assigned at ingestion
    headline:       str                 # Raw headline text
    body:           Optional[str]       # Full article body if available
    url:            Optional[HttpUrl]
    source_channel: str                 # Feed URL or Telegram channel handle
    source_type:    NewsSource
    published_at:   datetime            # UTC
    ingested_at:    datetime            # Field(default_factory=datetime.utcnow)
    language:       str                 # default "en"
    raw_metadata:   dict                # Unstructured source-specific fields
```

### Permutation + PermutationSet
*Stage 2 output / Stage 3 input*

```python
class Permutation(BaseModel):
    text:       str
    strategy:   str    # "synonym" | "passive_voice" | "paraphrase" | "entity_swap" | …
    confidence: float  # Field(ge=0.0, le=1.0, default=1.0)

class PermutationSet(BaseModel):
    source_item:    NewsItem
    original_query: str             # Canonicalised search string from the headline
    permutations:   list[Permutation]
    model_used:     str             # e.g. "claude-sonnet-4-6"
    generated_at:   datetime
    total_count:    int
```

### ScrapedResult + ScrapedResultSet
*Stage 3 output / Stage 4 input*

```python
class ContentType(StrEnum):
    ARTICLE     = "article"
    SOCIAL_POST = "social_post"
    FORUM       = "forum"
    UNKNOWN     = "unknown"

class ScrapedResult(BaseModel):
    result_id:    str
    url:          HttpUrl
    title:        Optional[str]
    snippet:      str              # Short excerpt
    full_text:    Optional[str]    # Populated by fetch_content(); max 8 000 chars
    domain:       str
    content_type: ContentType      # default UNKNOWN
    published_at: Optional[datetime]
    scraped_at:   datetime
    query_used:   str              # Which permutation found this result
    scraper_id:   str              # "basic" or "brightdata"
    http_status:  Optional[int]
    error:        Optional[str]    # Non-None means the fetch failed; result is partial

class ScrapedResultSet(BaseModel):
    source_permutation_set: PermutationSet
    results:                list[ScrapedResult]
    total_queries_issued:   int
    total_results_raw:      int
    deduplication_removed:  int    # default 0
    scrape_duration_seconds: float
```

### RankedResult + AnalyzedSet
*Stage 4 output / Stage 5 input*

```python
class RankedResult(BaseModel):
    scraped_result:     ScrapedResult
    similarity_score:   float   # Field(ge=0.0, le=1.0) — cosine/BM25 vs original headline
    chronological_rank: int     # 1 = earliest found across all results
    composite_score:    float   # 0.6×similarity + 0.4×recency_weight
    is_likely_original: bool    # default False; set True for the overall earliest

class AnalyzedSet(BaseModel):
    source_result_set:        ScrapedResultSet
    ranked_results:           list[RankedResult]      # all results, sorted by composite_score
    top_candidates:           list[RankedResult]      # top 10 → passed to Stage 5
    analysis_duration_seconds: float
    similarity_model:         str    # e.g. "sentence-transformers/all-MiniLM-L6-v2"
```

### DetectionMethod + AISignatureResult + ProvenanceReport
*Stage 5 output — the final artefact*

```python
class DetectionMethod(BaseModel):
    method_name:  str                  # "gptzero" | "sapling" | "statistical" | "llm_judge"
    score:        Optional[float]      # 0.0–1.0; None if method did not run
    label:        Optional[str]        # "AI" | "HUMAN" | "UNCERTAIN"
    raw_response: dict                 # Full API/analysis response for auditability
    error:        Optional[str]        # Non-None → method failed; excluded from ensemble

class AISignatureResult(BaseModel):
    ranked_result:     RankedResult
    detection_methods: list[DetectionMethod]
    ensemble_score:    float   # Field(ge=0.0, le=1.0)
    is_ai_generated:   bool
    confidence:        float   # Field(ge=0.0, le=1.0)
    explanation:       str     # Human-readable narrative

class ProvenanceReport(BaseModel):
    report_id:              str
    source_item:            NewsItem
    ai_signature_results:   list[AISignatureResult]
    earliest_source:        Optional[RankedResult]
    disinformation_risk:    float   # Field(ge=0.0, le=1.0) — pipeline-level score
    risk_label:             str     # "LOW" | "MEDIUM" | "HIGH" | "CRITICAL"
    pipeline_version:       str
    generated_at:           datetime
    total_duration_seconds: float
    summary:                str     # One-paragraph human-readable narrative
```

---

## Agent Specifications

All agents inherit from `BaseAgent` in `src/provenance/agents/base_agent.py`.

### BaseAgent (abstract)

```python
class BaseAgent(ABC):
    def __init__(
        self,
        name:         str,
        input_queue:  Optional[asyncio.Queue] = None,
        output_queue: Optional[asyncio.Queue] = None,
        config:       Optional[PipelineConfig] = None,
    ): ...

    @abstractmethod
    async def process(self, item: Any) -> Any:
        """Core transformation. Must work without queues (enables unit testing)."""
        ...

    async def run(self) -> None:
        """Queue-driven loop. Stops on sentinel None. Forwards sentinel downstream."""
        ...

    async def stop(self) -> None: ...
```

`run()` catches per-item exceptions and continues processing (non-fatal); it logs with
`exc_info=True` before moving on. Fatal errors (e.g., misconfiguration) should be raised
in `__init__`, not swallowed in `run()`.

---

### 1. IngestionAgent
`src/provenance/agents/ingestion_agent.py`

| | |
|-|-|
| **Purpose** | Poll Telegram channels or RSS feeds; emit one `NewsItem` per story |
| **Input** | External source (no input queue — this is a producer) |
| **Output** | `NewsItem` → `ingest_q` |
| **Libraries** | `feedparser`, `telethon`, `python-dateutil` |

**Key methods:**
```python
async def process(item: RawFeedEntry) -> NewsItem
async def poll_rss(feed_url: str) -> AsyncIterator[NewsItem]
async def poll_telegram(channel: str) -> AsyncIterator[NewsItem]
async def run_source(source_config: SourceConfig) -> None   # top-level driver
```

**Failure behaviour:**
- RSS parse errors: skip item, log warning, continue polling
- Telegram `FloodWaitError`: sleep for `flood_wait.seconds + 5`, then retry
- Duplicate detection: maintain an in-memory `seen_ids: set[str]`; skip seen items

---

### 2. SemanticAgent
`src/provenance/agents/semantic_agent.py`

| | |
|-|-|
| **Purpose** | Generate ~100 semantic permutations of a headline using an LLM |
| **Input** | `NewsItem` |
| **Output** | `PermutationSet` |
| **Libraries** | `anthropic` SDK |
| **Rate limiting** | `LLMRateLimiter(rpm=50, tpm=60_000)` |

**Key methods:**
```python
async def process(item: NewsItem) -> PermutationSet
async def _call_llm(headline: str, n: int) -> list[Permutation]
def _build_prompt(headline: str, n: int) -> str
def _parse_permutations(raw: str) -> list[Permutation]
```

**Prompt strategy:**
Single structured call requesting a JSON array. Temperature `0.9` for lexical diversity.
Requested strategies: synonym replacement, passive/active voice swap, entity generalisation
(e.g., "Prime Minister" → "head of government"), temporal paraphrasing ("yesterday" →
"on Monday"), cross-domain reframing, and negation variants.

**Failure behaviour:**
- LLM API error after 3 retries with backoff: emit a `PermutationSet` containing only the
  original headline as a single permutation (graceful degradation, not halt).

---

### 3. BroadScraperAgent
`src/provenance/agents/broad_scraper_agent.py`

| | |
|-|-|
| **Purpose** | Search all permutations across the web; return deduplicated results |
| **Input** | `PermutationSet` |
| **Output** | `ScrapedResultSet` |
| **Concurrency** | `asyncio.Semaphore(config.scraper_concurrency)` — default 10 |
| **Rate limiting** | `DomainRateLimiter(default_rps=2.0)` per domain |

**Key methods:**
```python
async def process(item: PermutationSet) -> ScrapedResultSet
async def _search_permutation(perm: Permutation) -> list[ScrapedResult]
async def _fetch_content_batch(results: list[ScrapedResult]) -> list[ScrapedResult]
def _deduplicate(results: list[ScrapedResult]) -> list[ScrapedResult]
```

**Deduplication:** normalise URLs (strip UTM params, canonicalise scheme), then deduplicate
by normalised URL. Keep the result with the most complete data.

**Failure behaviour:**
- Per-permutation network error: log + continue; partial `ScrapedResultSet` emitted.
- If total results after dedup < `config.minimum_results_threshold` (default 5):
  log a warning and proceed anyway (downstream stages handle sparse data).

---

### 4. ChronologicalSimilarityAnalyzer
`src/provenance/agents/similarity_analyzer_agent.py`

| | |
|-|-|
| **Purpose** | Sort by date, score semantic similarity, filter false positives, select top 10 |
| **Input** | `ScrapedResultSet` |
| **Output** | `AnalyzedSet` |
| **Libraries** | `sentence-transformers`, `numpy`, `scipy` |

**Five-phase process:**
1. **Chronological sort** — `published_at` ASC, nulls last
2. **Embedding** — encode original headline + each result's `title + " " + snippet`
   using `all-MiniLM-L6-v2` (loaded once in `__init__`)
3. **Cosine similarity** — filter out results with `similarity_score < 0.45`
4. **Composite score** — `0.6 × similarity_score + 0.4 × recency_weight`
   where `recency_weight` decays linearly from 1.0 (oldest) to 0.0 (newest in set)
5. **Select top 10** — sorted by `composite_score` descending

**Key methods:**
```python
async def process(item: ScrapedResultSet) -> AnalyzedSet
def _embed(texts: list[str]) -> np.ndarray
def _cosine_similarity(a: np.ndarray, b: np.ndarray) -> float
def _recency_weight(published_at: datetime, oldest: datetime, newest: datetime) -> float
def _filter_false_positives(results: list[RankedResult]) -> list[RankedResult]
```

**Important:** load the sentence-transformers model in `__init__`, **not** in `process()`.
Model load takes ~2 seconds; calling it per-item would destroy throughput.

---

### 5. AISignatureDetectorAgent
`src/provenance/agents/ai_signature_detector_agent.py`

| | |
|-|-|
| **Purpose** | Analyse each top candidate for AI-generated text signatures |
| **Input** | `AnalyzedSet` |
| **Output** | `ProvenanceReport` |

**Key methods:**
```python
async def process(item: AnalyzedSet) -> ProvenanceReport
async def _detect_one(ranked_result: RankedResult) -> AISignatureResult
async def _run_all_methods(text: str) -> list[DetectionMethod]
def _compute_ensemble(methods: list[DetectionMethod]) -> tuple[float, bool, float]
def _build_report(item: AnalyzedSet, results: list[AISignatureResult]) -> ProvenanceReport
```

Runs all 4 detection methods **in parallel** per candidate via `asyncio.gather`.
See the [AI Detection Methods](#ai-detection-methods) section for full details.

---

## Scraper Strategy Pattern

### Abstract Interface
`src/provenance/scrapers/base_scraper.py`

```python
class Scraper(ABC):
    @abstractmethod
    async def search(
        self,
        query:       str,
        max_results: int = 10,
        date_from:   Optional[str] = None,    # ISO date string "2024-01-15"
        site_filter: Optional[str] = None,    # e.g. "reddit.com"
    ) -> list[ScrapedResult]: ...

    @abstractmethod
    async def fetch_content(
        self,
        url:       str,
        timeout_s: int = 15,
    ) -> Optional[str]: ...
    # Returns None on failure — never raises.

    @property
    @abstractmethod
    def scraper_id(self) -> str: ...
```

### BasicWebScraper (MVP — no paid credentials)
`src/provenance/scrapers/basic_web_scraper.py`

```
search():
  - duckduckgo_search.DDGS().text() for general queries
  - DDGS().news() for time-filtered queries
  - Both are synchronous; wrapped with loop.run_in_executor(None, ...)
  - Maps DDGS result dicts → ScrapedResult instances

fetch_content():
  - httpx.AsyncClient (shared instance, connection pooling)
  - BeautifulSoup("html.parser") to extract <article> or <main> text
  - Strips <nav>, <footer>, <script>, <style> before text extraction
  - Truncates to max 8 000 characters (LLM context budget)

scraper_id = "basic"
```

### BrightDataScraper (Hackathon-day drop-in)
`src/provenance/scrapers/brightdata_scraper.py`

```
search():
  - Bright Data SERP API: POST https://api.brightdata.com/api/v1/search
  - Auth: Bearer token from BRIGHTDATA_API_KEY
  - Returns structured results with reliable published_at dates
  - Handles paywalls, bot-detection, and social media natively

fetch_content():
  - Bright Data Web Unlocker: POST https://api.brightdata.com/api/v1/request
  - JavaScript rendering + CAPTCHA bypass included

Budget guard:
  - Tracks self._spent_usd against BRIGHTDATA_BUDGET_USD
  - Raises BudgetExceededError before issuing any new request
  - NEVER bypass this guard

scraper_id = "brightdata"
```

### ScraperFactory
`src/provenance/scrapers/factory.py`

```python
class ScraperBackend(StrEnum):
    BASIC      = "basic"
    BRIGHTDATA = "brightdata"

class ScraperFactory:
    @staticmethod
    def create(config: PipelineConfig) -> Scraper:
        """Read config.scraper_backend; return the appropriate implementation."""
        ...

    @staticmethod
    def inject(scraper: Scraper) -> Scraper:
        """For tests: accept any Scraper-conforming object (mock, stub, etc.)."""
        return scraper
```

**Rule:** `ScraperFactory.create(config)` is the **only** legal way to obtain a `Scraper`
in production code. Direct imports of concrete scrapers inside agent code are forbidden.

---

## Pipeline Runner

`src/provenance/pipeline/runner.py`

```python
class PipelineRunner:
    """
    Wires all five agents with asyncio.Queues.

    Two run modes:
      run_once(news_item) → ProvenanceReport
        Inject a single item; await full pipeline; return report.
      run_continuous() → AsyncIterator[ProvenanceReport]
        Start all agents + source pollers; yield reports as they arrive.
    """

    def __init__(self, config: PipelineConfig, scraper: Optional[Scraper] = None):
        self._scraper = scraper or ScraperFactory.create(config)
        self._build_queues()
        self._build_agents()

    def _build_queues(self):
        self.ingest_q   = asyncio.Queue(maxsize=100)
        self.semantic_q = asyncio.Queue(maxsize=50)   # backpressure
        self.scrape_q   = asyncio.Queue(maxsize=20)
        self.analyze_q  = asyncio.Queue(maxsize=20)
        self.report_q   = asyncio.Queue()             # unbounded — caller drains this

    async def run_once(self, news_item: NewsItem) -> ProvenanceReport: ...
    async def run_continuous(self) -> AsyncIterator[ProvenanceReport]: ...
```

`runner.py` contains **only** queue wiring and agent lifecycle management.
Business logic belongs in the relevant agent, not here.

---

## Directory Structure

```
provenance-pipeline/
├── .env.example                         # Env var template
├── .env                                 # Gitignored — actual credentials
├── .gitignore
├── pyproject.toml                       # Build system, deps, ruff + mypy config
├── README.md
├── CLAUDE.md                            # Claude Code session instructions
│
├── docs/
│   ├── ARCHITECTURE.md                  # ← this document
│   ├── BRIGHTDATA_SETUP.md              # Step-by-step Bright Data activation guide
│   └── AI_DETECTION_METHODS.md          # Detailed notes on detection approaches
│
├── src/
│   └── provenance/
│       ├── __init__.py
│       ├── config.py                    # PipelineConfig (pydantic-settings)
│       │
│       ├── models/
│       │   ├── __init__.py
│       │   ├── news_item.py             # NewsItem, NewsSource
│       │   ├── permutation_set.py       # Permutation, PermutationSet
│       │   ├── scraped_result.py        # ScrapedResult, ScrapedResultSet, ContentType
│       │   ├── ranked_result.py         # RankedResult, AnalyzedSet
│       │   └── provenance_report.py     # DetectionMethod, AISignatureResult, ProvenanceReport
│       │
│       ├── agents/
│       │   ├── __init__.py
│       │   ├── base_agent.py            # BaseAgent ABC + asyncio queue loop
│       │   ├── ingestion_agent.py       # Stage 1
│       │   ├── semantic_agent.py        # Stage 2
│       │   ├── broad_scraper_agent.py   # Stage 3
│       │   ├── similarity_analyzer_agent.py  # Stage 4
│       │   └── ai_signature_detector_agent.py  # Stage 5
│       │
│       ├── scrapers/
│       │   ├── __init__.py
│       │   ├── base_scraper.py          # Scraper ABC
│       │   ├── basic_web_scraper.py     # DuckDuckGo + httpx + BeautifulSoup
│       │   ├── brightdata_scraper.py    # Bright Data SERP + Web Unlocker
│       │   └── factory.py              # ScraperFactory, ScraperBackend
│       │
│       ├── pipeline/
│       │   ├── __init__.py
│       │   └── runner.py               # PipelineRunner
│       │
│       └── utils/
│           ├── __init__.py
│           ├── rate_limiter.py          # TokenBucket, DomainRateLimiter, LLMRateLimiter
│           ├── retry.py                 # @retry_with_backoff decorator
│           ├── text_utils.py            # URL normalisation, text cleaning
│           └── logging_config.py        # Structured JSON logging (structlog)
│
├── tests/
│   ├── conftest.py                      # Fixtures: mock_scraper, sample_news_item, test_config
│   ├── agents/
│   │   ├── test_ingestion_agent.py
│   │   ├── test_semantic_agent.py
│   │   ├── test_broad_scraper_agent.py
│   │   ├── test_similarity_analyzer_agent.py
│   │   └── test_ai_signature_detector_agent.py
│   ├── scrapers/
│   │   ├── test_basic_web_scraper.py
│   │   └── test_brightdata_scraper.py
│   ├── models/
│   │   └── test_model_validation.py
│   ├── pipeline/
│   │   └── test_runner.py
│   └── utils/
│       ├── test_rate_limiter.py
│       └── test_retry.py
│
├── scripts/
│   ├── run_pipeline.py                  # CLI entry point (argparse)
│   └── test_single_item.py              # Smoke test with a hardcoded NewsItem
│
└── data/
    ├── inputs/                          # Pre-saved NewsItem JSON files (optional)
    └── outputs/                         # ProvenanceReport JSON files
```

---

## Utility Layer

### `utils/rate_limiter.py`

**`TokenBucket`** — async token bucket with `asyncio.Lock`. `acquire(n)` sleeps if depleted.

**`DomainRateLimiter`** — per-domain `TokenBucket` map. Call `await limiter.acquire(domain)`
before every HTTP request. Default: 2 req/s, burst of 5.

**`LLMRateLimiter`** — two-dimensional bucket (requests per minute + tokens per minute).
Call `await limiter.acquire(estimated_tokens)` before every Anthropic API call.

### `utils/retry.py`

**`@retry_with_backoff`** — decorator for `async` functions. Exponential backoff with jitter.

```python
@retry_with_backoff(
    max_attempts=3,
    base_delay=1.0,
    max_delay=60.0,
    retryable_exceptions=(httpx.HTTPError, anthropic.APIError),
)
async def call_external_api(...): ...
```

### `utils/text_utils.py`

- `normalise_url(url: str) -> str` — strip UTM params, normalise scheme
- `clean_html_text(html: str) -> str` — strip tags, collapse whitespace
- `extract_domain(url: str) -> str` — `"https://www.nytimes.com/..."` → `"nytimes.com"`
- `truncate_to_tokens(text: str, max_chars: int = 8_000) -> str`

### `utils/logging_config.py`

Configures `structlog` with JSON output in production and coloured console output in dev.
Every agent initialises its logger as:
```python
self.logger = structlog.get_logger(agent_name=self.name)
```

---

## AI Detection Methods

All four methods run **in parallel** for each of the top-10 candidates.

### Method 1 — GPTZero API
```
Endpoint : POST https://api.gptzero.me/v2/predict/text
Auth     : X-Api-Key header (env: GPTZERO_API_KEY)
Free tier: 10 000 words/month
Input    : {"document": full_text}
Score    : response["completely_generated_prob"]  (float 0.0–1.0)
Weight   : 0.35
Timeout  : 10 s; retries: 2
Fallback : error set in DetectionMethod; excluded from ensemble
```

### Method 2 — Sapling AI Detector
```
Endpoint : POST https://api.sapling.ai/api/v1/aidetect
Auth     : "key" field in body (env: SAPLING_API_KEY)
Input    : {"key": ..., "text": full_text}
Score    : response["score"]  (1.0 = AI, 0.0 = human)
Weight   : 0.25
Timeout  : 10 s; retries: 2
Fallback : same as GPTZero
```

### Method 3 — Statistical / Linguistic Analysis (local, no external calls)
`src/provenance/utils/linguistic_analysis.py`

Five features combined into a composite score:

| Feature | AI Signal |
|---------|-----------|
| Sentence length variance (σ²) | Low variance |
| Burstiness B = (σ−μ)/(σ+μ) | Low burstiness (uniform rhythm) |
| Type-Token Ratio | Extreme values (very high or very low) |
| Transition word density | > 3 per 100 words ("Furthermore", "Moreover", …) |
| Paragraph length uniformity | Low standard deviation |

Libraries: `spacy` (tokenisation); `numpy` for statistics.
Weight: 0.20 — never fails; always contributes to ensemble.

### Method 4 — LLM Self-Evaluation (Claude as Judge)
```
Model      : claude-haiku-4-5 (cheapest; consistent at temp=0.1)
Auth       : shared ANTHROPIC_API_KEY
Temperature: 0.1
```

Prompt asks Claude to score five dimensions (0.0–1.0) and return structured JSON:
```json
{
  "scores": {
    "lexical_uniformity": 0.0,
    "structural_regularity": 0.0,
    "hedging_patterns": 0.0,
    "factual_confidence": 0.0,
    "originality": 0.0
  },
  "overall": 0.0,
  "reasoning": "..."
}
```
Score used: `response["overall"]`. Weight: 0.20.

### Ensemble Aggregation

```python
# Weights
WEIGHTS = {"gptzero": 0.35, "sapling": 0.25, "statistical": 0.20, "llm_judge": 0.20}

# Only successful methods (error is None) contribute
successful = [m for m in methods if m.error is None and m.score is not None]
total_w    = sum(WEIGHTS[m.method_name] for m in successful)
score      = sum((WEIGHTS[m.method_name] / total_w) * m.score for m in successful)

# Confidence scales with number of successful methods
confidence = min(0.5 + 0.17 * len(successful), 1.0)
# 1 method → 0.67;  2 → 0.84;  3 → 1.0;  0 → 0.5 (minimum)

is_ai_generated = score >= config.ai_detection_threshold  # default 0.65
```

---

## Configuration

`src/provenance/config.py` — `PipelineConfig(BaseSettings)` reads from `.env`.

| Env Var | Type | Default | Description |
|---------|------|---------|-------------|
| `ANTHROPIC_API_KEY` | str | — | **Required** |
| `LLM_MODEL` | str | `claude-sonnet-4-6` | Semantic Agent model |
| `LLM_MAX_TOKENS` | int | `4096` | |
| `LLM_TEMPERATURE` | float | `0.9` | |
| `LLM_REQUESTS_PER_MINUTE` | int | `50` | |
| `LLM_TOKENS_PER_MINUTE` | int | `60000` | |
| `SCRAPER_BACKEND` | str | `basic` | `"basic"` or `"brightdata"` |
| `BRIGHTDATA_API_KEY` | str | None | Required if backend=brightdata |
| `BRIGHTDATA_BUDGET_USD` | float | `75.0` | Hard spend cap |
| `PERMUTATION_COUNT` | int | `100` | Target permutations per headline |
| `MAX_SCRAPE_RESULTS` | int | `200` | Max results before dedup |
| `SIMILARITY_THRESHOLD` | float | `0.45` | Min cosine similarity to pass filter |
| `TOP_CANDIDATES` | int | `10` | Items passed to AI detector |
| `SCRAPER_CONCURRENCY` | int | `10` | Simultaneous scrape tasks |
| `DOMAIN_RATE_LIMIT_RPS` | float | `2.0` | Per-domain request rate |
| `RSS_FEEDS` | list[str] | `[]` | Comma-separated feed URLs |
| `TELEGRAM_CHANNELS` | list[str] | `[]` | Comma-separated channel handles |
| `TELEGRAM_API_ID` | str | None | From my.telegram.org |
| `TELEGRAM_API_HASH` | str | None | From my.telegram.org |
| `EMBEDDING_MODEL` | str | `all-MiniLM-L6-v2` | sentence-transformers model |
| `GPTZERO_API_KEY` | str | None | Optional |
| `SAPLING_API_KEY` | str | None | Optional |
| `AI_DETECTION_THRESHOLD` | float | `0.65` | Ensemble threshold for is_ai_generated |

---

## Key Libraries

| Library | Version | Role |
|---------|---------|------|
| `anthropic` | ≥ 0.40.0 | LLM calls (Semantic Agent + LLM Judge) |
| `pydantic` | ≥ 2.0 | Data contracts |
| `pydantic-settings` | ≥ 2.0 | `.env` → `PipelineConfig` |
| `httpx` | ≥ 0.27.0 | Async HTTP (scraping, API calls) |
| `beautifulsoup4` | ≥ 4.12 | HTML content extraction |
| `duckduckgo-search` | ≥ 6.0 | Free search API (BasicWebScraper) |
| `sentence-transformers` | ≥ 3.0 | Semantic similarity embeddings |
| `numpy` | ≥ 1.26 | Vector math |
| `scipy` | ≥ 1.13 | Cosine distance |
| `feedparser` | ≥ 6.0 | RSS ingestion |
| `telethon` | ≥ 1.36 | Telegram ingestion |
| `python-dateutil` | ≥ 2.9 | Datetime parsing |
| `spacy` | ≥ 3.7 | Tokenisation for statistical analysis |
| `structlog` | ≥ 24.0 | Structured JSON logging |
| `pytest` | ≥ 8.0 | Test runner |
| `pytest-asyncio` | ≥ 0.23 | Async test support |
| `pytest-mock` | ≥ 3.12 | Mock fixtures |
| `ruff` | latest | Linting + formatting |
| `mypy` | latest | Static type checking |

---

## Testing Strategy

**Principle:** every agent is independently testable via `process()` without queues.

```python
# Pattern for all agent unit tests
@pytest.mark.asyncio
async def test_semantic_agent_returns_permutation_set(mock_anthropic_client, sample_news_item):
    agent = SemanticAgent(name="test", config=test_config())
    agent._client = mock_anthropic_client          # inject mock — no live API call
    result = await agent.process(sample_news_item) # direct call, no queue
    assert isinstance(result, PermutationSet)
    assert result.total_count > 0
    assert result.source_item.item_id == sample_news_item.item_id
```

**Scraper tests** use `httpx.MockTransport` to return recorded HTTP fixtures.
**BrightDataScraper tests** stub the Bright Data API with fixture responses.

**`conftest.py` shared fixtures:**
- `sample_news_item() -> NewsItem` — a deterministic test item
- `mock_scraper() -> Scraper` — an `AsyncMock`-backed `Scraper` stub
- `test_config() -> PipelineConfig` — minimal config with no real API keys
- `mock_anthropic_client` — `AsyncMock` for the Anthropic SDK client

---

## Implementation Order (Hackathon Day)

```
Hour 0–1   models/
           Define all 10 Pydantic models. Write test_model_validation.py.
           Goal: all models instantiate correctly; field constraints enforced.

Hour 1–2   utils/
           rate_limiter.py + retry.py — foundational; every subsequent module needs these.
           Goal: unit tests pass for TokenBucket, DomainRateLimiter, retry decorator.

Hour 2–3   scrapers/
           base_scraper.py → basic_web_scraper.py → brightdata_scraper.py (stub) → factory.py
           Goal: BasicWebScraper can search DuckDuckGo and fetch a URL; tests mock HTTP.

Hour 3–4   agents/semantic_agent.py
           Build LLM permutation generation. Mock the Anthropic client in tests.
           Goal: process(NewsItem) → PermutationSet with ~100 items.

Hour 4–5   agents/broad_scraper_agent.py
           Wire Semaphore + DomainRateLimiter + injected scraper + deduplication.
           Goal: process(PermutationSet) → ScrapedResultSet; mock scraper in tests.

Hour 5–6   agents/similarity_analyzer_agent.py
           Load sentence-transformers model in __init__; implement 5-phase ranking.
           Goal: process(ScrapedResultSet) → AnalyzedSet with top 10 candidates.

Hour 6–7   agents/ai_signature_detector_agent.py
           Implement all 4 detection methods + ensemble; mock external APIs in tests.
           Goal: process(AnalyzedSet) → ProvenanceReport with risk_label.

Hour 7–8   pipeline/runner.py + scripts/run_pipeline.py
           Wire everything; end-to-end smoke test with a real headline.
           Goal: full run produces a ProvenanceReport JSON written to data/outputs/.

Hour 8+    (If budget allows)
           Set SCRAPER_BACKEND=brightdata in .env; re-run; validate BrightDataScraper
           returns richer results without any code changes to agents.
```

---

## Bright Data Integration

Bright Data is the **hackathon-day upgrade**. The default `BasicWebScraper` uses free
DuckDuckGo search and standard HTTP fetching. When the premium scraper is needed:

**Activation (zero code changes):**
```bash
export SCRAPER_BACKEND=brightdata
export BRIGHTDATA_API_KEY=<key from https://brightdata.com/cp/zones>
python scripts/run_pipeline.py --item "..."
```

**Why it's better on hackathon day:**
- Bypasses paywalls, bot-detection, and JavaScript-heavy pages
- Structured SERP results with reliable `published_at` dates
- Social media content (Reddit, Twitter/X) accessible without auth
- Handles rate limits on the Bright Data side — no DomainRateLimiter needed for fetches

**Budget management:**
- `BRIGHTDATA_BUDGET_USD=75.0` (default)
- `BrightDataScraper.__init__` tracks `self._spent_usd`
- Raises `BudgetExceededError` before any request that would exceed the cap
- Estimated cost per full pipeline run: ~$1.02 (73 runs within budget)
