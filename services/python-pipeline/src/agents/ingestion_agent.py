"""
IngestionAgent  —  Stage 1
============================
Fetches news items from RSS/Atom feeds and maps them cleanly to the NewsItem schema.

Architecture
────────────
Two thin layers:

  IngestorSource (ABC)   — source-agnostic fetch interface
    └── RSSIngestor      — fetches any RSS/Atom URL via feedparser
    └── [future] TelegramIngestor — drop in: inherit IngestorSource + implement fetch()
    └── [future] APIIngestor, WebhookIngestor, ...

  IngestionAgent (BaseAgent)  — orchestrates one or more IngestorSources:
    • Fans out to all sources concurrently with asyncio.gather
    • Deduplicates by URL
    • Sorts newest-first
    • Strips HTML from titles and descriptions
    • Maps to NewsItem (the only data contract for Stage 2)

Extending to Telegram later
────────────────────────────
    class TelegramIngestor(IngestorSource):
        def __init__(self, channel_handle: str, ...): ...
        async def fetch(self, limit: int = 10) -> list[NewsItem]: ...
        @property
        def source_id(self) -> str: return f"telegram:{self.channel_handle}"

    agent = IngestionAgent(sources=[
        RSSIngestor("https://feeds.bbci.co.uk/news/world/rss.xml"),
        TelegramIngestor("some_channel"),
    ])
    items = await agent.fetch_latest(limit=5)

HTML sanitisation
──────────────────
RSS descriptions commonly contain inline HTML (<p>, <a>, <b>, entities…).
All text fields (headline, body) are passed through _strip_html() before
they reach the Pydantic model, giving SemanticAgent clean natural-language input.
"""

from __future__ import annotations

import asyncio
import calendar
import html as html_lib
import logging
import re
import time as time_module
from abc import ABC, abstractmethod
from datetime import datetime, timezone
from typing import Any, Optional

from src.agents.base_agent import BaseAgent
from src.config import PipelineConfig
from src.models.schemas import NewsItem, NewsSource

logger = logging.getLogger("provenance.agent.ingestion")

# ── Default feeds (used when no URLs are supplied) ────────────────────────────
DEFAULT_RSS_FEEDS: list[str] = [
    "https://feeds.bbci.co.uk/news/world/rss.xml",           # BBC World News
    "https://feeds.bbci.co.uk/news/rss.xml",                  # BBC Top Stories
    "https://www.theguardian.com/world/rss",                   # The Guardian World
    "https://www.aljazeera.com/xml/rss/all.xml",              # Al Jazeera All
]

# ── Fetch timeout for each feed ───────────────────────────────────────────────
_DEFAULT_FETCH_TIMEOUT_S: float = 20.0

# ── User-agent sent with RSS requests ─────────────────────────────────────────
_USER_AGENT = (
    "ProvenancePipeline/0.1 "
    "(https://github.com/provenance; university-hackathon)"
)


# ══════════════════════════════════════════════════════════════════════════════
#  IngestorSource — abstract source interface
# ══════════════════════════════════════════════════════════════════════════════

class IngestorSource(ABC):
    """
    Pluggable source of NewsItem objects.

    Implement this to add any ingestion backend; the IngestionAgent
    only ever calls fetch() and inspects source_id — it is oblivious to
    whether the data comes from RSS, Telegram, a REST API, or a webhook.

    To add a new backend:
      1. Subclass IngestorSource.
      2. Implement fetch() and the source_id property.
      3. Instantiate and pass to IngestionAgent(sources=[...]).
    """

    @abstractmethod
    async def fetch(self, limit: int = 10) -> list[NewsItem]:
        """
        Retrieve up to `limit` news items from this source.

        Returns:
            list[NewsItem] — may be fewer than `limit` if the source
            has fewer items; never raises (logs internally).
        """

    @property
    @abstractmethod
    def source_id(self) -> str:
        """
        Human-readable unique identifier for this source.
        Stored in NewsItem.source_channel for provenance tracing.
        Example: "rss:https://feeds.bbci.co.uk/news/world/rss.xml"
        """


# ══════════════════════════════════════════════════════════════════════════════
#  RSSIngestor — concrete RSS/Atom implementation
# ══════════════════════════════════════════════════════════════════════════════

class RSSIngestor(IngestorSource):
    """
    Fetches any RSS 2.0 or Atom 1.0 feed using feedparser.

    Handles:
      • RSS 2.0, Atom 0.3/1.0, RDF Site Summary
      • ETag / Last-Modified conditional GETs (via feedparser)
      • HTML content in titles and descriptions (stripped via _strip_html)
      • Missing publication dates (NewsItem.published_at = None)
      • Encoding detection and UTF-8 normalisation
    """

    def __init__(
        self,
        feed_url: str,
        fetch_timeout: float = _DEFAULT_FETCH_TIMEOUT_S,
    ) -> None:
        self._url = feed_url
        self._timeout = fetch_timeout

    @property
    def source_id(self) -> str:
        return f"rss:{self._url}"

    async def fetch(self, limit: int = 10) -> list[NewsItem]:
        """Parse the feed and return up to `limit` NewsItems."""
        try:
            feed = await self._parse_feed()
        except Exception as exc:
            logger.error("RSSIngestor: feed fetch failed for %s: %s", self._url, exc)
            return []

        if not feed or not getattr(feed, "entries", None):
            logger.warning("RSSIngestor: no entries found at %s", self._url)
            return []

        items: list[NewsItem] = []
        for entry in feed.entries[:max(limit * 3, 30)]:  # grab extra; dedup later
            item = _entry_to_news_item(entry, feed, source_channel=self._url)
            if item:
                items.append(item)
            if len(items) >= limit:
                break

        logger.info(
            "RSSIngestor: fetched %d/%d item(s) from '%s'",
            len(items),
            len(feed.entries),
            getattr(feed.feed, "title", self._url),
        )
        return items

    async def _parse_feed(self) -> Any:
        """Run feedparser.parse() in a thread executor with a timeout."""
        try:
            import feedparser  # noqa: PLC0415
        except ImportError as exc:
            raise ImportError(
                "feedparser is required for RSSIngestor.\n"
                "Run: pip install feedparser>=6.0"
            ) from exc

        loop = asyncio.get_event_loop()
        return await asyncio.wait_for(
            loop.run_in_executor(
                None,
                lambda: feedparser.parse(
                    self._url,
                    agent=_USER_AGENT,
                ),
            ),
            timeout=self._timeout,
        )


# ══════════════════════════════════════════════════════════════════════════════
#  IngestionAgent — orchestrates one or more IngestorSources
# ══════════════════════════════════════════════════════════════════════════════

class IngestionAgent(BaseAgent):
    """
    Stage 1: fetch, clean, and map news items to the NewsItem schema.

    Direct use (no queue needed):
        agent = IngestionAgent()
        items = await agent.fetch_latest(
            feed_urls=["https://feeds.bbci.co.uk/news/world/rss.xml"],
            limit=3,
        )
        headline = items[0].headline  # clean, HTML-stripped text

    Pre-configured sources (RSS + any future backend):
        agent = IngestionAgent(sources=[
            RSSIngestor("https://feeds.bbci.co.uk/news/world/rss.xml"),
            TelegramIngestor("some_channel"),   # drop-in when ready
        ])
        items = await agent.fetch_latest(limit=1)

    Factory for common RSS-only case:
        agent = IngestionAgent.from_rss_urls([url1, url2])
    """

    def __init__(
        self,
        sources: Optional[list[IngestorSource]] = None,
        default_feed_urls: Optional[list[str]] = None,
        fetch_timeout: float = _DEFAULT_FETCH_TIMEOUT_S,
        name: str = "ingestion",
        output_queue: Optional[asyncio.Queue[Any]] = None,
        config: Optional[PipelineConfig] = None,
    ) -> None:
        # IngestionAgent is a SOURCE — it has no input_queue.
        super().__init__(
            name=name,
            input_queue=None,
            output_queue=output_queue,
            config=config,
        )
        self._fetch_timeout = fetch_timeout
        self._sources: list[IngestorSource] = sources or []

        # The `default_feed_urls` list is used when neither `sources` nor
        # explicit `feed_urls` are supplied.  Falls back to DEFAULT_RSS_FEEDS.
        urls_from_config = (
            self.config.rss_feeds if hasattr(self.config, "rss_feeds") else []
        )
        self._default_feed_urls: list[str] = (
            default_feed_urls
            or urls_from_config
            or DEFAULT_RSS_FEEDS
        )

        logger.info(
            "IngestionAgent ready — %d pre-configured source(s), "
            "%d default feed URL(s)",
            len(self._sources),
            len(self._default_feed_urls),
        )

    # ── Factory ───────────────────────────────────────────────────────────────

    @classmethod
    def from_rss_urls(
        cls,
        feed_urls: list[str],
        fetch_timeout: float = _DEFAULT_FETCH_TIMEOUT_S,
        **kwargs: Any,
    ) -> "IngestionAgent":
        """
        Convenience factory: create an IngestionAgent from a list of RSS URLs.

        Equivalent to:
            IngestionAgent(sources=[RSSIngestor(url) for url in feed_urls])
        """
        sources = [RSSIngestor(url, fetch_timeout=fetch_timeout) for url in feed_urls]
        return cls(sources=sources, fetch_timeout=fetch_timeout, **kwargs)

    # ── BaseAgent.process (queue-path, IngestionAgent is a source producer) ──

    async def process(self, item: Any) -> list[NewsItem]:  # type: ignore[override]
        """
        Queue-compatible entry point.

        item may be:
            str        — single RSS feed URL
            list[str]  — multiple feed URLs
            None       — use default_feed_urls or pre-configured sources
        """
        if isinstance(item, str):
            return await self.fetch_latest(feed_urls=[item])
        if isinstance(item, list) and all(isinstance(u, str) for u in item):
            return await self.fetch_latest(feed_urls=item)
        return await self.fetch_latest()

    # ── Primary public method ─────────────────────────────────────────────────

    async def fetch_latest(
        self,
        feed_urls: Optional[list[str]] = None,
        limit: int = 1,
    ) -> list[NewsItem]:
        """
        Fetch the most-recent news items from RSS feeds or pre-configured sources.

        Processing order:
          1. Fetch all sources concurrently (asyncio.gather).
          2. Flatten and deduplicate by URL / headline.
          3. Sort newest-first (None dates pushed to end).
          4. Return the top `limit` items.

        Args:
            feed_urls: Optional list of RSS/Atom URLs.  If supplied, these are
                       fetched *in addition to* any pre-configured sources.
                       If None and no sources were pre-configured, the
                       DEFAULT_RSS_FEEDS are used.
            limit:     Maximum number of NewsItems to return (default: 1).

        Returns:
            list[NewsItem] — at most `limit` items, sorted newest-first.
        """
        sources: list[IngestorSource] = list(self._sources)

        # Convert ad-hoc feed_urls to RSSIngestor instances
        if feed_urls:
            sources.extend(
                RSSIngestor(url, fetch_timeout=self._fetch_timeout)
                for url in feed_urls
            )

        # If still no sources, fall back to defaults
        if not sources:
            sources = [
                RSSIngestor(url, fetch_timeout=self._fetch_timeout)
                for url in self._default_feed_urls
            ]

        if not sources:
            logger.warning("IngestionAgent: no sources configured — returning empty list.")
            return []

        logger.info(
            "IngestionAgent: fetching from %d source(s) concurrently (limit=%d)…",
            len(sources), limit,
        )

        # Fan out to all sources in parallel
        tasks = [src.fetch(limit=limit * 5) for src in sources]  # fetch extra for dedup
        results: list[Any] = await asyncio.gather(*tasks, return_exceptions=True)

        # Flatten; skip errored tasks
        all_items: list[NewsItem] = []
        for i, batch in enumerate(results):
            if isinstance(batch, BaseException):
                logger.warning(
                    "Source[%d] (%s) raised: %s",
                    i,
                    sources[i].source_id,
                    batch,
                )
                continue
            all_items.extend(batch)

        # Deduplicate by URL, then by normalised headline
        unique = _deduplicate_items(all_items)

        # Sort newest-first (None dates → end of list)
        unique.sort(key=_pub_sort_key, reverse=True)

        logger.info(
            "IngestionAgent: %d raw items → %d unique → returning top %d",
            len(all_items), len(unique), min(limit, len(unique)),
        )
        return unique[:limit]


# ══════════════════════════════════════════════════════════════════════════════
#  Conversion helpers  (stateless, independently unit-testable)
# ══════════════════════════════════════════════════════════════════════════════

def _entry_to_news_item(
    entry: Any,
    feed: Any,
    source_channel: str,
) -> Optional[NewsItem]:
    """
    Map one feedparser entry to a NewsItem.

    Returns None if the entry has no usable headline.
    """
    # ── Headline ──────────────────────────────────────────────────────────────
    raw_title = getattr(entry, "title", None) or ""
    headline  = _strip_html(raw_title).strip()
    if not headline:
        return None                          # unusable — skip silently

    # ── Body text (description / summary / content) ───────────────────────────
    body_raw: str = ""
    # feedparser puts full-text in entry.content (list of dicts)
    content_list = getattr(entry, "content", [])
    if content_list and isinstance(content_list, list):
        body_raw = content_list[0].get("value", "")
    # Fallback: summary or description
    if not body_raw:
        body_raw = (
            getattr(entry, "summary", None)
            or getattr(entry, "description", None)
            or ""
        )
    body = _strip_html(body_raw).strip() or None

    # ── URL ───────────────────────────────────────────────────────────────────
    url: Optional[str] = getattr(entry, "link", None) or getattr(entry, "id", None)

    # ── Published timestamp ───────────────────────────────────────────────────
    raw_date = (
        getattr(entry, "published_parsed", None)
        or getattr(entry, "updated_parsed", None)
    )
    published_at: Optional[datetime] = _parse_feedparser_date(raw_date)

    # ── Language (feed-level metadata) ───────────────────────────────────────
    feed_meta    = getattr(feed, "feed", None)
    raw_language = getattr(feed_meta, "language", "en") or "en"
    language     = re.split(r"[-_]", raw_language.lower())[0][:2] or "en"

    # ── Raw metadata (extra feed-level fields useful for debugging) ───────────
    feed_title = getattr(feed_meta, "title", "") or ""
    entry_id   = getattr(entry, "id", "") or ""
    tags       = [
        t.get("term", "") for t in getattr(entry, "tags", []) if isinstance(t, dict)
    ]

    return NewsItem(
        headline       = headline,
        body           = body,
        url            = url,
        source_channel = source_channel,
        source_type    = NewsSource.RSS,
        published_at   = published_at,
        language       = language,
        raw_metadata   = {
            "feed_title":  feed_title,
            "entry_id":    entry_id,
            "tags":        tags,
        },
    )


def _strip_html(text: str) -> str:
    """
    Remove HTML markup and decode HTML entities from a string.

    Uses BeautifulSoup4 when available (most robust) with a lightweight
    regex+html.unescape fallback.

    >>> _strip_html("<p>PM <b>retires</b> after &amp; 10 years</p>")
    'PM retires after & 10 years'
    """
    if not text:
        return ""
    try:
        from bs4 import BeautifulSoup  # noqa: PLC0415
        cleaned = BeautifulSoup(text, "html.parser").get_text(separator=" ", strip=True)
    except ImportError:
        # Fallback: regex strip + entity unescape
        no_tags = re.sub(r"<[^>]+>", " ", text)
        cleaned = html_lib.unescape(no_tags)
    # Collapse runs of whitespace into single spaces
    return " ".join(cleaned.split())


def _parse_feedparser_date(
    ts: Optional[time_module.struct_time],
) -> Optional[datetime]:
    """
    Convert a feedparser time.struct_time (UTC) to an aware datetime.

    feedparser.entry.published_parsed is already in UTC; calendar.timegm()
    converts it to a Unix timestamp without local-timezone correction.
    """
    if ts is None:
        return None
    try:
        return datetime.fromtimestamp(calendar.timegm(ts), tz=timezone.utc)
    except (ValueError, OverflowError, OSError):
        return None


def _deduplicate_items(items: list[NewsItem]) -> list[NewsItem]:
    """
    Remove duplicate NewsItems, preferring items with richer fields.

    Deduplication keys (in priority order):
      1. Normalised URL (if present)
      2. Lowercased headline (fallback for items without URLs)

    When two items share a URL, the one with a ``body`` value is kept; if
    both have (or both lack) a body, the first-encountered item wins.

    Implementation note: ``seen_urls`` / ``seen_headlines`` map each key to
    the *index* of the corresponding slot in ``result`` so that upgrades
    (body-rich copy found after the first-seen copy) are written back into
    the correct position rather than being silently dropped.
    """
    seen_urls:      dict[str, int] = {}   # normalised URL  → result index
    seen_headlines: dict[str, int] = {}   # normalised text → result index
    result:         list[NewsItem] = []

    for item in items:
        if item.url:
            key = _normalise_url(item.url)
            if key in seen_urls:
                idx = seen_urls[key]
                # Upgrade in-place when the new copy is richer
                if item.body and not result[idx].body:
                    result[idx] = item
                continue
            # First encounter — register and record insertion index
            seen_urls[key] = len(result)
            # Also register the headline key so headline-only items
            # that duplicate this entry are caught in the second pass.
            h_key = " ".join(item.headline.lower().split())
            seen_headlines.setdefault(h_key, len(result))
            result.append(item)
            continue

        # No URL — secondary dedup by normalised headline only
        h_key = " ".join(item.headline.lower().split())
        if h_key in seen_headlines:
            idx = seen_headlines[h_key]
            if item.body and not result[idx].body:
                result[idx] = item
            continue
        seen_headlines[h_key] = len(result)
        result.append(item)

    return result


def _normalise_url(url: str) -> str:
    """Lowercase scheme+host, strip tracking params and fragment."""
    try:
        from urllib.parse import parse_qs, urlencode, urlparse, urlunparse  # noqa: PLC0415
        _STRIP = {"utm_source","utm_medium","utm_campaign","utm_content","utm_term",
                  "utm_id","fbclid","gclid","msclkid","ref","_ga"}
        p = urlparse(url.strip())
        qs = {k: v for k, v in parse_qs(p.query).items() if k.lower() not in _STRIP}
        cleaned = p._replace(
            scheme=p.scheme.lower(), netloc=p.netloc.lower(),
            query=urlencode(qs, doseq=True), fragment="",
        )
        return urlunparse(cleaned).rstrip("/")
    except Exception:
        return url.strip().lower()


def _pub_sort_key(item: NewsItem) -> datetime:
    """Sort key for newest-first ordering; None dates sort to the end."""
    if item.published_at is None:
        return datetime.min.replace(tzinfo=timezone.utc)
    return item.published_at
