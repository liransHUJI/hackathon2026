"""
BasicWebScraper
===============
MVP scraper backend — no paid credentials required.

Search  : DuckDuckGo (via duckduckgo_search.DDGS) — free, no API key.
          The library is synchronous, so it's wrapped with run_in_executor
          to keep the asyncio event loop unblocked.

Fetch   : httpx.AsyncClient — async HTTP with follow-redirects.
Parse   : BeautifulSoup (html.parser) — strips chrome, returns main body text.

Rate limiting
─────────────
An asyncio.Semaphore(max_concurrent) limits how many DDG searches run at once
(default 5).  A per-domain delay (default 1 s) is applied before every
fetch_page() call to be polite to target sites.
Both values are configurable on construction or from PipelineConfig.

Drop-in replacement
────────────────────
When SCRAPER_BACKEND=brightdata is set in .env, ScraperFactory will return a
BrightDataScraper instead.  BroadScraperAgent never knows which is in use.
"""

from __future__ import annotations

import asyncio
import logging
from datetime import datetime, timezone
from typing import Optional
from urllib.parse import urlparse

import httpx
from bs4 import BeautifulSoup

from src.models.schemas import ContentType, Permutation, ScrapedResult
from src.scraper.base import BaseScraper

logger = logging.getLogger("provenance.scraper.basic")

_MAX_BODY_CHARS = 8_000   # LLM context budget for full-text fields
_USER_AGENT = (
    "Mozilla/5.0 (compatible; ProvenancePipelineBot/0.1; "
    "+https://github.com/provenance-pipeline)"
)


class BasicWebScraper(BaseScraper):
    """
    Free-tier scraper: DuckDuckGo search + httpx page fetching.

    Instantiate once per pipeline run and reuse across all permutations so
    that httpx's connection pool is shared (faster, fewer handshakes).

    Args:
        max_results_per_query:  Results requested from DDG per query string.
        max_concurrent_queries: asyncio.Semaphore size for parallel DDG calls.
        per_domain_delay_s:     Seconds to wait between fetches to the same domain.
        timeout_s:              httpx request timeout in seconds.
    """

    def __init__(
        self,
        max_results_per_query: int = 10,
        max_concurrent_queries: int = 5,
        per_domain_delay_s: float = 1.0,
        timeout_s: int = 15,
    ) -> None:
        self.max_results_per_query = max_results_per_query
        self.per_domain_delay_s = per_domain_delay_s
        self.timeout_s = timeout_s
        self._semaphore = asyncio.Semaphore(max_concurrent_queries)
        self._client = httpx.AsyncClient(
            headers={"User-Agent": _USER_AGENT},
            timeout=httpx.Timeout(timeout_s),
            follow_redirects=True,
        )
        # Tracks the last fetch time per domain for polite rate limiting.
        self._last_fetch: dict[str, float] = {}

    # ── Abstract implementation ───────────────────────────────────────────────

    @property
    def backend_name(self) -> str:
        return "basic"

    async def scrape(
        self,
        permutations: list[Permutation],
        *,
        max_results_per_query: int | None = None,
    ) -> list[ScrapedResult]:
        """
        Run a DDG search for every permutation (concurrency-limited),
        collect all results, and deduplicate by normalised URL.

        Returns a flat list of ScrapedResult objects.
        full_text is NOT populated here; call fetch_page() on the URLs you
        care about after ranking (Stage 4 → Stage 5 boundary).
        """
        n = max_results_per_query or self.max_results_per_query

        tasks = [self._search_one(perm, n) for perm in permutations]
        nested: list[list[ScrapedResult]] = await asyncio.gather(*tasks)

        all_results: list[ScrapedResult] = [r for batch in nested for r in batch]
        return self._deduplicate(all_results)

    async def fetch_page(self, url: str, *, timeout_s: int = 15) -> Optional[str]:
        """
        Fetch full body text from a URL using httpx + BeautifulSoup.

        Strips <script>, <style>, <nav>, <footer>, <header> before extracting
        text.  Truncates to _MAX_BODY_CHARS.  Returns None on any failure.
        """
        await self._polite_delay(url)
        try:
            resp = await self._client.get(url, timeout=timeout_s)
            resp.raise_for_status()
            return self._extract_text(resp.text)
        except httpx.HTTPStatusError as exc:
            logger.warning("HTTP %s for %s", exc.response.status_code, url)
            return None
        except httpx.RequestError as exc:
            logger.warning("Request error fetching %s: %s", url, exc)
            return None
        except Exception as exc:
            logger.warning("Unexpected error fetching %s: %s", url, exc)
            return None

    # ── Internal search helpers ───────────────────────────────────────────────

    async def _search_one(
        self, perm: Permutation, max_results: int
    ) -> list[ScrapedResult]:
        """Run a single DDG query and map results to ScrapedResult objects."""
        async with self._semaphore:
            loop = asyncio.get_event_loop()
            try:
                raw: list[dict] = await loop.run_in_executor(
                    None, self._ddg_search, perm.text, max_results
                )
            except Exception as exc:
                logger.warning(
                    "DDG search failed for query '%s…': %s",
                    perm.text[:60],
                    exc,
                )
                return []
        return [self._map_ddg_result(row, perm.text) for row in raw]

    def _ddg_search(self, query: str, max_results: int) -> list[dict]:
        """
        Synchronous DuckDuckGo search (run inside a thread via run_in_executor).

        Returns a list of dicts with keys: "title", "href", "body".
        Importing DDGS here (inside the method) means the package is only
        required at runtime, not at import time — easier to mock in tests.
        """
        try:
            from ddgs import DDGS  # new package name (successor to duckduckgo_search)
        except ImportError:
            from duckduckgo_search import DDGS  # type: ignore[no-redef]

        results = list(DDGS().text(query, max_results=max_results))
        logger.debug("DDG returned %d result(s) for '%s…'", len(results), query[:60])
        return results

    def _map_ddg_result(self, raw: dict, query_used: str) -> ScrapedResult:
        """Convert a raw DDG result dict to a typed ScrapedResult."""
        url: str = raw.get("href", "")
        return ScrapedResult(
            url=url,
            title=raw.get("title") or None,
            snippet=raw.get("body", ""),
            domain=self._extract_domain(url),
            content_type=ContentType.ARTICLE,
            published_at=self._parse_date(raw.get("date")),
            query_used=query_used,
            scraper_id=self.backend_name,
        )

    # ── Internal fetch helpers ────────────────────────────────────────────────

    def _extract_text(self, html: str) -> Optional[str]:
        """Parse HTML and return clean plain text, or None if blank."""
        soup = BeautifulSoup(html, "html.parser")
        for tag in soup(["script", "style", "nav", "footer", "header", "aside"]):
            tag.decompose()

        # Prefer semantic content wrappers; fall back to full body.
        container = (
            soup.find("article")
            or soup.find("main")
            or soup.find(id="content")
            or soup.find(class_="content")
            or soup.body
        )
        if container is None:
            return None

        text = container.get_text(separator=" ", strip=True)
        text = " ".join(text.split())   # collapse internal whitespace
        return text[:_MAX_BODY_CHARS] if text else None

    async def _polite_delay(self, url: str) -> None:
        """Sleep if we've fetched from this domain too recently."""
        import time

        domain = self._extract_domain(url)
        last = self._last_fetch.get(domain, 0.0)
        elapsed = time.monotonic() - last
        gap = self.per_domain_delay_s - elapsed
        if gap > 0:
            await asyncio.sleep(gap)
        self._last_fetch[domain] = time.monotonic()

    # ── Static helpers ────────────────────────────────────────────────────────

    @staticmethod
    def _parse_date(raw: Optional[str]) -> Optional[datetime]:
        """
        Best-effort parse of DDG's optional date string.
        Returns None rather than crashing if the format is unexpected.
        """
        if not raw:
            return None
        try:
            from dateutil import parser as dateparser  # type: ignore[import-untyped]

            return dateparser.parse(raw).replace(tzinfo=timezone.utc)
        except Exception:
            return None

    # ── Lifecycle ─────────────────────────────────────────────────────────────

    async def close(self) -> None:
        """Close the shared httpx client. Call when the pipeline shuts down."""
        await self._client.aclose()

    async def __aenter__(self) -> "BasicWebScraper":
        return self

    async def __aexit__(self, *_: object) -> None:
        await self.close()
