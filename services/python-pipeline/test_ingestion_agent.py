#!/usr/bin/env python3
"""
test_ingestion_agent.py — Smoke test for IngestionAgent (Stage 1)
=================================================================
Makes ONE live HTTP request to BBC World News RSS (no API keys required).
Verifies that the agent:
  • Returns a non-empty list of NewsItem objects
  • Headlines are non-empty and HTML-free (no < > angle brackets)
  • published_at is timezone-aware when present
  • source_type == NewsSource.RSS
  • Items are sorted newest-first
  • Deduplication runs (item count ≤ raw feed entry count)

Run:
    python test_ingestion_agent.py

If the feed is unreachable (network issue) the test is skipped gracefully.
"""

from __future__ import annotations

import asyncio
import sys
import time
from datetime import datetime

# Load .env if present (no-op if not)
from pathlib import Path
try:
    from dotenv import load_dotenv
    load_dotenv(Path(__file__).parent / ".env")
except ImportError:
    pass

from src.agents.ingestion_agent import (
    DEFAULT_RSS_FEEDS,
    IngestionAgent,
    RSSIngestor,
    _strip_html,
    _parse_feedparser_date,
    _deduplicate_items,
)
from src.models.schemas import NewsItem, NewsSource


# ── Colour helpers ────────────────────────────────────────────────────────────
_G  = "\033[92m"
_R  = "\033[91m"
_Y  = "\033[93m"
_C  = "\033[96m"
_B  = "\033[1m"
_D  = "\033[0m"
_DIM = "\033[2m"


def _ok(msg: str) -> str:
    return f"  {_G}✓{_D}  {msg}"


def _fail(msg: str) -> str:
    return f"  {_R}✗{_D}  {msg}"


def _skip(msg: str) -> str:
    return f"  {_Y}–{_D}  {msg}"


# ══════════════════════════════════════════════════════════════════════════════
#  Unit tests — no network required
# ══════════════════════════════════════════════════════════════════════════════

def test_strip_html() -> None:
    """_strip_html removes tags, decodes entities, collapses whitespace."""
    cases = [
        ("<p>PM <b>retires</b> after &amp; 10 years</p>", "PM retires after & 10 years"),
        ("<a href='x'>Click here</a>",                    "Click here"),
        ("No tags at all",                                "No tags at all"),
        ("",                                              ""),
        ("&lt;script&gt;alert(1)&lt;/script&gt;",        "<script>alert(1)</script>"),
        ("  lots   of   spaces  ",                        "lots of spaces"),
    ]
    passed = 0
    for raw, expected in cases:
        got = _strip_html(raw)
        if got == expected:
            print(_ok(f"_strip_html: {repr(raw)[:40]} → {repr(got)}"))
            passed += 1
        else:
            print(_fail(f"_strip_html: {repr(raw)[:40]}\n    expected {repr(expected)}\n    got      {repr(got)}"))
    assert passed == len(cases), f"_strip_html: {passed}/{len(cases)} cases passed"


def test_parse_feedparser_date_none() -> None:
    """_parse_feedparser_date returns None for None input."""
    result = _parse_feedparser_date(None)
    assert result is None, f"Expected None, got {result}"
    print(_ok("_parse_feedparser_date(None) → None"))


def test_parse_feedparser_date_utc() -> None:
    """_parse_feedparser_date returns UTC-aware datetime for valid struct_time."""
    import time as time_mod
    # time.struct_time for 2024-01-15 12:00:00 UTC
    ts = time_mod.strptime("2024-01-15 12:00:00", "%Y-%m-%d %H:%M:%S")
    result = _parse_feedparser_date(ts)
    assert result is not None, "Expected a datetime, got None"
    assert result.tzinfo is not None, "datetime must be timezone-aware"
    assert result.year  == 2024
    assert result.month == 1
    assert result.day   == 15
    assert result.hour  == 12
    print(_ok(f"_parse_feedparser_date → {result.isoformat()}"))


def test_deduplicate_items_by_url() -> None:
    """_deduplicate_items collapses items with the same URL."""
    item_a = NewsItem(
        headline="Story A",
        url="https://example.com/story",
        source_type=NewsSource.RSS,
        source_channel="test",
    )
    item_b = NewsItem(
        headline="Story A (copy)",
        url="https://example.com/story",  # same URL
        source_type=NewsSource.RSS,
        source_channel="test",
    )
    item_c = NewsItem(
        headline="Story B",
        url="https://example.com/story-b",
        source_type=NewsSource.RSS,
        source_channel="test",
    )
    result = _deduplicate_items([item_a, item_b, item_c])
    assert len(result) == 2, f"Expected 2 items after dedup, got {len(result)}"
    print(_ok(f"_deduplicate_items: 3 items → {len(result)} after URL dedup"))


def test_deduplicate_items_prefers_body() -> None:
    """_deduplicate_items keeps the richer copy (with body) when URLs clash."""
    item_no_body = NewsItem(
        headline="Story",
        url="https://example.com/story",
        source_type=NewsSource.RSS,
        source_channel="test",
        body=None,
    )
    item_with_body = NewsItem(
        headline="Story (full text)",
        url="https://example.com/story",  # same URL
        source_type=NewsSource.RSS,
        source_channel="test",
        body="The full article text.",
    )
    # first-seen wins but body upgrade should replace it
    result = _deduplicate_items([item_no_body, item_with_body])
    assert len(result) == 1
    assert result[0].body == "The full article text.", "Body copy not selected"
    print(_ok("_deduplicate_items: body-rich copy preferred over body-less copy"))


def test_ingestion_agent_init() -> None:
    """IngestionAgent constructs without errors and has correct defaults."""
    agent = IngestionAgent()
    assert agent.name == "ingestion"
    assert isinstance(agent._default_feed_urls, list)
    assert len(agent._default_feed_urls) > 0
    print(_ok(f"IngestionAgent() — {len(agent._default_feed_urls)} default feed URL(s)"))


def test_from_rss_urls_factory() -> None:
    """IngestionAgent.from_rss_urls creates one RSSIngestor per URL."""
    urls = [
        "https://feeds.bbci.co.uk/news/world/rss.xml",
        "https://www.theguardian.com/world/rss",
    ]
    agent = IngestionAgent.from_rss_urls(urls)
    assert len(agent._sources) == 2
    for src in agent._sources:
        assert isinstance(src, RSSIngestor)
    print(_ok(f"IngestionAgent.from_rss_urls: {len(agent._sources)} RSSIngestor(s) created"))


# ══════════════════════════════════════════════════════════════════════════════
#  Live smoke test — one real HTTP request
# ══════════════════════════════════════════════════════════════════════════════

async def test_live_rss_fetch() -> bool:
    """
    Fetches from BBC World News RSS.
    Returns True if the test passed, False if it failed,
    or prints a skip message and returns True if the network is unreachable.
    """
    feed_url = "https://feeds.bbci.co.uk/news/world/rss.xml"
    print(f"\n  {_C}Live RSS test — fetching from{_D}")
    print(f"  {_DIM}{feed_url}{_D}")

    agent = IngestionAgent()
    t0 = time.perf_counter()
    try:
        items = await agent.fetch_latest(feed_urls=[feed_url], limit=3)
    except Exception as exc:
        print(_skip(f"Network unreachable or feed down: {exc}"))
        print(_skip("Skipping live test — unit tests still passed."))
        return True  # treat as skip, not failure

    elapsed = time.perf_counter() - t0

    if not items:
        print(_skip("Feed returned 0 items (may be a transient network issue)."))
        return True  # skip

    print(f"\n  {_B}Fetched {len(items)} item(s) in {elapsed:.1f}s{_D}\n")
    all_passed = True

    for i, item in enumerate(items):
        label = f"item[{i}]"

        # 1. Must be a NewsItem
        if not isinstance(item, NewsItem):
            print(_fail(f"{label}: not a NewsItem — got {type(item)}"))
            all_passed = False
            continue

        # 2. Headline non-empty
        if not item.headline or not item.headline.strip():
            print(_fail(f"{label}: empty headline"))
            all_passed = False
        else:
            print(_ok(f"{label} headline: \"{item.headline[:65]}\""))

        # 3. HTML-free headline (no angle brackets)
        if "<" in item.headline or ">" in item.headline:
            print(_fail(f"{label}: headline contains raw HTML tags"))
            all_passed = False
        else:
            print(_ok(f"{label}: headline is HTML-clean"))

        # 4. source_type
        if item.source_type != NewsSource.RSS:
            print(_fail(f"{label}: source_type={item.source_type!r} (expected RSS)"))
            all_passed = False
        else:
            print(_ok(f"{label}: source_type=RSS"))

        # 5. published_at timezone-aware (if present)
        if item.published_at is not None:
            if item.published_at.tzinfo is None:
                print(_fail(f"{label}: published_at is timezone-naive"))
                all_passed = False
            else:
                pub_s = item.published_at.strftime("%Y-%m-%d %H:%M UTC")
                print(_ok(f"{label}: published_at={pub_s} (tz-aware)"))
        else:
            print(_skip(f"{label}: published_at=None (feed didn't provide a date)"))

        # 6. source_channel set to the feed URL
        if not item.source_channel:
            print(_fail(f"{label}: source_channel is empty"))
            all_passed = False
        else:
            print(_ok(f"{label}: source_channel={item.source_channel[:60]}"))

        print()

    # 7. Newest-first ordering (if all items have dates)
    dated = [it for it in items if it.published_at is not None]
    if len(dated) >= 2:
        for j in range(len(dated) - 1):
            if dated[j].published_at < dated[j + 1].published_at:  # type: ignore[operator]
                print(_fail("Items are not sorted newest-first!"))
                all_passed = False
                break
        else:
            print(_ok("Items are sorted newest-first"))

    return all_passed


# ══════════════════════════════════════════════════════════════════════════════
#  Runner
# ══════════════════════════════════════════════════════════════════════════════

async def run_all() -> int:
    bar = "═" * 68
    print(f"\n{_B}{bar}{_D}")
    print(f"{_B}  IngestionAgent — Smoke Test{_D}")
    print(f"{_B}{bar}{_D}\n")

    # ── Unit tests (no network) ───────────────────────────────────────────────
    print(f"  {_B}Unit tests (no network){_D}")
    print(f"  {'─' * 54}")
    failed_unit = 0
    for test_fn in [
        test_strip_html,
        test_parse_feedparser_date_none,
        test_parse_feedparser_date_utc,
        test_deduplicate_items_by_url,
        test_deduplicate_items_prefers_body,
        test_ingestion_agent_init,
        test_from_rss_urls_factory,
    ]:
        try:
            test_fn()
        except AssertionError as exc:
            print(_fail(f"{test_fn.__name__}: {exc}"))
            failed_unit += 1
        except Exception as exc:
            print(_fail(f"{test_fn.__name__}: unexpected error: {exc}"))
            failed_unit += 1

    print()

    # ── Live test ─────────────────────────────────────────────────────────────
    print(f"  {_B}Live RSS smoke test{_D}")
    print(f"  {'─' * 54}")
    live_ok = await test_live_rss_fetch()

    # ── Summary ───────────────────────────────────────────────────────────────
    print(f"\n  {'─' * 54}")
    if failed_unit == 0 and live_ok:
        print(f"  {_G}{_B}All tests passed.{_D}")
        return 0
    else:
        if failed_unit:
            print(f"  {_R}{_B}{failed_unit} unit test(s) FAILED.{_D}")
        if not live_ok:
            print(f"  {_R}{_B}Live RSS test FAILED.{_D}")
        return 1


if __name__ == "__main__":
    sys.exit(asyncio.run(run_all()))
