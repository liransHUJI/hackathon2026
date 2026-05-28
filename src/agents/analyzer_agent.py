"""
AnalyzerAgent  —  Stage 4
==========================
Chronological sorting + Gemini-powered semantic similarity filtering.

Pipeline position
─────────────────
  BroadScraperAgent  →  [ScrapedResultSet]  →  AnalyzerAgent  →  [AnalyzedSet]  →  AISignatureDetectorAgent

What it does
─────────────
  1. Receives a list of raw ScrapedResult objects and the baseline NewsItem.
  2. Sorts by publication date  (oldest first; items missing dates go last).
  3. Calls Gemini with response_schema=list[SimilarityEvaluation] to evaluate
     EVERY candidate simultaneously — getting a structured score, boolean verdict,
     and one-sentence reasoning per article.
  4. Filters out false positives (similarity_score < threshold).
  5. Computes a composite score weighting similarity AND chronological priority.
  6. Returns the top-K earliest, most-relevant candidates for AI-detection.

Why Gemini for similarity (not sentence-transformers)?
───────────────────────────────────────────────────────
Embedding-based cosine similarity catches surface overlap but fails on
"false positive traps" — articles that share keywords or named entities
but describe completely different events.  Gemini's reasoning enables
nuanced verdicts like:

  Baseline:  "Netanyahu announces retirement from politics"
  Candidate: "Netanyahu flies to London for diplomatic summit"
  → Embedding score might be 0.72 (same person, political context)
  → Gemini score: 0.12  is_true_positive=False
    Reasoning: "False positive: same person but covers a travel event, not a political retirement."

Structured output contract
──────────────────────────
SimilarityEvaluation (local Pydantic model) is the schema passed to Gemini.
It is NOT a stage data-contract — it exists only inside this module.
Stage contracts (ScrapedResultSet → AnalyzedSet) use schemas.py models.
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
import time
from datetime import datetime, timezone
from typing import Any, Optional

from pydantic import BaseModel, Field, TypeAdapter, ValidationError

from src.agents.base_agent import BaseAgent
from src.config import PipelineConfig
from src.models.schemas import (
    AnalyzedSet,
    NewsItem,
    Permutation,
    PermutationSet,
    RankedResult,
    ScrapedResult,
    ScrapedResultSet,
)

logger = logging.getLogger("provenance.agent.analyzer")

# ── Tuning constants ──────────────────────────────────────────────────────────
DEFAULT_MODEL       = "gemini-2.0-flash"
DEFAULT_THRESHOLD   = 0.45
DEFAULT_TOP_K       = 10
_MAX_BATCH_SIZE     = 50    # items per Gemini call (stays within context budget)
_MAX_OUTPUT_TOKENS  = 8192
_EVAL_TEMPERATURE   = 0.1   # near-zero temp → consistent, deterministic verdicts

# Composite score weights (must sum to 1.0)
_W_SIMILARITY = 0.6
_W_RECENCY    = 0.4


# ══════════════════════════════════════════════════════════════════════════════
#  SimilarityEvaluation — Gemini response schema (module-private)
# ══════════════════════════════════════════════════════════════════════════════

class SimilarityEvaluation(BaseModel):
    """
    Gemini's verdict on one scraped article relative to the baseline.

    item_index correlates each evaluation back to its input position so that
    out-of-order or truncated responses can be safely realigned.
    """

    item_index: int = Field(
        description=(
            "0-based index of this candidate in the input list. "
            "Must match the [N] label in the prompt exactly."
        )
    )
    similarity_score: float = Field(
        ge=0.0,
        le=1.0,
        description=(
            "Semantic similarity of this article to the baseline headline. "
            "0.0 = completely unrelated story; 1.0 = identical event, different wording."
        ),
    )
    is_true_positive: bool = Field(
        description=(
            "True only when the article covers the exact same specific event as the baseline "
            "(same subject + same action + same context). "
            "Must be False when similarity_score < 0.65."
        ),
    )
    reasoning: str = Field(
        description=(
            "One sentence explaining the verdict. "
            "Lead with 'True positive:' or 'False positive:' and name the decisive factor."
        ),
    )


_EVAL_LIST_ADAPTER: TypeAdapter[list[SimilarityEvaluation]] = TypeAdapter(
    list[SimilarityEvaluation]
)


# ══════════════════════════════════════════════════════════════════════════════
#  System prompt
# ══════════════════════════════════════════════════════════════════════════════

_SYSTEM_PROMPT = """\
You are a semantic similarity analyst for an investigative journalism provenance tool.
Your verdicts determine which historical articles are genuine antecedents of a breaking story.

YOUR TASK
─────────
Given a BASELINE headline and a NUMBERED LIST of candidate articles, evaluate whether
each candidate covers the exact same specific event as the baseline.

Return a JSON array with exactly one evaluation object per candidate, in the SAME ORDER
as the numbered input list.


THE CRITICAL DISTINCTION — SAME EVENT vs SAME TOPIC
─────────────────────────────────────────────────────
A TRUE POSITIVE requires ALL THREE:
  ① Same SUBJECT  — the identical person, organisation, or named entity
  ② Same EVENT    — the same specific action or announcement
  ③ Same CONTEXT  — broadly the same circumstances

FALSE POSITIVE PATTERNS (is_true_positive = False):

  TYPE A — Same Entity, Completely Different Event
  ──────────────────────────────────────────────────
    Baseline:  "Netanyahu announces retirement from politics"
    Candidate: "Netanyahu flies to London for diplomatic summit"
    → Same person, totally different event.  similarity_score ≈ 0.12

  TYPE B — Keyword Coincidence, Unrelated Subject
  ─────────────────────────────────────────────────
    Baseline:  "Prime Minister announces retirement"
    Candidate: "Government raises national retirement age to 68"
    → "retirement" is coincidental; the subjects are unrelated.  score ≈ 0.08

  TYPE C — Related Saga, Distinct Specific Event
  ─────────────────────────────────────────────────
    Baseline:  "CEO resigns following board vote"
    Candidate: "Board members call for CEO's resignation"
    → Calls for ≠ Confirmation.  Different event within the same story arc.  score ≈ 0.38

  TYPE D — Temporal Confusion / Speculation vs Confirmed
  ────────────────────────────────────────────────────────
    Baseline:  "Chancellor officially resigns"
    Candidate: "Rumours swirl that Chancellor may resign"
    → Rumour ≠ Confirmed fact.  score ≈ 0.45  (below true-positive threshold)

  TYPE E — Same Role, Different Person or Jurisdiction
  ──────────────────────────────────────────────────────
    Baseline:  "German Chancellor steps down"
    Candidate: "Austrian Chancellor calls snap election"
    → Same title, different country, different event.  score ≈ 0.15


SCORING GUIDE
─────────────
  1.00  Identical event — rewording only                  → is_true_positive = True
  0.90  Same event — minor framing difference             → is_true_positive = True
  0.75  Probably the same event — minor ambiguity         → is_true_positive = True
  0.65  Borderline — lean True only if subject+event match → is_true_positive = True
  0.50  Possibly same, but key details unclear             → is_true_positive = False
  0.35  Same entities, probably a different event          → is_true_positive = False
  0.15  Topical/keyword overlap, clearly different story   → is_true_positive = False
  0.05  Superficial name match, completely different story → is_true_positive = False

Set is_true_positive = True ONLY when similarity_score >= 0.65.

REASONING FORMAT
────────────────
Exactly one sentence.  Begin with "True positive:" or "False positive:" then the key reason.
Good examples:
  "True positive: same announcement of the PM's political retirement reworded."
  "False positive: same politician but covers a travel event, not a resignation."
  "False positive: 'retirement' here refers to pension policy, not a person leaving office."
"""


# ══════════════════════════════════════════════════════════════════════════════
#  AnalyzerAgent
# ══════════════════════════════════════════════════════════════════════════════

class AnalyzerAgent(BaseAgent):
    """
    Stage 4: chronological sort + Gemini semantic similarity filtering.

    Direct use (no queue required):
        analyzed = await agent.analyze_and_sort_sources(baseline, scraped)

    Queue-driven use (via PipelineRunner):
        result = await agent.process(scraped_result_set)
    """

    def __init__(
        self,
        name: str = "analyzer",
        api_key: Optional[str] = None,
        model: Optional[str] = None,
        similarity_threshold: Optional[float] = None,
        top_k: Optional[int] = None,
        input_queue: Optional[asyncio.Queue[Any]] = None,
        output_queue: Optional[asyncio.Queue[Any]] = None,
        config: Optional[PipelineConfig] = None,
    ) -> None:
        super().__init__(
            name=name,
            input_queue=input_queue,
            output_queue=output_queue,
            config=config,
        )

        _key = api_key or os.getenv("GEMINI_API_KEY") or os.getenv("GOOGLE_API_KEY")
        if not _key:
            raise ValueError(
                "No Gemini API key found.\n"
                "  Set GEMINI_API_KEY in .env or pass api_key= to AnalyzerAgent()."
            )

        self.model_name: str = model or os.getenv("GEMINI_MODEL", DEFAULT_MODEL)
        self.similarity_threshold: float = (
            similarity_threshold
            if similarity_threshold is not None
            else (self.config.similarity_threshold or DEFAULT_THRESHOLD)
        )
        self.top_k: int = top_k or self.config.top_candidates or DEFAULT_TOP_K

        try:
            from google import genai        # noqa: PLC0415
            from google.genai import types  # noqa: PLC0415
            self._client = genai.Client(api_key=_key)
            self._types  = types
        except ImportError as exc:
            raise ImportError(
                "google-genai is required.  Run: pip install google-genai>=1.0.0"
            ) from exc

        self._anthropic_key: Optional[str] = os.getenv("ANTHROPIC_API_KEY")

        logger.info(
            "AnalyzerAgent ready — model=%s  threshold=%.2f  top_k=%d",
            self.model_name, self.similarity_threshold, self.top_k,
        )

    # ── BaseAgent.process (queue path) ────────────────────────────────────────

    async def process(self, item: ScrapedResultSet) -> AnalyzedSet:  # type: ignore[override]
        """Queue-driven entry point called by PipelineRunner."""
        baseline = item.source_permutation_set.source_item
        return await self.analyze_and_sort_sources(
            baseline=baseline,
            scraped=item.results,
            source_result_set=item,
        )

    # ── Primary public method ─────────────────────────────────────────────────

    async def analyze_and_sort_sources(
        self,
        baseline: NewsItem,
        scraped: list[ScrapedResult],
        source_result_set: Optional[ScrapedResultSet] = None,
    ) -> AnalyzedSet:
        """
        Sort scraped results chronologically, evaluate semantic similarity via
        Gemini, filter false positives, and return the top-K candidates.

        Args:
            baseline:          The original story whose provenance we are tracing.
            scraped:           Raw list from BroadScraperAgent (any order, any dates).
            source_result_set: Optional upstream context preserved in AnalyzedSet lineage.

        Returns:
            AnalyzedSet with:
              • ranked_results — all items that passed the similarity threshold,
                sorted by composite_score (similarity × recency).
              • top_candidates — the top-K from ranked_results for Stage 5.
        """
        t0 = time.perf_counter()

        if not scraped:
            logger.warning("AnalyzerAgent received empty scraped list — returning empty result.")
            return self._empty_analyzed_set(baseline, source_result_set)

        # ── Step 1: Chronological sort ─────────────────────────────────────────
        sorted_results = _sort_by_date(scraped)
        n_dated   = sum(1 for r in sorted_results if r.published_at is not None)
        n_undated = len(sorted_results) - n_dated
        logger.info(
            "Sorted %d results: %d dated, %d without date (pushed to end).",
            len(sorted_results), n_dated, n_undated,
        )

        # ── Step 2: Gemini similarity evaluation ──────────────────────────────
        evaluations = await self._evaluate_similarities(baseline, sorted_results)

        # ── Step 3: Build RankedResult objects ────────────────────────────────
        total = len(sorted_results)
        ranked: list[RankedResult] = []
        for chron_rank, (result, ev) in enumerate(
            zip(sorted_results, evaluations), start=1
        ):
            rw = _recency_weight(chron_rank, total)
            cs = round(_W_SIMILARITY * ev.similarity_score + _W_RECENCY * rw, 4)
            ranked.append(
                RankedResult(
                    scraped_result    = result,
                    similarity_score  = ev.similarity_score,
                    chronological_rank= chron_rank,
                    composite_score   = min(cs, 1.0),
                    is_likely_original= False,
                )
            )

        # ── Step 4: Filter below threshold ───────────────────────────────────
        passing = [r for r in ranked if r.similarity_score >= self.similarity_threshold]
        logger.info(
            "%d/%d results passed similarity threshold %.2f.",
            len(passing), total, self.similarity_threshold,
        )

        # ── Step 5: Mark the earliest true positive ───────────────────────────
        if passing:
            earliest = min(passing, key=lambda r: r.chronological_rank)
            passing = [
                RankedResult(**{**r.model_dump(), "is_likely_original": True})
                if r.scraped_result.result_id == earliest.scraped_result.result_id
                else r
                for r in passing
            ]

        # ── Step 6: Select top-K by composite score ───────────────────────────
        passing_sorted  = sorted(passing, key=lambda r: r.composite_score, reverse=True)
        top_candidates  = passing_sorted[: self.top_k]

        srs     = source_result_set or _build_minimal_result_set(baseline, scraped)
        elapsed = time.perf_counter() - t0

        logger.info(
            "Analysis done: %d candidates selected in %.2fs.", len(top_candidates), elapsed
        )
        return AnalyzedSet(
            source_result_set        = srs,
            ranked_results           = passing_sorted,
            top_candidates           = top_candidates,
            analysis_duration_seconds= elapsed,
            similarity_model         = self.model_name,
        )

    # ── Evaluation internals ──────────────────────────────────────────────────

    async def _evaluate_similarities(
        self,
        baseline: NewsItem,
        items: list[ScrapedResult],
    ) -> list[SimilarityEvaluation]:
        """Evaluate all items, batched to respect context limits."""
        all_evals: list[SimilarityEvaluation] = []
        for batch_start, batch in _make_batches(items, _MAX_BATCH_SIZE):
            evals = await self._evaluate_batch(baseline, batch, batch_start)
            all_evals.extend(evals)
        # Realign: ensure exactly one eval per item (pad if Gemini dropped any)
        return _align_evaluations(all_evals, len(items))

    async def _evaluate_batch(
        self,
        baseline: NewsItem,
        batch: list[ScrapedResult],
        index_offset: int = 0,
    ) -> list[SimilarityEvaluation]:
        """One Gemini call for a batch; returns one eval per item."""
        prompt = _build_eval_prompt(baseline, batch, index_offset)
        loop   = asyncio.get_event_loop()
        try:
            response = await loop.run_in_executor(
                None, self._call_gemini_sync, prompt
            )
        except Exception as exc:
            logger.error(
                "Gemini evaluation call failed: %s — using neutral fallback.", exc,
                exc_info=True,
            )
            if self._anthropic_key:
                logger.info("Falling back to Anthropic claude-haiku for similarity evaluation.")
                try:
                    return await self._evaluate_batch_with_anthropic(baseline, batch, index_offset)
                except Exception as anthropic_exc:
                    logger.error(
                        "Anthropic fallback also failed: %s", anthropic_exc, exc_info=True
                    )
            return _fallback_evaluations(batch, index_offset)
        return self._parse_evaluations(response, batch, index_offset)

    async def _evaluate_batch_with_anthropic(
        self,
        baseline: NewsItem,
        batch: list[ScrapedResult],
        index_offset: int,
    ) -> list[SimilarityEvaluation]:
        """Fallback similarity evaluation via Anthropic claude-haiku with tool use."""
        import anthropic  # noqa: PLC0415

        client = anthropic.AsyncAnthropic(api_key=self._anthropic_key)
        prompt = _build_eval_prompt(baseline, batch, index_offset)

        response = await client.messages.create(
            model="claude-haiku-4-5-20251001",
            max_tokens=8192,
            system=_SYSTEM_PROMPT,
            tools=[
                {
                    "name": "submit_evaluations",
                    "description": "Submit similarity evaluations for all candidate articles.",
                    "input_schema": {
                        "type": "object",
                        "properties": {
                            "evaluations": {
                                "type": "array",
                                "items": {
                                    "type": "object",
                                    "properties": {
                                        "item_index": {"type": "integer"},
                                        "similarity_score": {"type": "number"},
                                        "is_true_positive": {"type": "boolean"},
                                        "reasoning": {"type": "string"},
                                    },
                                    "required": [
                                        "item_index",
                                        "similarity_score",
                                        "is_true_positive",
                                        "reasoning",
                                    ],
                                },
                            }
                        },
                        "required": ["evaluations"],
                    },
                }
            ],
            tool_choice={"type": "tool", "name": "submit_evaluations"},
            messages=[{"role": "user", "content": prompt}],
        )

        for block in response.content:
            if block.type == "tool_use" and block.name == "submit_evaluations":
                raw = block.input.get("evaluations", [])
                evals = _EVAL_LIST_ADAPTER.validate_python(raw)
                logger.info(
                    "Anthropic fallback produced %d evaluations for batch offset=%d",
                    len(evals), index_offset,
                )
                return evals

        logger.warning("Anthropic response had no tool_use block; using neutral fallback.")
        return _fallback_evaluations(batch, index_offset)

    def _call_gemini_sync(self, prompt: str) -> Any:
        """Synchronous Gemini call, executed inside ThreadPoolExecutor."""
        config = self._types.GenerateContentConfig(
            system_instruction=_SYSTEM_PROMPT,
            response_mime_type="application/json",
            response_schema=list[SimilarityEvaluation],
            temperature=_EVAL_TEMPERATURE,
            max_output_tokens=_MAX_OUTPUT_TOKENS,
        )
        return self._client.models.generate_content(
            model=self.model_name,
            contents=prompt,
            config=config,
        )

    def _parse_evaluations(
        self,
        response: Any,
        batch: list[ScrapedResult],
        index_offset: int,
    ) -> list[SimilarityEvaluation]:
        """
        Extract list[SimilarityEvaluation] from Gemini response.
        Three-layer fallback: response.parsed → JSON decode → neutral defaults.
        """
        # Layer 1: SDK structured-output path
        parsed = getattr(response, "parsed", None)
        if parsed is not None:
            try:
                validated = _EVAL_LIST_ADAPTER.validate_python(
                    [e.model_dump() if hasattr(e, "model_dump") else e for e in parsed]
                )
                if validated:
                    return validated
            except (ValidationError, Exception) as exc:
                logger.warning("response.parsed validation failed (%s); trying JSON.", exc)

        # Layer 2: Manual JSON decode
        raw = getattr(response, "text", None)
        if raw:
            try:
                data = json.loads(raw)
                if isinstance(data, dict):
                    data = next(iter(data.values()), [])
                validated = _EVAL_LIST_ADAPTER.validate_python(data)
                if validated:
                    return validated
            except (json.JSONDecodeError, ValidationError, Exception) as exc:
                logger.warning("JSON fallback also failed (%s).", exc)

        # Layer 3: Neutral placeholder so the pipeline continues
        logger.error("Could not parse any evaluations — returning neutral fallback.")
        return _fallback_evaluations(batch, index_offset)

    def _empty_analyzed_set(
        self,
        baseline: NewsItem,
        srs: Optional[ScrapedResultSet],
    ) -> AnalyzedSet:
        return AnalyzedSet(
            source_result_set=srs or _build_minimal_result_set(baseline, []),
            ranked_results=[],
            top_candidates=[],
            analysis_duration_seconds=0.0,
            similarity_model=self.model_name,
        )


# ══════════════════════════════════════════════════════════════════════════════
#  Module-level pure helpers  (stateless, independently unit-testable)
# ══════════════════════════════════════════════════════════════════════════════

def _sort_by_date(items: list[ScrapedResult]) -> list[ScrapedResult]:
    """
    Sort by published_at ascending — oldest first.
    Items with no date are pushed to the END of the list.
    Items with timezone-naive datetimes are treated as UTC.
    """
    def sort_key(r: ScrapedResult):
        dt = r.published_at
        if dt is None:
            # Use a far-future sentinel so None-date items sort last
            return datetime(9999, 12, 31, tzinfo=timezone.utc)
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=timezone.utc)
        return dt

    return sorted(items, key=sort_key)


def _recency_weight(chronological_rank: int, total: int) -> float:
    """
    Recency weight: 1.0 for the oldest item, 0.0 for the newest.

    Earlier sources are more valuable for provenance — we want to surface
    the article that first published the story, not recent reposts.
    """
    if total <= 1:
        return 1.0
    return 1.0 - (chronological_rank - 1) / (total - 1)


def _build_eval_prompt(
    baseline: NewsItem,
    batch: list[ScrapedResult],
    index_offset: int = 0,
) -> str:
    """
    Build the user-turn prompt for one evaluation batch.

    Each candidate is labelled with its 0-based absolute index so that
    Gemini's response can be unambiguously correlated back to the input.
    """
    lines = [
        f'BASELINE HEADLINE: "{baseline.headline}"',
        "",
        f"CANDIDATE ARTICLES ({len(batch)} items — evaluate ALL of them):",
        "",
    ]
    for i, r in enumerate(batch):
        abs_idx = index_offset + i
        date_str = (
            r.published_at.strftime("%Y-%m-%d")
            if r.published_at else "date unknown"
        )
        title_str   = r.title   or "(no title)"
        snippet_str = (r.snippet or "")[:300].strip() or "(no snippet)"
        lines += [
            f"[{abs_idx}] Title:   {title_str}",
            f"     Snippet: {snippet_str}",
            f"     Date:    {date_str}",
            f"     Domain:  {r.domain or 'unknown'}",
            "",
        ]
    lines += [
        f"Return exactly {len(batch)} evaluation objects.",
        f"item_index values must be: {list(range(index_offset, index_offset + len(batch)))}",
        "Preserve the input order.",
    ]
    return "\n".join(lines)


def _make_batches(
    items: list[ScrapedResult],
    batch_size: int,
) -> list[tuple[int, list[ScrapedResult]]]:
    """Return (start_index, batch) tuples."""
    return [
        (i, items[i : i + batch_size])
        for i in range(0, len(items), batch_size)
    ]


def _align_evaluations(
    evals: list[SimilarityEvaluation],
    expected_count: int,
) -> list[SimilarityEvaluation]:
    """
    Realign evaluations to a contiguous 0…(expected_count-1) index range.

    If Gemini returned out-of-order items (rare but possible), they are
    sorted by item_index.  Any missing indices are filled with a neutral
    fallback score of 0.3 so the pipeline can continue.
    """
    index_map = {e.item_index: e for e in evals}
    result: list[SimilarityEvaluation] = []
    for i in range(expected_count):
        if i in index_map:
            result.append(index_map[i])
        else:
            logger.warning("Missing evaluation for item index %d — using neutral fallback.", i)
            result.append(
                SimilarityEvaluation(
                    item_index=i,
                    similarity_score=0.3,
                    is_true_positive=False,
                    reasoning="Evaluation missing — neutral fallback applied.",
                )
            )
    return result


def _fallback_evaluations(
    batch: list[ScrapedResult],
    index_offset: int,
) -> list[SimilarityEvaluation]:
    """
    Neutral fallback used when Gemini call fails entirely.

    A score of 0.3 is below the default 0.45 threshold, so these items will
    be filtered out — correct behaviour when we have no real data.
    """
    return [
        SimilarityEvaluation(
            item_index=index_offset + i,
            similarity_score=0.3,
            is_true_positive=False,
            reasoning="API call failed — neutral fallback score assigned.",
        )
        for i in range(len(batch))
    ]


def _build_minimal_result_set(
    baseline: NewsItem,
    scraped: list[ScrapedResult],
) -> ScrapedResultSet:
    """
    Build the minimal ScrapedResultSet needed to satisfy AnalyzedSet.source_result_set
    when calling analyze_and_sort_sources() directly (not via process()).
    """
    minimal_pset = PermutationSet(
        source_item=baseline,
        original_query=baseline.headline,
        permutations=[Permutation(text=baseline.headline)],
        model_used="",
    )
    return ScrapedResultSet(
        source_permutation_set=minimal_pset,
        results=scraped,
        total_results_raw=len(scraped),
    )
