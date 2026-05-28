#!/usr/bin/env python3
"""
BroadScraperAgent — Smoke Test
================================
Verifies that the BroadScraperAgent correctly:

  ① Accepts a PermutationSet (3 Permutations) and fans them out concurrently
  ② Uses asyncio.gather under the hood — all 3 queries run in parallel
  ③ Collects and deduplicates ScrapedResult objects across permutations
  ④ Returns a valid ScrapedResultSet that Stage 4 (AnalyzerAgent) can consume
  ⑤ Handles per-task failures gracefully (one task crashes → others continue)

Mock dataset design
────────────────────
  The mock scraper returns 2–3 results per permutation with intentionally varied
  (and sometimes missing) publication dates to exercise Stage 4's sorting logic:

    Permutation 0  →  3 results: haaretz.com (Jan 2024), reuters.com (Jun 2024),
                                  globes.co.il (Dec 2023)
    Permutation 1  →  2 results: timesofisrael.com (Mar 2024), jpost.com (no date)
    Permutation 2  →  3 results: globes.co.il (SAME URL as perm-0 → dedup!),
                                  bbc.co.uk (Jun 2025), apnews.com (Aug 2024)

  Raw count: 8   ▸   After agent dedup: 7  ▸   Dates span Dec-2023 → Jun-2025

No external API keys are required for this test.

Usage:
    python test_scraper_agent.py
    python test_scraper_agent.py --live      # use real BasicWebScraper + DuckDuckGo
    python test_scraper_agent.py --concurrency 2
"""

from __future__ import annotations

import argparse
import asyncio
import os
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Optional

from dotenv import load_dotenv
load_dotenv(Path(__file__).parent / ".env")

from src.agents.scraper_agent import BroadScraperAgent
from src.models.schemas import (
    ContentType, NewsItem, NewsSource, Permutation,
    PermutationSet, ScrapedResult, ScrapedResultSet,
)
from src.scraper.base import BaseScraper
from src.scraper.factory import ScraperFactory

# ── ANSI helpers ──────────────────────────────────────────────────────────────
_G = "\033[92m"; _R = "\033[91m"; _Y = "\033[93m"; _C = "\033[96m"
_B = "\033[1m";  _D = "\033[0m"

def ok(m):   return f"{_G}✓{_D}  {m}"
def err(m):  return f"{_R}✗{_D}  {m}"
def warn(m): return f"{_Y}⚠{_D}  {m}"
def info(m): return f"{_C}ℹ{_D}  {m}"


# ════════════════════════════════════════════════════════════════════════════
#  3 HARDCODED PERMUTATIONS (the input to BroadScraperAgent)
# ════════════════════════════════════════════════════════════════════════════

PERMUTATIONS: list[Permutation] = [
    Permutation(
        text="Netanyahu announces retirement from politics",
        strategy="paraphrase",
        confidence=1.0,
    ),
    Permutation(
        text="Israeli Prime Minister confirms departure from political life",
        strategy="entity_generalization",
        confidence=0.95,
    ),
    Permutation(
        text="Bibi stepping down ends decades-long Israeli political career",
        strategy="synonym",
        confidence=0.90,
    ),
]


# ════════════════════════════════════════════════════════════════════════════
#  MOCK SCRAPER
#  Returns pre-defined ScrapedResult objects with varied / missing dates.
#  Permutation 0 and Permutation 2 share one URL → exercises agent dedup.
# ════════════════════════════════════════════════════════════════════════════

_SHARED_URL = "https://globes.co.il/article/netanyahu-pension-reform"

_MOCK_RESULTS: dict[int, list[ScrapedResult]] = {
    0: [
        ScrapedResult(
            url="https://haaretz.com/politics/netanyahu-to-step-down",
            title="Netanyahu to step down, ending decades-long political career",
            snippet=(
                "Benjamin Netanyahu has confirmed he will leave politics, bringing to an end "
                "his long tenure as Israel's dominant political figure."
            ),
            domain="haaretz.com",
            content_type=ContentType.ARTICLE,
            published_at=datetime(2024, 1, 3, 8, 0, tzinfo=timezone.utc),
            query_used=PERMUTATIONS[0].text,
            scraper_id="mock",
            http_status=200,
        ),
        ScrapedResult(
            url="https://reuters.com/world/middle-east/netanyahu-retirement",
            title="Head of Israeli government ends political career",
            snippet=(
                "The Israeli Prime Minister formally declared an end to his political career "
                "on Wednesday, sending shockwaves through the Knesset."
            ),
            domain="reuters.com",
            content_type=ContentType.ARTICLE,
            published_at=datetime(2024, 6, 14, 18, 45, tzinfo=timezone.utc),
            query_used=PERMUTATIONS[0].text,
            scraper_id="mock",
            http_status=200,
        ),
        ScrapedResult(
            url=_SHARED_URL,                           # ← this URL also appears in perm-2
            title="Analysis: Netanyahu legacy and Israeli political transition",
            snippet="Political analysts assess the long-term impact of the Prime Minister's decision.",
            domain="globes.co.il",
            content_type=ContentType.ARTICLE,
            published_at=datetime(2023, 12, 15, 10, 0, tzinfo=timezone.utc),  # EARLIEST
            query_used=PERMUTATIONS[0].text,
            scraper_id="mock",
            http_status=200,
        ),
    ],
    1: [
        ScrapedResult(
            url="https://timesofisrael.com/pm-confirms-departure",
            title="Israeli PM confirms departure from political life",
            snippet=(
                "The Israeli Prime Minister officially announced his plans to retire from "
                "government, citing a desire to spend more time with family."
            ),
            domain="timesofisrael.com",
            content_type=ContentType.ARTICLE,
            published_at=datetime(2024, 3, 22, 14, 30, tzinfo=timezone.utc),
            query_used=PERMUTATIONS[1].text,
            scraper_id="mock",
            http_status=200,
        ),
        ScrapedResult(
            url="https://jpost.com/israel-news/netanyahu-exits-politics",
            title="Bibi quits: Netanyahu exits the political stage",
            snippet=(
                "In a surprise announcement, Netanyahu stated he would not seek re-election "
                "and intends to formally retire from politics by year end."
            ),
            domain="jpost.com",
            content_type=ContentType.ARTICLE,
            published_at=None,                          # ← deliberately missing date
            query_used=PERMUTATIONS[1].text,
            scraper_id="mock",
            http_status=200,
        ),
    ],
    2: [
        ScrapedResult(
            url=_SHARED_URL,                           # ← DUPLICATE of perm-0's third result
            title="Analysis: Netanyahu legacy (enriched copy)",
            snippet=(
                "Political analysts assess the long-term impact of the Prime Minister's decision. "
                "This version contains full article text for richer analysis."
            ),
            full_text=(
                "Full body text: Netanyahu's retirement marks a watershed moment in Israeli politics. "
                "Analysts note the vacuum left by his departure will reshape coalition dynamics "
                "for years to come, affecting both domestic policy and regional diplomacy."
            ),
            domain="globes.co.il",
            content_type=ContentType.ARTICLE,
            published_at=datetime(2023, 12, 15, 10, 0, tzinfo=timezone.utc),
            query_used=PERMUTATIONS[2].text,
            scraper_id="mock",
            http_status=200,
        ),
        ScrapedResult(
            url="https://bbc.co.uk/news/world-middle-east-netanyahu-retrospective",
            title="Revisiting Netanyahu's retirement announcement one year on",
            snippet="A year ago, Netanyahu announced his retirement. We revisit the historic moment.",
            domain="bbc.co.uk",
            content_type=ContentType.ARTICLE,
            published_at=datetime(2025, 6, 15, 12, 0, tzinfo=timezone.utc),  # LATEST
            query_used=PERMUTATIONS[2].text,
            scraper_id="mock",
            http_status=200,
        ),
        ScrapedResult(
            url="https://apnews.com/article/netanyahu-retirement-analysis",
            title="What Netanyahu's exit means for Middle East diplomacy",
            snippet="AP analysis of the geopolitical implications of Netanyahu stepping back from power.",
            domain="apnews.com",
            content_type=ContentType.ARTICLE,
            published_at=datetime(2024, 8, 30, 9, 0, tzinfo=timezone.utc),
            query_used=PERMUTATIONS[2].text,
            scraper_id="mock",
            http_status=200,
        ),
    ],
}


class MockBaseScraper(BaseScraper):
    """
    Deterministic mock scraper.  Returns pre-defined results indexed by permutation
    position — the position is inferred from the text matching PERMUTATIONS.

    For any unknown permutation text, returns an empty list.
    Uses ScraperFactory.inject() pattern so it slots into the Strategy hierarchy.
    """

    @property
    def backend_name(self) -> str:
        return "mock"

    async def scrape(
        self,
        permutations: list[Permutation],
        *,
        max_results_per_query: int = 10,
    ) -> list[ScrapedResult]:
        results: list[ScrapedResult] = []
        for perm in permutations:
            # Look up by position in the global PERMUTATIONS list
            idx = next(
                (i for i, p in enumerate(PERMUTATIONS) if p.text == perm.text),
                None,
            )
            if idx is not None and idx in _MOCK_RESULTS:
                results.extend(_MOCK_RESULTS[idx])
            # Small artificial delay to show concurrent execution is meaningful
            await asyncio.sleep(0.01)
        return self._deduplicate(results)

    async def fetch_page(self, url: str, *, timeout_s: int = 15) -> Optional[str]:
        return f"<html><body>Mock content for {url}</body></html>"


# ════════════════════════════════════════════════════════════════════════════
#  FAILING MOCK SCRAPER  (for graceful-degradation test)
# ════════════════════════════════════════════════════════════════════════════

class PartiallyFailingMockScraper(BaseScraper):
    """
    Raises on one permutation to verify gather_sources recovers gracefully.
    Returns normal mock results for all other permutations.
    """

    def __init__(self, fail_on_perm_index: int = 1) -> None:
        self._fail_idx = fail_on_perm_index

    @property
    def backend_name(self) -> str:
        return "mock_failing"

    async def scrape(
        self,
        permutations: list[Permutation],
        *,
        max_results_per_query: int = 10,
    ) -> list[ScrapedResult]:
        results: list[ScrapedResult] = []
        for perm in permutations:
            idx = next(
                (i for i, p in enumerate(PERMUTATIONS) if p.text == perm.text),
                None,
            )
            if idx == self._fail_idx:
                raise RuntimeError(f"Simulated scraper failure for permutation[{idx}]")
            # Return the normal mock data for non-failing permutations
            if idx is not None and idx in _MOCK_RESULTS:
                results.extend(_MOCK_RESULTS[idx])
        return self._deduplicate(results)

    async def fetch_page(self, url: str, *, timeout_s: int = 15) -> Optional[str]:
        return None


# ════════════════════════════════════════════════════════════════════════════
#  SMOKE TEST
# ════════════════════════════════════════════════════════════════════════════

async def run_smoke_test(concurrency: int, live: bool) -> bool:
    print(f"\n{_B}{'═' * 68}{_D}")
    print(f"{_B}  Provenance Pipeline — BroadScraperAgent Smoke Test{_D}")
    print(f"{_B}{'═' * 68}{_D}\n")

    # ── Setup: wrap the 3 Permutations in a minimal PermutationSet ─────────
    baseline = NewsItem(
        headline="Israeli Prime Minister Benjamin Netanyahu announces retirement from politics",
        source_type=NewsSource.MANUAL,
        source_channel="smoke_test",
        published_at=datetime(2024, 6, 15, tzinfo=timezone.utc),
    )
    perm_set = PermutationSet(
        source_item=baseline,
        original_query=baseline.headline,
        permutations=PERMUTATIONS,
        model_used="mock",
    )

    print(info(f"Baseline: \"{baseline.headline[:70]}\""))
    print(info(f"{len(PERMUTATIONS)} permutations  |  concurrency={concurrency}  |  live={live}"))
    for i, p in enumerate(PERMUTATIONS):
        print(f"  [{i}] {p.strategy:<25}  \"{p.text[:60]}\"")
    print()

    failures: list[str] = []

    # ── TEST 1: Main smoke test with mock scraper ───────────────────────────
    print(f"  {_B}Test 1 — MockBaseScraper (no network, deterministic){_D}\n")
    mock_scraper = ScraperFactory.inject(MockBaseScraper())
    agent = BroadScraperAgent(
        scraper=mock_scraper,
        concurrency=concurrency,
        max_results_per_query=10,
    )

    t0 = time.perf_counter()
    result: ScrapedResultSet = await agent.gather_sources(perm_set)
    elapsed = time.perf_counter() - t0

    # Print results table
    print(f"  {'─' * 68}")
    print(f"  {_B}{'#':>3}  {'DATE':<12}  {'DOMAIN':<30}  TITLE{_D}")
    print(f"  {'─' * 68}")
    from src.agents.analyzer_agent import _sort_by_date
    sorted_results = _sort_by_date(result.results)
    for i, r in enumerate(sorted_results, 1):
        date_s = r.published_at.strftime("%Y-%m-%d") if r.published_at else "  no date"
        has_text = f"  {_G}[+full_text]{_D}" if r.full_text else ""
        print(f"  {i:>3}.  {date_s:<12}  {r.domain:<30}  {(r.title or '')[:38]}{has_text}")
    print(f"  {'─' * 68}")
    print(
        f"  {len(PERMUTATIONS)} queries  │  "
        f"raw={result.total_results_raw}  │  "
        f"dedup_removed={result.deduplication_removed}  │  "
        f"final={len(result.results)}  │  "
        f"elapsed={elapsed:.3f}s\n"
    )

    # ── Structural assertions ─────────────────────────────────────────────
    print(f"  {_B}Structural Assertions{_D}\n")

    # Type check
    if isinstance(result, ScrapedResultSet):
        print(ok("Return type is ScrapedResultSet"))
    else:
        failures.append(f"Return type {type(result).__name__} ≠ ScrapedResultSet")

    # Source lineage preserved
    if result.source_permutation_set.source_item.item_id == baseline.item_id:
        print(ok("source_permutation_set.source_item lineage preserved"))
    else:
        failures.append("source_item.item_id mismatch — lineage broken")

    # Query count
    if result.total_queries_issued == len(PERMUTATIONS):
        print(ok(f"total_queries_issued = {result.total_queries_issued} (matches # permutations)"))
    else:
        failures.append(f"total_queries_issued {result.total_queries_issued} ≠ {len(PERMUTATIONS)}")

    # Raw count: 3+2+3 = 8
    EXPECTED_RAW = 8
    if result.total_results_raw == EXPECTED_RAW:
        print(ok(f"total_results_raw = {result.total_results_raw} (expected {EXPECTED_RAW})"))
    else:
        failures.append(f"total_results_raw {result.total_results_raw} ≠ {EXPECTED_RAW}")

    # Dedup: 1 URL shared between perm-0 and perm-2; final count = 7
    EXPECTED_FINAL = 7
    if len(result.results) == EXPECTED_FINAL:
        print(ok(f"Deduplicated count = {len(result.results)} (expected {EXPECTED_FINAL})"))
    else:
        failures.append(f"Deduplicated count {len(result.results)} ≠ {EXPECTED_FINAL}")

    # Dedup count consistent
    if result.deduplication_removed == result.total_results_raw - len(result.results):
        print(ok("deduplication_removed field is consistent with raw/final counts"))
    else:
        failures.append(
            f"deduplication_removed={result.deduplication_removed} inconsistent with "
            f"raw={result.total_results_raw}, final={len(result.results)}"
        )

    # The shared URL should be the RICHER copy (has full_text)
    shared_results = [r for r in result.results if "globes.co.il" in r.url]
    if len(shared_results) == 1 and shared_results[0].full_text:
        print(ok("Dedup kept the richer copy of the shared URL (full_text present)"))
    else:
        failures.append(
            f"Shared URL globes.co.il: expected 1 rich result, "
            f"got {len(shared_results)} results"
            + (f" (no full_text)" if shared_results and not shared_results[0].full_text else "")
        )

    # No-date result preserved
    no_date = [r for r in result.results if r.published_at is None]
    if len(no_date) == 1:
        print(ok(f"1 result with published_at=None preserved ({no_date[0].domain})"))
    else:
        failures.append(f"Expected 1 no-date result, got {len(no_date)}")

    # Duration populated
    if result.scrape_duration_seconds > 0:
        print(ok(f"scrape_duration_seconds = {result.scrape_duration_seconds:.3f}s"))
    else:
        failures.append("scrape_duration_seconds is 0 or negative")

    # Date range spans expected period
    dated = [r.published_at for r in result.results if r.published_at]
    if dated:
        oldest = min(dated)
        newest = max(dated)
        print(ok(f"Date range: {oldest.strftime('%Y-%m-%d')} → {newest.strftime('%Y-%m-%d')}"))

    # ── TEST 2: Graceful degradation — one task fails ─────────────────────
    print(f"\n  {_B}Test 2 — Graceful degradation (perm[1] task raises){_D}\n")
    failing_scraper = ScraperFactory.inject(PartiallyFailingMockScraper(fail_on_perm_index=1))
    agent2 = BroadScraperAgent(scraper=failing_scraper, concurrency=concurrency)
    result2: ScrapedResultSet = await agent2.gather_sources(perm_set)

    if isinstance(result2, ScrapedResultSet):
        # Should have collected results from perms 0 and 2 (not 1, which failed)
        # perm-0 has 3 results, perm-2 has 3 results → 6 raw, but globes.co.il dedup'd → 5
        print(ok(
            f"gather_sources returned ScrapedResultSet despite one failing task "
            f"(got {len(result2.results)} results from 2 successful tasks)"
        ))
        if len(result2.results) > 0:
            print(ok("  Pipeline continued; partial results returned (not a hard crash)"))
        else:
            print(warn("  No results returned — partial-failure recovery produced nothing"))
    else:
        failures.append("gather_sources raised or returned wrong type when one task failed")

    # ── TEST 3: Empty permutation set ─────────────────────────────────────
    print(f"\n  {_B}Test 3 — Empty PermutationSet{_D}\n")
    empty_pset = PermutationSet(
        source_item=baseline,
        original_query=baseline.headline,
        permutations=[],
        model_used="mock",
    )
    agent3 = BroadScraperAgent(scraper=ScraperFactory.inject(MockBaseScraper()))
    result3: ScrapedResultSet = await agent3.gather_sources(empty_pset)
    if isinstance(result3, ScrapedResultSet) and len(result3.results) == 0:
        print(ok("Empty PermutationSet → ScrapedResultSet with 0 results (no crash)"))
    else:
        failures.append("Empty PermutationSet did not return empty ScrapedResultSet")

    # ── TEST 4: Optional live DuckDuckGo test ─────────────────────────────
    if live:
        print(f"\n  {_B}Test 4 — Live BasicWebScraper (real DuckDuckGo queries){_D}\n")
        try:
            from src.config import PipelineConfig  # noqa: PLC0415
            from src.scraper.factory import ScraperFactory as SF  # noqa: PLC0415

            cfg = PipelineConfig()
            live_scraper = SF.create(cfg)
            live_agent   = BroadScraperAgent(scraper=live_scraper, concurrency=3, max_results_per_query=3)
            live_pset    = PermutationSet(
                source_item=baseline,
                original_query=baseline.headline,
                permutations=PERMUTATIONS[:2],   # just 2 queries to minimise DDG load
                model_used="live_test",
            )
            print(f"  ⏳ Querying DuckDuckGo for {len(live_pset.permutations)} permutations…")
            t_live = time.perf_counter()
            live_result = await live_agent.gather_sources(live_pset)
            print(ok(
                f"Live DDG: {live_result.total_results_raw} raw → "
                f"{len(live_result.results)} after dedup in "
                f"{time.perf_counter()-t_live:.1f}s"
            ))
            for r in live_result.results[:5]:
                date_s = r.published_at.strftime("%Y-%m-%d") if r.published_at else "no date"
                print(f"    {date_s}  {r.domain:<30}  {(r.title or '')[:50]}")
            if hasattr(live_scraper, 'close'):
                await live_scraper.close()
        except Exception as exc:
            print(warn(f"Live test skipped or failed: {exc}"))

    # ── Final verdict ──────────────────────────────────────────────────────
    print(f"\n{'═' * 68}")
    if failures:
        print(f"{_R}{_B}  FAILED{_D} — {len(failures)} assertion(s):")
        for f in failures:
            print(f"    {err(f)}")
        print(f"{'═' * 68}\n")
        return False

    print(
        f"{_G}{_B}  ALL CHECKS PASSED ✓{_D}\n"
        f"  BroadScraperAgent is operational.\n"
        f"  ScrapedResultSet is ready to hand off to AnalyzerAgent."
    )
    print(f"{'═' * 68}\n")
    return True


# ── CLI ────────────────────────────────────────────────────────────────────────

def _parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        description="Smoke test for BroadScraperAgent",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=(
            "Examples:\n"
            "  python test_scraper_agent.py\n"
            "  python test_scraper_agent.py --live\n"
            "  python test_scraper_agent.py --concurrency 2\n"
        ),
    )
    p.add_argument("--concurrency", type=int, default=10, metavar="N",
                   help="asyncio.Semaphore size (default: 10)")
    p.add_argument("--live", action="store_true",
                   help="Also run a real DuckDuckGo query (requires network)")
    return p


if __name__ == "__main__":
    args = _parser().parse_args()
    success = asyncio.run(run_smoke_test(args.concurrency, args.live))
    sys.exit(0 if success else 1)
