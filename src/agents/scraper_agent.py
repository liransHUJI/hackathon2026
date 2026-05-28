"""
BroadScraperAgent  —  Stage 3
================================
Fans out all semantic permutations to the web concurrently, deduplicates
the collected results, and returns a single ScrapedResultSet.

Pipeline position
─────────────────
  SemanticAgent  →  [PermutationSet]  →  BroadScraperAgent  →  [ScrapedResultSet]
                                                │
                                         BaseScraper (injected)
                                         ├── BasicWebScraper   (default: DuckDuckGo + httpx)
                                         └── BrightDataScraper (hackathon day drop-in)

Concurrency model
──────────────────
  Each permutation becomes one independent search task.
  All tasks are launched simultaneously via asyncio.gather().
  An asyncio.Semaphore(concurrency) caps the number of tasks allowed to call
  the scraper at the same time, preventing quota exhaustion and rate-limit errors.

  asyncio.gather(..., return_exceptions=True) is used so that one failing
  permutation does not cancel the others — the agent logs the failure and
  continues with whatever results it has.

Strategy Pattern compliance (CLAUDE.md §4 — THE CARDINAL RULE)
────────────────────────────────────────────────────────────────
  This agent holds self.scraper: BaseScraper and calls ONLY:
    • await self.scraper.scrape([perm], max_results_per_query=N)
    • await self.scraper.fetch_page(url)

  The concrete implementation (BasicWebScraper, BrightDataScraper, any future
  backend) is ALWAYS supplied by the caller — never instantiated here directly.

  Production wiring (PipelineRunner):
      scraper = ScraperFactory.create(config)
      agent   = BroadScraperAgent(scraper=scraper, config=config)

  Test / smoke-test injection:
      agent = BroadScraperAgent(scraper=ScraperFactory.inject(mock_scraper))
"""

from __future__ import annotations

import asyncio
import logging
import time
from typing import Any, Optional
from urllib.parse import parse_qs, urlencode, urlparse, urlunparse

from src.agents.base_agent import BaseAgent
from src.config import PipelineConfig
from src.models.schemas import (
    Permutation,
    PermutationSet,
    ScrapedResult,
    ScrapedResultSet,
)
from src.scraper.base import BaseScraper

logger = logging.getLogger("provenance.agent.scraper")

# Query-string keys that carry zero semantic signal — strip before deduplication.
_UTM_STRIP: frozenset[str] = frozenset(
    {
        "utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term",
        "utm_id", "fbclid", "gclid", "msclkid", "ref", "_ga",
    }
)


# ══════════════════════════════════════════════════════════════════════════════
#  BroadScraperAgent
# ══════════════════════════════════════════════════════════════════════════════

class BroadScraperAgent(BaseAgent):
    """
    Stage 3: concurrent multi-query web scraper.

    Concurrently executes one search task per permutation, then flattens and
    deduplicates the results across all queries before handing off to Stage 4.

    Args:
        scraper:               Injected BaseScraper implementation.
                               NEVER import BasicWebScraper here — receive it via
                               ScraperFactory.create(config) or ScraperFactory.inject().
        max_results_per_query: How many results to request per search query.
                               Passed straight through to scraper.scrape().
        concurrency:           Maximum number of simultaneous scraper calls.
                               Maps to the asyncio.Semaphore size.
        input_queue:           Set by PipelineRunner; None for direct use.
        output_queue:          Set by PipelineRunner; None for direct use.
        config:                PipelineConfig; created from .env if not provided.
    """

    def __init__(
        self,
        scraper: BaseScraper,
        name: str = "scraper",
        max_results_per_query: Optional[int] = None,
        concurrency: Optional[int] = None,
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
        self.scraper: BaseScraper = scraper
        # max_results_per_query: explicit arg > config field > sensible default.
        # Using `scraper_concurrency` as a fallback would be semantically wrong, so
        # we use a distinct default constant here.
        self.max_results_per_query: int = (
            max_results_per_query if max_results_per_query is not None else 5
        )
        self.concurrency: int = concurrency or self.config.scraper_concurrency or 10

        # Semaphore created once; each _search_one task acquires before calling
        # the scraper so we never exceed the configured concurrency cap.
        # Note: must be created inside the same event loop that runs the tasks.
        # We create it lazily on the first call to gather_sources() to avoid
        # issues with event-loop switching in tests.
        self._sem: Optional[asyncio.Semaphore] = None

        logger.info(
            "BroadScraperAgent ready — backend=%r  max_results=%d  concurrency=%d",
            self.scraper.backend_name,
            self.max_results_per_query,
            self.concurrency,
        )

    # ── BaseAgent.process (queue path) ────────────────────────────────────────

    async def process(self, item: PermutationSet) -> ScrapedResultSet:  # type: ignore[override]
        """Queue-driven entry point called by PipelineRunner."""
        return await self.gather_sources(item)

    # ── Primary public method ─────────────────────────────────────────────────

    async def gather_sources(self, perm_set: PermutationSet) -> ScrapedResultSet:
        """
        Fan out to all permutations concurrently and return a deduplicated result set.

        Args:
            perm_set: PermutationSet produced by SemanticAgent.  The source_item
                      lineage is preserved in the returned ScrapedResultSet.

        Returns:
            ScrapedResultSet with:
              • results           — deduplicated ScrapedResult objects
              • total_queries_issued
              • total_results_raw — before dedup
              • deduplication_removed
              • scrape_duration_seconds
        """
        t0 = time.perf_counter()

        if not perm_set.permutations:
            logger.warning("BroadScraperAgent received an empty PermutationSet.")
            return self._empty_result_set(perm_set, t0)

        # ── Lazy semaphore creation (bound to the running event loop) ──────────
        if self._sem is None:
            self._sem = asyncio.Semaphore(self.concurrency)

        n_perms = len(perm_set.permutations)
        logger.info(
            "Launching %d concurrent search tasks (semaphore cap=%d)…",
            n_perms, self.concurrency,
        )

        # ── Fan-out: one task per permutation, all running concurrently ────────
        tasks = [self._search_one(perm) for perm in perm_set.permutations]
        nested: list[Any] = await asyncio.gather(*tasks, return_exceptions=True)

        # ── Collect results; log (but do not re-raise) per-task failures ───────
        all_results: list[ScrapedResult] = []
        n_failed = 0
        for i, batch in enumerate(nested):
            if isinstance(batch, BaseException):
                logger.warning(
                    "Search task for permutation[%d] '%s…' failed: %s",
                    i,
                    perm_set.permutations[i].text[:50],
                    batch,
                )
                n_failed += 1
                continue
            all_results.extend(batch)

        total_raw = len(all_results)

        # ── Agent-level deduplication across all permutation results ──────────
        deduplicated = _agent_deduplicate(all_results)
        n_removed = total_raw - len(deduplicated)
        elapsed = time.perf_counter() - t0

        logger.info(
            "BroadScraperAgent done: %d/%d queries succeeded, "
            "%d raw → %d after dedup in %.2fs",
            n_perms - n_failed, n_perms,
            total_raw, len(deduplicated),
            elapsed,
        )

        return ScrapedResultSet(
            source_permutation_set = perm_set,
            results                = deduplicated,
            total_queries_issued   = n_perms,
            total_results_raw      = total_raw,
            deduplication_removed  = n_removed,
            scrape_duration_seconds= elapsed,
        )

    # ── Internal task ─────────────────────────────────────────────────────────

    async def _search_one(self, perm: Permutation) -> list[ScrapedResult]:
        """
        Semaphore-gated search for a single permutation.

        Acquires the semaphore before calling the scraper so that at most
        self.concurrency tasks are calling the network at the same time.
        Raises on error (caught by asyncio.gather return_exceptions=True).
        """
        assert self._sem is not None  # set by gather_sources before tasks start
        async with self._sem:
            results = await self.scraper.scrape(
                [perm],
                max_results_per_query=self.max_results_per_query,
            )
            logger.debug(
                "  '%s…' → %d result(s)",
                perm.text[:60], len(results),
            )
            return results

    # ── Utilities ─────────────────────────────────────────────────────────────

    def _empty_result_set(self, perm_set: PermutationSet, t0: float) -> ScrapedResultSet:
        return ScrapedResultSet(
            source_permutation_set  = perm_set,
            results                 = [],
            total_queries_issued    = 0,
            total_results_raw       = 0,
            deduplication_removed   = 0,
            scrape_duration_seconds = time.perf_counter() - t0,
        )


# ══════════════════════════════════════════════════════════════════════════════
#  Module-level deduplication helpers  (stateless, independently unit-testable)
# ══════════════════════════════════════════════════════════════════════════════

def _normalise_url(url: str) -> str:
    """
    Strip UTM/tracking params, fragment, and trailing slash; lowercase scheme+host.

    Identical to BaseScraper._normalise_url — kept as a standalone function here
    so the agent can deduplicate without accessing a protected method.
    """
    try:
        parsed = urlparse(url.strip())
        clean_qs = {
            k: v
            for k, v in parse_qs(parsed.query).items()
            if k.lower() not in _UTM_STRIP
        }
        cleaned = parsed._replace(
            scheme   = parsed.scheme.lower(),
            netloc   = parsed.netloc.lower(),
            query    = urlencode(clean_qs, doseq=True),
            fragment = "",
        )
        return urlunparse(cleaned).rstrip("/")
    except Exception:
        return url.strip().lower()


def _agent_deduplicate(results: list[ScrapedResult]) -> list[ScrapedResult]:
    """
    Collapse duplicate URLs from different permutation queries into one result.

    When the same article is found via multiple permutations (expected — that is
    the point of permutation search), keep the richest copy:
      full_text present > title present > first encountered.

    Args:
        results: Flat list of ScrapedResult objects from all permutation queries.

    Returns:
        De-duplicated list, preserving insertion order of first encounter.
    """
    seen: dict[str, ScrapedResult] = {}
    for r in results:
        key = _normalise_url(r.url)
        if key not in seen:
            seen[key] = r
        else:
            existing = seen[key]
            # Prefer richer: full_text > title > first-seen
            if r.full_text and not existing.full_text:
                seen[key] = r
            elif r.title and not existing.title:
                seen[key] = r

    n_removed = len(results) - len(seen)
    if n_removed:
        logger.debug("Agent dedup removed %d duplicate(s) across permutations.", n_removed)
    return list(seen.values())
