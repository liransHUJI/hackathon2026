"""
Scraper — Strategy Interface
=============================
Abstract base class for all web-scraping backends.

Pattern: Strategy (GoF)
  - BroadScraperAgent depends only on this interface.
  - Concrete implementations (BasicWebScraper, BrightDataScraper) are never
    imported directly in agent code.
  - ScraperFactory.create(config) is the ONLY legal way to obtain a Scraper
    in production code.
  - ScraperFactory.inject(mock) is used in tests.

To add a new backend (e.g. SerperAPI):
  1. Create src/scrapers/serper_scraper.py inheriting from Scraper.
  2. Implement search() and fetch_content().
  3. Add a new ScraperBackend enum value and elif branch in ScraperFactory.
  4. Write tests in tests/scrapers/test_serper_scraper.py.
  ⚠  Never add if/isinstance branches inside BroadScraperAgent.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import Optional

from src.models.schemas import ScrapedResult


class Scraper(ABC):
    """
    Strategy interface for all web scraper implementations.

    Both methods must be async.
    fetch_content() must return None on failure — never raise.
    search() may raise on unrecoverable errors (e.g. auth failure).
    """

    # ── Abstract interface ────────────────────────────────────────────────────

    @abstractmethod
    async def search(
        self,
        query: str,
        max_results: int = 10,
        date_from: Optional[str] = None,
        site_filter: Optional[str] = None,
    ) -> list[ScrapedResult]:
        """
        Submit a search query and return lightweight results.

        Args:
            query:       The search string (one Permutation.text).
            max_results: Upper bound on results returned.
            date_from:   ISO date string "YYYY-MM-DD" to restrict results.
            site_filter: Restrict to a specific domain, e.g. "reddit.com".

        Returns:
            List of ScrapedResult with url, title, snippet, domain populated.
            full_text is NOT populated here — call fetch_content() separately.
        """
        ...

    @abstractmethod
    async def fetch_content(
        self,
        url: str,
        timeout_s: int = 15,
    ) -> Optional[str]:
        """
        Fetch and extract the main body text from a URL.

        Returns:
            Extracted text (max 8 000 chars), or None on any failure.
            Must never raise — log errors internally and return None.
        """
        ...

    @property
    @abstractmethod
    def scraper_id(self) -> str:
        """
        Short identifier stored in ScrapedResult.scraper_id.
        E.g. "basic", "brightdata", "serper".
        """
        ...

    # ── Repr ──────────────────────────────────────────────────────────────────

    def __repr__(self) -> str:
        return f"{self.__class__.__name__}(scraper_id={self.scraper_id!r})"
