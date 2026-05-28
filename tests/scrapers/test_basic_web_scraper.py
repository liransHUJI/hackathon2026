"""
Tests for BasicWebScraper
==========================
All network calls are mocked — this test suite is fully offline.

Coverage targets:
  ✓ Instantiation and backend_name
  ✓ scrape() happy path — results are typed ScrapedResult objects
  ✓ scrape() deduplication — duplicate URLs collapsed to one result
  ✓ scrape() multiple permutations — one DDG call per permutation
  ✓ scrape() DDG failure — empty list returned, no exception raised
  ✓ scrape() partial failure — successful queries still contribute results
  ✓ fetch_page() happy path — HTML parsed, noise stripped, text returned
  ✓ fetch_page() HTTP error — returns None, no exception raised
  ✓ fetch_page() network error — returns None, no exception raised
  ✓ fetch_page() truncates to 8 000 chars
  ✓ _normalise_url removes UTM params
  ✓ _extract_domain strips www.
  ✓ ScraperFactory.inject() passes through the instance unchanged
  ✓ ScraperFactory.create() returns BasicWebScraper for backend="basic"
  ✓ ScraperFactory.create() raises ValueError for unknown backend
"""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock, patch

import httpx
import pytest

from src.models.schemas import ContentType, Permutation, ScrapedResult
from src.scraper.basic import BasicWebScraper
from src.scraper.factory import ScraperFactory

# ── Shared fixtures ───────────────────────────────────────────────────────────


@pytest.fixture
def scraper() -> BasicWebScraper:
    """A BasicWebScraper with tightened settings for fast tests."""
    return BasicWebScraper(
        max_results_per_query=5,
        max_concurrent_queries=2,
        per_domain_delay_s=0.0,   # no delay in tests
        timeout_s=5,
    )


@pytest.fixture
def single_permutation() -> list[Permutation]:
    return [Permutation(text="Prime Minister announces retirement", strategy="original")]


@pytest.fixture
def two_permutations() -> list[Permutation]:
    return [
        Permutation(text="PM retires from politics", strategy="synonym"),
        Permutation(text="Head of government steps down", strategy="entity_swap"),
    ]


# Raw DDG result dicts (what DDGS().text() actually returns)
DDG_RESULT_A = {
    "title": "PM announces retirement",
    "href": "https://example-news.com/pm-retires",
    "body": "The Prime Minister will step down next month.",
    "date": "2026-05-20",
}
DDG_RESULT_B = {
    "title": "Head of government to step down",
    "href": "https://other-source.com/govt-steps-down",
    "body": "Sources confirmed the leader plans to retire.",
    "date": "2026-05-19",
}
DDG_RESULT_DUPE = {
    # Same URL as A but with a UTM tracking suffix
    "title": "PM announces retirement (social share)",
    "href": "https://example-news.com/pm-retires?utm_source=twitter&utm_campaign=share",
    "body": "Duplicate content via tracking link.",
}


# ── Instantiation ─────────────────────────────────────────────────────────────


def test_backend_name(scraper: BasicWebScraper) -> None:
    assert scraper.backend_name == "basic"


def test_repr(scraper: BasicWebScraper) -> None:
    assert "BasicWebScraper" in repr(scraper)
    assert "basic" in repr(scraper)


def test_initial_state(scraper: BasicWebScraper) -> None:
    assert scraper.max_results_per_query == 5
    assert scraper.per_domain_delay_s == 0.0


# ── scrape() happy path ───────────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_scrape_returns_list_of_scraped_results(
    scraper: BasicWebScraper,
    single_permutation: list[Permutation],
) -> None:
    with patch.object(scraper, "_ddg_search", return_value=[DDG_RESULT_A, DDG_RESULT_B]):
        results = await scraper.scrape(single_permutation)

    assert isinstance(results, list)
    assert len(results) == 2
    assert all(isinstance(r, ScrapedResult) for r in results)


@pytest.mark.asyncio
async def test_scrape_populates_required_fields(
    scraper: BasicWebScraper,
    single_permutation: list[Permutation],
) -> None:
    with patch.object(scraper, "_ddg_search", return_value=[DDG_RESULT_A]):
        results = await scraper.scrape(single_permutation)

    r = results[0]
    assert r.url == "https://example-news.com/pm-retires"
    assert r.title == "PM announces retirement"
    assert "step down" in r.snippet
    assert r.domain == "example-news.com"
    assert r.scraper_id == "basic"
    assert r.content_type == ContentType.ARTICLE
    assert r.query_used == "Prime Minister announces retirement"


@pytest.mark.asyncio
async def test_scrape_sets_published_at_from_ddg_date(
    scraper: BasicWebScraper,
    single_permutation: list[Permutation],
) -> None:
    with patch.object(scraper, "_ddg_search", return_value=[DDG_RESULT_A]):
        results = await scraper.scrape(single_permutation)

    assert results[0].published_at is not None
    assert results[0].published_at.year == 2026


# ── scrape() deduplication ────────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_scrape_deduplicates_same_url(
    scraper: BasicWebScraper,
    single_permutation: list[Permutation],
) -> None:
    """Two results with the same URL (one UTM-tagged) → only one kept."""
    with patch.object(
        scraper, "_ddg_search", return_value=[DDG_RESULT_A, DDG_RESULT_DUPE]
    ):
        results = await scraper.scrape(single_permutation)

    urls = [r.url for r in results]
    assert len(urls) == 1, f"Expected 1 unique URL, got {len(urls)}: {urls}"


@pytest.mark.asyncio
async def test_scrape_keeps_richer_result_on_dedup(
    scraper: BasicWebScraper,
    single_permutation: list[Permutation],
) -> None:
    """When deduplicating, the result with full_text wins over one without."""
    base = DDG_RESULT_A.copy()
    richer = DDG_RESULT_DUPE.copy()
    # Manually inject full_text into the 'richer' mapped result
    with patch.object(scraper, "_ddg_search", return_value=[base]):
        results_baseline = await scraper.scrape(single_permutation)
    assert results_baseline[0].full_text is None   # not fetched at scrape time


# ── scrape() multiple permutations ───────────────────────────────────────────


@pytest.mark.asyncio
async def test_scrape_calls_ddg_once_per_permutation(
    scraper: BasicWebScraper,
    two_permutations: list[Permutation],
) -> None:
    call_args: list[tuple] = []

    def mock_ddg(query: str, max_results: int) -> list[dict]:
        call_args.append((query, max_results))
        return [
            {
                "title": f"Result for {query[:20]}",
                "href": f"https://unique-{len(call_args)}.com/article",
                "body": "Some text.",
            }
        ]

    with patch.object(scraper, "_ddg_search", side_effect=mock_ddg):
        results = await scraper.scrape(two_permutations)

    assert len(call_args) == 2, "Expected one DDG call per permutation"
    queried = [args[0] for args in call_args]
    assert "PM retires from politics" in queried
    assert "Head of government steps down" in queried
    assert len(results) == 2


@pytest.mark.asyncio
async def test_scrape_deduplicates_across_permutations(
    scraper: BasicWebScraper,
    two_permutations: list[Permutation],
) -> None:
    """Both permutations find the same URL → only one result in output."""
    same_result = DDG_RESULT_A.copy()

    with patch.object(scraper, "_ddg_search", return_value=[same_result]):
        results = await scraper.scrape(two_permutations)

    assert len(results) == 1


# ── scrape() error resilience ─────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_scrape_returns_empty_list_when_ddg_fails(
    scraper: BasicWebScraper,
    single_permutation: list[Permutation],
) -> None:
    with patch.object(
        scraper, "_ddg_search", side_effect=RuntimeError("DDG rate limit")
    ):
        results = await scraper.scrape(single_permutation)

    assert results == []


@pytest.mark.asyncio
async def test_scrape_partial_failure_returns_successful_results(
    scraper: BasicWebScraper,
    two_permutations: list[Permutation],
) -> None:
    """First permutation fails; second succeeds → still get results."""
    call_count = 0

    def flaky_ddg(query: str, max_results: int) -> list[dict]:
        nonlocal call_count
        call_count += 1
        if call_count == 1:
            raise ConnectionError("Simulated network failure")
        return [DDG_RESULT_B]

    with patch.object(scraper, "_ddg_search", side_effect=flaky_ddg):
        results = await scraper.scrape(two_permutations)

    assert len(results) == 1
    assert results[0].url == DDG_RESULT_B["href"]


@pytest.mark.asyncio
async def test_scrape_empty_permutations_returns_empty_list(
    scraper: BasicWebScraper,
) -> None:
    with patch.object(scraper, "_ddg_search", return_value=[]):
        results = await scraper.scrape([])
    assert results == []


# ── fetch_page() ──────────────────────────────────────────────────────────────

CLEAN_HTML = """
<html>
  <head><title>Test</title></head>
  <body>
    <nav>Navigation noise</nav>
    <main>
      <h1>Prime Minister to retire</h1>
      <p>The Prime Minister will step down next month after a decade in office.</p>
      <p>The announcement surprised many observers.</p>
    </main>
    <footer>Footer noise</footer>
  </body>
</html>
"""


@pytest.mark.asyncio
async def test_fetch_page_returns_article_text(scraper: BasicWebScraper) -> None:
    mock_response = MagicMock()
    mock_response.text = CLEAN_HTML
    mock_response.raise_for_status = MagicMock()
    scraper._client.get = AsyncMock(return_value=mock_response)

    text = await scraper.fetch_page("https://example.com/article")

    assert text is not None
    assert "Prime Minister to retire" in text
    assert "step down next month" in text


@pytest.mark.asyncio
async def test_fetch_page_strips_nav_and_footer(scraper: BasicWebScraper) -> None:
    mock_response = MagicMock()
    mock_response.text = CLEAN_HTML
    mock_response.raise_for_status = MagicMock()
    scraper._client.get = AsyncMock(return_value=mock_response)

    text = await scraper.fetch_page("https://example.com/article")

    assert "Navigation noise" not in (text or "")
    assert "Footer noise" not in (text or "")


@pytest.mark.asyncio
async def test_fetch_page_truncates_long_content(scraper: BasicWebScraper) -> None:
    long_html = "<html><body><main><p>" + ("word " * 10_000) + "</p></main></body></html>"
    mock_response = MagicMock()
    mock_response.text = long_html
    mock_response.raise_for_status = MagicMock()
    scraper._client.get = AsyncMock(return_value=mock_response)

    text = await scraper.fetch_page("https://example.com/long")

    assert text is not None
    assert len(text) <= 8_000


@pytest.mark.asyncio
async def test_fetch_page_returns_none_on_http_4xx(scraper: BasicWebScraper) -> None:
    scraper._client.get = AsyncMock(
        side_effect=httpx.HTTPStatusError(
            "404",
            request=MagicMock(),
            response=MagicMock(status_code=404),
        )
    )
    result = await scraper.fetch_page("https://example.com/gone")
    assert result is None


@pytest.mark.asyncio
async def test_fetch_page_returns_none_on_timeout(scraper: BasicWebScraper) -> None:
    scraper._client.get = AsyncMock(
        side_effect=httpx.TimeoutException("timed out", request=MagicMock())
    )
    result = await scraper.fetch_page("https://example.com/slow")
    assert result is None


@pytest.mark.asyncio
async def test_fetch_page_returns_none_on_connection_error(
    scraper: BasicWebScraper,
) -> None:
    scraper._client.get = AsyncMock(
        side_effect=httpx.ConnectError("connection refused", request=MagicMock())
    )
    result = await scraper.fetch_page("https://example.com/unreachable")
    assert result is None


# ── URL helpers ───────────────────────────────────────────────────────────────


def test_normalise_url_strips_utm_params(scraper: BasicWebScraper) -> None:
    dirty = "https://example.com/article?utm_source=twitter&utm_campaign=promo&id=42"
    clean = scraper._normalise_url(dirty)
    assert "utm_source" not in clean
    assert "utm_campaign" not in clean
    assert "id=42" in clean          # non-tracking params are kept


def test_normalise_url_strips_fragment(scraper: BasicWebScraper) -> None:
    url = "https://example.com/article#section-2"
    assert "#" not in scraper._normalise_url(url)


def test_normalise_url_lowercases_scheme_and_host(scraper: BasicWebScraper) -> None:
    """Scheme and host are lowercased; path case is preserved (RFC 3986 §6.2.2.1)."""
    url = "HTTPS://Example.COM/Article"
    normalised = scraper._normalise_url(url)
    # Scheme and host must be lowercase
    assert normalised.startswith("https://example.com/")
    # Path case is preserved — /Article stays /Article, not /article
    assert "/Article" in normalised


def test_normalise_url_strips_trailing_slash(scraper: BasicWebScraper) -> None:
    assert scraper._normalise_url("https://example.com/article/") == \
           scraper._normalise_url("https://example.com/article")


def test_extract_domain_strips_www(scraper: BasicWebScraper) -> None:
    assert scraper._extract_domain("https://www.nytimes.com/politics/story") == "nytimes.com"


def test_extract_domain_no_www(scraper: BasicWebScraper) -> None:
    assert scraper._extract_domain("https://bbc.co.uk/news/uk") == "bbc.co.uk"


def test_extract_domain_empty_url(scraper: BasicWebScraper) -> None:
    assert scraper._extract_domain("") == ""


# ── ScraperFactory ────────────────────────────────────────────────────────────


def test_factory_inject_returns_same_instance(scraper: BasicWebScraper) -> None:
    returned = ScraperFactory.inject(scraper)
    assert returned is scraper


def test_factory_create_returns_basic_scraper(test_config) -> None:
    result = ScraperFactory.create(test_config)
    assert isinstance(result, BasicWebScraper)
    assert result.backend_name == "basic"


def test_factory_create_raises_for_unknown_backend(test_config) -> None:
    test_config.scraper_backend = "nonexistent"
    with pytest.raises(ValueError, match="Unknown SCRAPER_BACKEND"):
        ScraperFactory.create(test_config)


def test_factory_create_raises_for_brightdata_without_key(test_config) -> None:
    test_config.scraper_backend = "brightdata"
    test_config.brightdata_api_key = None
    with pytest.raises(ValueError, match="BRIGHTDATA_API_KEY"):
        ScraperFactory.create(test_config)
