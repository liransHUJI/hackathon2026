"""
ScraperFactory
==============
The single, authoritative way to obtain a BaseScraper instance.

  • In production code  →  ScraperFactory.create(config)
  • In tests            →  ScraperFactory.inject(MockScraper())

Switching between free (DuckDuckGo) and premium (Bright Data) scraping is a
one-line .env change: SCRAPER_BACKEND=brightdata.  Zero code changes required
anywhere else, because BroadScraperAgent only ever calls scraper.scrape() and
scraper.fetch_page() through the BaseScraper interface.

Supported backends
──────────────────
  "basic"      BasicWebScraper   DuckDuckGo + httpx + BS4 (default)
  "brightdata" BrightDataScraper Bright Data SERP + Web Unlocker (hackathon day)
"""

from __future__ import annotations

import logging
from enum import Enum
from typing import TYPE_CHECKING

from src.scraper.base import BaseScraper

if TYPE_CHECKING:
    from src.config import PipelineConfig

logger = logging.getLogger("provenance.scraper.factory")


class ScraperBackend(str, Enum):
    BASIC      = "basic"
    BRIGHTDATA = "brightdata"


class ScraperFactory:
    """Constructs the correct BaseScraper implementation from config."""

    @staticmethod
    def create(config: "PipelineConfig") -> BaseScraper:
        """
        Instantiate a scraper based on config.scraper_backend.

        Raises:
            ValueError  if SCRAPER_BACKEND=brightdata but BRIGHTDATA_API_KEY is unset.
            ValueError  if an unknown backend string is configured.
        """
        backend = config.scraper_backend.lower()

        if backend == ScraperBackend.BRIGHTDATA:
            if not config.has_brightdata_key:
                raise ValueError(
                    "SCRAPER_BACKEND=brightdata requires BRIGHTDATA_API_KEY to be set in .env"
                )
            # Import lazily so BrightDataScraper's httpx client isn't created
            # when the factory module is imported (useful for tests).
            from src.scraper.brightdata import BrightDataScraper  # noqa: PLC0415

            logger.info(
                "Using BrightDataScraper (budget cap: $%.2f USD)",
                config.brightdata_budget_usd,
            )
            return BrightDataScraper(
                api_key=config.brightdata_api_key,  # type: ignore[arg-type]
                budget_usd=config.brightdata_budget_usd,
            )

        if backend == ScraperBackend.BASIC:
            from src.scraper.basic import BasicWebScraper  # noqa: PLC0415

            logger.info("Using BasicWebScraper (DuckDuckGo + httpx).")
            return BasicWebScraper(
                max_results_per_query=config.max_scrape_results // max(1, 100),
                max_concurrent_queries=config.scraper_concurrency,
                per_domain_delay_s=1.0 / max(config.domain_rate_limit_rps, 0.1),
            )

        raise ValueError(
            f"Unknown SCRAPER_BACKEND={backend!r}. "
            f"Valid options: {[b.value for b in ScraperBackend]}"
        )

    @staticmethod
    def inject(scraper: BaseScraper) -> BaseScraper:
        """
        Accept any BaseScraper-conforming object.
        Use this in tests to inject a mock without touching PipelineConfig:

            scraper = ScraperFactory.inject(AsyncMock(spec=BaseScraper))
        """
        return scraper
