"""
Shared pytest fixtures for the Provenance Pipeline test suite.

All fixtures here are available to every test file without importing.

Usage in tests:
    def test_something(sample_news_item, test_config):
        assert sample_news_item.headline == "PM announces retirement"

    async def test_agent(sample_news_item, mock_scraper, test_config):
        agent = MyAgent(name="test", config=test_config)
        result = await agent.process(sample_news_item)
        ...
"""

from __future__ import annotations

from datetime import datetime, timezone
from unittest.mock import AsyncMock

import pytest

from src.config import PipelineConfig
from src.models.schemas import (
    ContentType,
    NewsItem,
    NewsSource,
    Permutation,
    PermutationSet,
    ScrapedResult,
)


# ── Configuration ─────────────────────────────────────────────────────────────

@pytest.fixture
def test_config() -> PipelineConfig:
    """
    Minimal PipelineConfig for tests.
    No real API keys — all external calls must be mocked.
    """
    return PipelineConfig(
        _env_file=None,      # type: ignore[call-arg]
        anthropic_api_key="test-key-not-real",
        llm_model="claude-haiku-4-5-20251001",
        scraper_backend="basic",
        permutation_count=5,  # keep small in tests
        top_candidates=3,
    )


# ── Sample data ───────────────────────────────────────────────────────────────

@pytest.fixture
def sample_news_item() -> NewsItem:
    """A deterministic NewsItem used across all test files."""
    return NewsItem(
        item_id="test-item-001",
        headline="Prime Minister announces surprise retirement from politics",
        source_type=NewsSource.MANUAL,
        source_channel="test",
        published_at=datetime(2026, 5, 25, 12, 0, 0, tzinfo=timezone.utc),
    )


@pytest.fixture
def sample_permutation_set(sample_news_item: NewsItem) -> PermutationSet:
    """A small PermutationSet for testing downstream agents."""
    return PermutationSet(
        source_item=sample_news_item,
        original_query="Prime Minister announces surprise retirement",
        model_used="claude-haiku-4-5-20251001",
        permutations=[
            Permutation(text="Head of government steps down unexpectedly", strategy="entity_swap"),
            Permutation(text="PM quits political career in surprise move", strategy="synonym"),
            Permutation(text="Leader resigns from office amid speculation", strategy="paraphrase"),
        ],
    )


@pytest.fixture
def sample_scraped_result() -> ScrapedResult:
    """A representative ScrapedResult."""
    return ScrapedResult(
        result_id="scraped-001",
        url="https://example-news.com/article/pm-retires",
        title="Prime Minister to retire, sources confirm",
        snippet="The Prime Minister will step down next month after a decade in office.",
        domain="example-news.com",
        content_type=ContentType.ARTICLE,
        published_at=datetime(2026, 5, 20, 9, 0, 0, tzinfo=timezone.utc),
        query_used="Head of government steps down unexpectedly",
        scraper_id="basic",
    )


# ── Mock Scraper ──────────────────────────────────────────────────────────────

@pytest.fixture
def mock_scraper(sample_scraped_result: ScrapedResult) -> AsyncMock:
    """
    An AsyncMock Scraper stub.

    scraper.search()        → [sample_scraped_result]
    scraper.fetch_content() → plain text string
    scraper.scraper_id      → "mock"
    """
    scraper = AsyncMock()
    scraper.scraper_id = "mock"
    scraper.search = AsyncMock(return_value=[sample_scraped_result])
    scraper.fetch_content = AsyncMock(
        return_value=(
            "The Prime Minister will step down next month after a decade in office. "
            "The announcement surprised political observers who had expected the leader "
            "to continue until the next general election."
        )
    )
    return scraper
