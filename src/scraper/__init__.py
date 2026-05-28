"""
src/scraper — high-level batch scraping layer (Strategy Pattern).

Two-level scraper design
─────────────────────────
  src/scrapers/base_scraper.py  ←  low-level: Scraper(ABC) — one query at a time
  src/scraper/base.py           ←  this package: BaseScraper(ABC) — drives all
                                     permutations, owns deduplication, returns
                                     a single unified list[ScrapedResult].

Implementations:
  BasicWebScraper   DuckDuckGo + httpx + BeautifulSoup (free, default)
  BrightDataScraper Bright Data SERP + Web Unlocker     (hackathon day, $75 cap)

Usage (production):
  scraper = ScraperFactory.create(config)
  results = await scraper.scrape(permutation_set.permutations)

Usage (tests):
  scraper = ScraperFactory.inject(MockScraper())
  results = await scraper.scrape(...)
"""

from src.scraper.base import BaseScraper
from src.scraper.factory import ScraperFactory

__all__ = ["BaseScraper", "ScraperFactory"]
