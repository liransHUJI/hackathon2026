"""
BaseScraper — high-level batch Strategy interface
==================================================

This is the interface that BroadScraperAgent (and any code that needs scraping)
depends on.  Concrete implementations (BasicWebScraper, BrightDataScraper) live
alongside this file; the ScraperFactory chooses which to instantiate at runtime.

Contract
────────
  scrape(permutations)  →  list[ScrapedResult]
    Drive all permutations through the chosen search backend, deduplicate by
    normalised URL, and return a flat result list ready for the Similarity
    Analyzer.  This is the method your agent calls.

  fetch_page(url)  →  str | None
    Fetch the full body text of a single URL.  Returns None on any failure —
    never raises.  Called selectively on the top-N results after scrape().

  backend_name  →  str
    Short identifier stored in every ScrapedResult.scraper_id produced by this
    instance.  Switching backends is one env-var change, zero code changes.

Helper methods provided on the base class (no need to override):
  _normalise_url(url)    →  str         strip UTM / tracking params, lowercase
  _deduplicate(results)  →  list[...]   keep richest result per normalised URL
  _extract_domain(url)   →  str         "https://www.nytimes.com/…" → "nytimes.com"

To add a new backend:
  1. Create src/scraper/<name>.py and inherit from BaseScraper.
  2. Implement scrape(), fetch_page(), and backend_name.
  3. Add a branch in ScraperFactory.create().
  4. Write tests in tests/scrapers/test_<name>_scraper.py.
  ⚠  Never branch on scraper type inside BroadScraperAgent.
"""

from __future__ import annotations

import logging
from abc import ABC, abstractmethod
from typing import Optional
from urllib.parse import parse_qs, urlencode, urlparse, urlunparse

from src.models.schemas import Permutation, ScrapedResult

logger = logging.getLogger("provenance.scraper")

# Query-string parameters that carry no semantic meaning and pollute URL dedup.
_UTM_PARAMS: frozenset[str] = frozenset(
    {
        "utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term",
        "utm_id", "fbclid", "gclid", "msclkid", "ref", "referrer",
        "_ga", "mc_cid", "mc_eid",
    }
)


class BaseScraper(ABC):
    """
    Abstract base for all web-scraper backends.

    Subclasses must implement:
      • scrape()       — drive all permutations through the backend
      • fetch_page()   — retrieve full body text for a single URL
      • backend_name   — short string identifier (e.g. "basic", "brightdata")
    """

    # ── Abstract interface ────────────────────────────────────────────────────

    @abstractmethod
    async def scrape(
        self,
        permutations: list[Permutation],
        *,
        max_results_per_query: int = 10,
    ) -> list[ScrapedResult]:
        """
        Search for all permutations and return a deduplicated result list.

        Args:
            permutations:          Semantic variants produced by SemanticAgent.
            max_results_per_query: How many results to request per query.

        Returns:
            Flat list of unique ScrapedResults (deduplicated by normalised URL).
            Results are *not* yet ranked or filtered — that is Stage 4's job.
        """
        ...

    @abstractmethod
    async def fetch_page(self, url: str, *, timeout_s: int = 15) -> Optional[str]:
        """
        Fetch and extract the main body text from a URL.

        Returns:
            Extracted plain text (max 8 000 chars), or None on any failure.
            Must never raise — log internally and return None on error.
        """
        ...

    @property
    @abstractmethod
    def backend_name(self) -> str:
        """
        Short identifier stored in every ScrapedResult.scraper_id.
        E.g. "basic", "brightdata".
        """
        ...

    # ── Concrete helpers (shared across all backends) ─────────────────────────

    def _normalise_url(self, url: str) -> str:
        """
        Strip UTM/tracking query params, fragment, trailing slash, lowercase.
        Used as the deduplication key so that identical articles fetched via
        different tracking-tagged links collapse to a single result.
        """
        try:
            parsed = urlparse(url.strip())
            clean_qs = {
                k: v
                for k, v in parse_qs(parsed.query).items()
                if k.lower() not in _UTM_PARAMS
            }
            cleaned = parsed._replace(
                query=urlencode(clean_qs, doseq=True),
                fragment="",
                scheme=parsed.scheme.lower(),
                netloc=parsed.netloc.lower(),
            )
            return urlunparse(cleaned).rstrip("/")
        except Exception:
            return url.strip().lower()

    def _deduplicate(self, results: list[ScrapedResult]) -> list[ScrapedResult]:
        """
        Return one result per normalised URL, keeping the richest version.

        "Richer" means: has full_text > has title > first-seen.
        """
        seen: dict[str, ScrapedResult] = {}
        for r in results:
            key = self._normalise_url(r.url)
            if key not in seen:
                seen[key] = r
            else:
                existing = seen[key]
                # Prefer the result that already has a full page text fetched.
                if r.full_text and not existing.full_text:
                    seen[key] = r
                # Otherwise prefer the result with a title over one without.
                elif r.title and not existing.title:
                    seen[key] = r
        removed = len(results) - len(seen)
        if removed:
            logger.debug("Deduplication removed %d result(s).", removed)
        return list(seen.values())

    def _extract_domain(self, url: str) -> str:
        """
        Extract the bare domain from a URL, stripping "www.".

        "https://www.nytimes.com/politics/…" → "nytimes.com"
        """
        try:
            netloc = urlparse(url).netloc.lower()
            return netloc.removeprefix("www.")
        except Exception:
            return ""

    # ── Repr ──────────────────────────────────────────────────────────────────

    def __repr__(self) -> str:
        return f"{self.__class__.__name__}(backend={self.backend_name!r})"
