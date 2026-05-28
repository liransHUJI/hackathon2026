# Claude Code Instructions — Provenance Pipeline

This file governs every Claude Code session in this repository.
Read it fully before touching any code. These rules are non-negotiable.

---

## Project Summary

**Provenance Pipeline** is a Python async system that:
1. Ingests a news headline (Telegram / RSS)
2. Uses an LLM to generate ~100 semantic permutations of the headline
3. Searches the web for all permutations simultaneously
4. Sorts findings chronologically and ranks by semantic similarity
5. Analyses the 10 earliest, most-relevant results for AI-generated text signatures

The goal is to detect AI-seeded disinformation by finding _where_ a story first appeared and
_whether_ it was machine-written. Context: university hackathon, "Trust & Safety in the AI Era".

---

## The Pipeline (quick reference)

```
Ingestion → Semantic → BroadScraper → SimilarityAnalyzer → AISignatureDetector
NewsItem  → PermutationSet → ScrapedResultSet → AnalyzedSet → ProvenanceReport
```

Each arrow is an `asyncio.Queue`. Each stage is a `BaseAgent` subclass.

---

## ABSOLUTE CONSTRAINTS

These must **never** be violated. If a request would violate one, say so and propose an
alternative that respects them.

### 1. Python only
No JavaScript, TypeScript, or shell scripts for pipeline logic.
Shell scripts are only acceptable for one-line dev-convenience wrappers (e.g., `start.sh`).

### 2. OOP with ABC abstractions
Every major component (`Scraper`, `BaseAgent`) must have an abstract base class.
Concrete implementations must inherit from those ABCs.
Mypy `--strict` must pass on all `src/` code.

### 3. Pydantic v2 models are the ONLY data contracts
Never pass raw `dict` or untyped data across agent boundaries.
Every stage input and output is a specific Pydantic model:

```
NewsItem → PermutationSet → ScrapedResultSet → AnalyzedSet → ProvenanceReport
```

When adding a new field, add it to the model — never tack it onto `raw_metadata` as a workaround.

### 4. Strategy Pattern for scrapers — THE CARDINAL RULE
**Agents must never import or instantiate `BasicWebScraper` or `BrightDataScraper` directly.**

The only legal patterns are:

```python
# In production code (PipelineRunner or top-level setup):
scraper = ScraperFactory.create(config)

# In tests:
scraper = ScraperFactory.inject(MockScraper())
```

`BroadScraperAgent` holds `self.scraper: Scraper` and calls only:
- `await self.scraper.search(query, max_results=...)`
- `await self.scraper.fetch_content(url)`

Never add `if isinstance(self.scraper, BrightDataScraper): ...` branches. That defeats the pattern.

### 5. asyncio throughout — no blocking I/O in async functions
Use `httpx.AsyncClient` (not `requests`).
For sync-only libraries (e.g., `duckduckgo_search`), wrap with:
```python
loop = asyncio.get_event_loop()
results = await loop.run_in_executor(None, sync_function, args)
```

### 6. Rate-limit every external call
- LLM calls (Anthropic SDK): wrap with `LLMRateLimiter.acquire(estimated_tokens)` before each call.
- HTTP calls: wrap with `DomainRateLimiter.acquire(domain)` before each request.
- Never call an external API inside a bare `for` loop without a rate limiter or semaphore.

### 7. Agents are independently testable
`BaseAgent.process(item)` must be callable without a queue:
```python
agent = SemanticAgent(name="test", config=test_config())
result = await agent.process(sample_news_item)   # no queue required
assert isinstance(result, PermutationSet)
```
This is enforced by the test suite. If `process()` breaks without queues, it's a bug.

---

## Coding Standards

- **Formatter / linter:** `ruff` — run `ruff check src/ tests/` and `ruff format src/ tests/`
- **Type checker:** `mypy --strict src/`
- **Line length:** 100 characters
- **Imports:** sorted by `ruff` (`isort` compatible); stdlib → third-party → local
- **Docstrings:** Google style for all public classes and methods
- **Logging:** use `structlog` (configured in `utils/logging_config.py`); every agent has
  `self.logger = structlog.get_logger(agent_name=self.name)`; never use bare `print()`
- **Error handling:** always log with `exc_info=True` before continuing; never silently swallow
- **Configuration:** all magic numbers go in `PipelineConfig` (read from `.env`);
  never hardcode thresholds, counts, or API endpoints in business logic

---

## Scraper Strategy Pattern — Extended Rules

### The interface (never change the signature)
```python
class Scraper(ABC):
    @abstractmethod
    async def search(
        self,
        query:       str,
        max_results: int = 10,
        date_from:   Optional[str] = None,
        site_filter: Optional[str] = None,
    ) -> list[ScrapedResult]: ...

    @abstractmethod
    async def fetch_content(self, url: str, timeout_s: int = 15) -> Optional[str]: ...

    @property
    @abstractmethod
    def scraper_id(self) -> str: ...
```

### Switching to Bright Data on hackathon day — ZERO code changes
```
1. Set SCRAPER_BACKEND=brightdata in .env
2. Set BRIGHTDATA_API_KEY=<key> in .env
3. Re-run the pipeline — ScraperFactory.create(config) returns BrightDataScraper automatically
```

### Adding a new scraper (e.g., SerperAPI)
1. Create `src/provenance/scrapers/serper_scraper.py`
2. Inherit from `Scraper(ABC)` and implement all abstract methods
3. Add `SERPER = "serper"` to `ScraperBackend` enum in `factory.py`
4. Add the `elif` branch in `ScraperFactory.create()`
5. Write tests in `tests/scrapers/test_serper_scraper.py`

---

## Data Contracts (Pydantic)

### Model hierarchy
```
NewsItem                      ← Stage 1 output / Stage 2 input
  └── PermutationSet          ← Stage 2 output / Stage 3 input
        └── ScrapedResultSet  ← Stage 3 output / Stage 4 input
              └── AnalyzedSet ← Stage 4 output / Stage 5 input
                    └── ProvenanceReport  ← final output
```

Each model carries its parent (e.g., `PermutationSet.source_item: NewsItem`) so you can always
trace a `ProvenanceReport` back to the original `NewsItem` without joining external state.

### Validation rules
- `similarity_score`, `confidence`, `ensemble_score`, `disinformation_risk`: all `float` in
  `[0.0, 1.0]` enforced by `Field(ge=0.0, le=1.0)`
- `published_at` is always `datetime` with timezone-awareness where possible; use
  `python-dateutil` for parsing; store in UTC
- URLs are `pydantic.HttpUrl`; never store bare strings for URLs in models

---

## Async & Queue Architecture

### Queue topology
```
ingest_q   maxsize=100   (IngestionAgent → SemanticAgent)
semantic_q maxsize=50    (SemanticAgent → BroadScraperAgent)  ← backpressure gate
scrape_q   maxsize=20    (BroadScraperAgent → SimilarityAnalyzer)
analyze_q  maxsize=20    (SimilarityAnalyzer → AISignatureDetector)
report_q   unbounded     (AISignatureDetector → caller)
```

`semantic_q` has `maxsize=50` to prevent the cheap LLM step from flooding the expensive
scraping step. Never increase this without profiling first.

### Sentinel protocol
Every agent's `run()` loop stops when it receives `None` from its input queue.
On receiving `None`, it forwards `None` to its output queue before exiting.
This propagates the shutdown signal through the entire pipeline.

### Concurrency within BroadScraperAgent
```python
sem = asyncio.Semaphore(config.scraper_concurrency)  # default 10
async with sem:
    results = await self.scraper.search(perm.text, max_results=10)
```
Never `asyncio.gather` unbounded permutations — always gate with the semaphore.

---

## Rate Limiting Requirements

### LLM (Anthropic)
```python
# In SemanticAgent.__init__:
self._llm_limiter = LLMRateLimiter(
    rpm=config.llm_requests_per_minute,   # default 50
    tpm=config.llm_tokens_per_minute,     # default 60_000
)

# Before every API call:
await self._llm_limiter.acquire(estimated_tokens=500)
response = await self._client.messages.create(...)
```

### HTTP (scrapers)
```python
# In BasicWebScraper.__init__:
self._rate_limiter = DomainRateLimiter(
    default_rps=config.domain_rate_limit_rps,  # default 2.0
    burst=5.0,
)

# Before every fetch:
await self._rate_limiter.acquire(domain)
response = await self._client.get(url)
```

### Retry policy
Use `@retry_with_backoff(max_attempts=3, base_delay=1.0)` from `utils/retry.py` on:
- All Anthropic SDK calls
- All `httpx` calls in scrapers
- Any external AI-detection API calls if a future free provider is added

---

## AI Detection Methods

Four methods run **in parallel** for each of the top-10 candidates.
Weights are renormalised over successful methods (errors are excluded, not zeroed).

| Method | Weight | External? | Fallback |
|--------|--------|-----------|----------|
| Statistical / Linguistic | 35% | No (local) | Never fails |
| Stylometric | 25% | No (local) | Never fails |
| Template / Repetition | 20% | No (local) | Never fails |
| LLM Self-Evaluation | 20% | Yes (Anthropic) | Excluded from ensemble |

**Ensemble threshold:** `is_ai_generated = ensemble_score >= AI_DETECTION_THRESHOLD` (default 0.65).
**Confidence cap:** if fewer than 2 methods succeed, `confidence` is capped at 0.5.

LLM Judge model: `claude-haiku-4-5` (cheapest, consistent at temp=0.1).
Do NOT use a larger model for the judge — volume makes cost prohibitive.

---

## Testing Expectations

- **No live network calls in tests.** Use `httpx.MockTransport`, `pytest-mock`, or recorded
  fixtures for all HTTP. Mock the Anthropic SDK client.
- Every agent has at least:
  - `test_process_returns_correct_model_type` — happy-path unit test
  - `test_process_handles_llm_error` / `test_process_handles_network_error` — error path
- Every scraper implementation has:
  - `test_search_returns_scraped_results`
  - `test_fetch_content_returns_none_on_error`
  - `test_scraper_id_is_correct`
- Run with: `pytest tests/ -v --asyncio-mode=auto`
- Coverage target: 80% on `src/` (enforced by CI).

---

## Bright Data Integration (Hackathon Day)

**Budget: $75 USD — this is a hard cap, not a guideline.**

```
Estimated cost per full pipeline run:
  100 permutations × 10 SERP results  = 1,000 results × $0.001 = $1.00
  10 full page fetches                              × $0.002 = $0.02
  ─────────────────────────────────────────────────────────────────
  Total per run: ~$1.02
  Runs within budget: ~73 (leave ~$2 buffer)
```

`BrightDataScraper` tracks cumulative spend in `self._spent_usd` and raises
`BudgetExceededError` before issuing any request that would exceed `BRIGHTDATA_BUDGET_USD`.
**Do NOT remove or bypass this guard.**

Activation steps (zero code changes):
```
1. export SCRAPER_BACKEND=brightdata
2. export BRIGHTDATA_API_KEY=<key from Bright Data dashboard>
3. python scripts/run_pipeline.py --item "..."
```

---

## File Organization

| What | Where |
|------|-------|
| Data contracts | `src/provenance/models/` |
| Pipeline agents | `src/provenance/agents/` |
| Scraper implementations | `src/provenance/scrapers/` |
| Queue wiring | `src/provenance/pipeline/runner.py` |
| Shared utilities | `src/provenance/utils/` |
| Configuration | `src/provenance/config.py` |
| Tests (mirror src/) | `tests/` |
| CLI entry points | `scripts/` |
| Documentation | `docs/` |

---

## What NOT To Do

| ❌ Don't | ✅ Do instead |
|----------|--------------|
| `import requests` | `import httpx` |
| `from .basic_web_scraper import BasicWebScraper` in agents | `ScraperFactory.create(config)` |
| Pass `dict` between agents | Use the Pydantic model for that stage |
| `model = SentenceTransformer(...)` inside `process()` | Load in `__init__`, reuse per call |
| `print(f"error: {e}")` | `self.logger.error("msg", exc_info=True)` |
| Hardcode `threshold = 0.45` in agent code | `config.similarity_threshold` |
| Add paid detector APIs by default | Prefer local/free detectors and keep external calls optional |
| Add logic to `runner.py` beyond queue wiring | Put it in the relevant agent |
| Increase `semantic_q maxsize` without profiling | Profile first; document the reason |
| Disable `BudgetExceededError` guard | Never — this is a hard financial limit |
