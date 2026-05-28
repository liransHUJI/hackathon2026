"""
PipelineConfig
==============
Single source of truth for all runtime configuration.
Values are read from environment variables or a .env file.

Usage:
    from src.config import PipelineConfig
    config = PipelineConfig()          # reads .env automatically
    config = PipelineConfig(_env_file=".env.test")  # override for tests
"""

from __future__ import annotations

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


class PipelineConfig(BaseSettings):
    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",           # silently ignore unknown env vars
        case_sensitive=False,
    )

    # ── LLM ───────────────────────────────────────────────────────────────────
    anthropic_api_key: str = Field(
        default="",
        description="Required for SemanticAgent and LLM-Judge detector.",
    )
    llm_model: str = Field(
        default="claude-sonnet-4-6",
        description="Anthropic model ID for the SemanticAgent.",
    )
    llm_max_tokens: int = Field(default=4096)
    llm_temperature: float = Field(default=0.9, ge=0.0, le=2.0)
    llm_requests_per_minute: int = Field(default=50, gt=0)
    llm_tokens_per_minute: int = Field(default=60_000, gt=0)

    # ── Scraper backend ───────────────────────────────────────────────────────
    scraper_backend: str = Field(
        default="basic",
        description='"basic" (free, DuckDuckGo+httpx) or "brightdata" (hackathon day).',
    )
    brightdata_api_key: str | None = Field(
        default=None,
        description="Required when scraper_backend='brightdata'.",
    )
    brightdata_budget_usd: float = Field(
        default=75.0,
        gt=0.0,
        description="Hard spend cap for the BrightDataScraper. Never disable.",
    )

    # ── Pipeline behaviour ────────────────────────────────────────────────────
    permutation_count: int = Field(
        default=100,
        gt=0,
        description="Target number of semantic permutations per headline.",
    )
    max_scrape_results: int = Field(
        default=200,
        gt=0,
        description="Maximum results collected before deduplication.",
    )
    similarity_threshold: float = Field(
        default=0.45,
        ge=0.0,
        le=1.0,
        description="Minimum cosine similarity for a result to pass to Stage 5.",
    )
    top_candidates: int = Field(
        default=10,
        gt=0,
        description="Number of top-ranked results passed to the AI detector.",
    )
    scraper_concurrency: int = Field(
        default=10,
        gt=0,
        description="asyncio.Semaphore size for parallel scrape tasks.",
    )
    domain_rate_limit_rps: float = Field(
        default=2.0,
        gt=0.0,
        description="Max requests per second per domain (DomainRateLimiter).",
    )

    # ── Ingestion sources ─────────────────────────────────────────────────────
    rss_feeds: list[str] = Field(
        default_factory=list,
        description="Comma-separated RSS feed URLs (set via RSS_FEEDS env var).",
    )
    telegram_channels: list[str] = Field(
        default_factory=list,
        description="Comma-separated Telegram channel handles.",
    )
    telegram_api_id: str | None = Field(default=None)
    telegram_api_hash: str | None = Field(default=None)

    # ── Similarity model ──────────────────────────────────────────────────────
    embedding_model: str = Field(
        default="all-MiniLM-L6-v2",
        description="sentence-transformers model for headline/result embedding.",
    )

    # ── AI detection ──────────────────────────────────────────────────────────
    gptzero_api_key: str | None = Field(default=None)
    sapling_api_key: str | None = Field(default=None)
    ai_detection_threshold: float = Field(
        default=0.65,
        ge=0.0,
        le=1.0,
        description="ensemble_score >= this → is_ai_generated = True.",
    )

    # ── Convenience helpers ───────────────────────────────────────────────────
    @property
    def has_anthropic_key(self) -> bool:
        return bool(self.anthropic_api_key)

    @property
    def has_brightdata_key(self) -> bool:
        return bool(self.brightdata_api_key)

    @property
    def use_brightdata(self) -> bool:
        return self.scraper_backend.lower() == "brightdata"
