"""Scraper implementations — Strategy Pattern.

Never import BasicWebScraper or BrightDataScraper directly in agent code.
Always use ScraperFactory.create(config) or ScraperFactory.inject(mock).
"""

from src.scrapers.base_scraper import Scraper

__all__ = ["Scraper"]
