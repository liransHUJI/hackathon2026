"""
Provenance Pipeline — Data Contracts
=====================================
All Pydantic v2 models that flow between pipeline stages.

Stage contracts:
  1. Ingestion Agent  →  NewsItem
  2. Semantic Agent   →  PermutationSet   (contains list[Permutation])
  3. Scraper Agent    →  ScrapedResultSet (contains list[ScrapedResult])
  4. Similarity Agent →  AnalyzedSet      (contains list[RankedResult])
  5. AI Detector      →  ProvenanceReport (contains list[AISignatureResult])

Rules:
  - Never pass raw dict across agent boundaries; always use these models.
  - Every float score field is constrained to [0.0, 1.0].
  - published_at / ingested_at fields are always UTC datetime.
"""

from __future__ import annotations

import uuid
from datetime import datetime, timezone
from enum import StrEnum
from typing import Any, Optional

from pydantic import BaseModel, Field


# ══════════════════════════════════════════════════════════════════════════════
#  Enums
# ══════════════════════════════════════════════════════════════════════════════

class NewsSource(StrEnum):
    """Where a NewsItem was ingested from."""
    TELEGRAM = "telegram"
    RSS      = "rss"
    MANUAL   = "manual"   # Injected directly via CLI / scripts / tests


class ContentType(StrEnum):
    """Best-guess classification of a scraped page."""
    ARTICLE     = "article"
    SOCIAL_POST = "social_post"
    FORUM       = "forum"
    UNKNOWN     = "unknown"


class RiskLabel(StrEnum):
    """Human-readable disinformation risk tier on the final report."""
    LOW      = "LOW"
    MEDIUM   = "MEDIUM"
    HIGH     = "HIGH"
    CRITICAL = "CRITICAL"


# ══════════════════════════════════════════════════════════════════════════════
#  Stage 1 → Stage 2  :  NewsItem
# ══════════════════════════════════════════════════════════════════════════════

class NewsItem(BaseModel):
    """
    A single news story extracted from an ingestion source.

    Produced by: IngestionAgent
    Consumed by: SemanticAgent
    """

    item_id: str = Field(
        default_factory=lambda: str(uuid.uuid4()),
        description="UUID4 assigned at ingestion time.",
    )
    headline: str = Field(
        description="The raw headline or title of the story.",
    )
    body: Optional[str] = Field(
        default=None,
        description="Full article body if available from the source.",
    )
    url: Optional[str] = Field(
        default=None,
        description="Canonical URL of the original article.",
    )
    source_channel: str = Field(
        default="",
        description="Feed URL, Telegram channel handle, or 'manual'.",
    )
    source_type: NewsSource = Field(
        default=NewsSource.MANUAL,
    )
    published_at: Optional[datetime] = Field(
        default=None,
        description="Publication timestamp from the source (UTC).",
    )
    ingested_at: datetime = Field(
        default_factory=lambda: datetime.now(timezone.utc),
        description="When this item entered the pipeline (UTC).",
    )
    language: str = Field(
        default="en",
        description="ISO 639-1 language code.",
    )
    raw_metadata: dict[str, Any] = Field(
        default_factory=dict,
        description="Unstructured source-specific fields (e.g. Telegram message ID).",
    )

    model_config = {}  # datetime serialisation handled by Pydantic v2 default (ISO 8601)


# ══════════════════════════════════════════════════════════════════════════════
#  Stage 2 → Stage 3  :  PermutationSet
# ══════════════════════════════════════════════════════════════════════════════

class Permutation(BaseModel):
    """
    A single semantic variant of a headline.

    Strategies include: synonym replacement, passive/active voice swap,
    entity generalisation, temporal paraphrasing, negation, paraphrase.
    """

    text: str = Field(
        description="The permuted query string to be used for web search.",
    )
    strategy: str = Field(
        default="paraphrase",
        description="Transformation strategy applied by the LLM.",
    )
    confidence: float = Field(
        default=1.0,
        ge=0.0,
        le=1.0,
        description="LLM-estimated likelihood this variant will find relevant results.",
    )


class PermutationSet(BaseModel):
    """
    All semantic permutations generated for one NewsItem.

    Produced by: SemanticAgent
    Consumed by: BroadScraperAgent
    """

    source_item: NewsItem = Field(
        description="The NewsItem this set was generated from.",
    )
    original_query: str = Field(
        description="Canonicalised search string derived from the headline.",
    )
    permutations: list[Permutation] = Field(
        default_factory=list,
        description="~100 semantic variants ready to be used as search queries.",
    )
    model_used: str = Field(
        default="",
        description="Anthropic model ID used for generation.",
    )
    generated_at: datetime = Field(
        default_factory=lambda: datetime.now(timezone.utc),
    )
    total_count: int = Field(
        default=0,
        description="Length of `permutations`; set automatically after generation.",
    )

    def model_post_init(self, __context: Any) -> None:  # noqa: ANN401
        """Auto-populate total_count from the permutations list length."""
        if self.total_count == 0 and self.permutations:
            # bypass Pydantic validation on assignment inside model_post_init
            object.__setattr__(self, "total_count", len(self.permutations))


# ══════════════════════════════════════════════════════════════════════════════
#  Stage 3 → Stage 4  :  ScrapedResultSet
# ══════════════════════════════════════════════════════════════════════════════

class ScrapedResult(BaseModel):
    """
    A single article or post found by the scraper for one permutation query.

    Produced by: BroadScraperAgent (via Scraper.search + Scraper.fetch_content)
    Consumed by: ChronologicalSimilarityAnalyzer
    """

    result_id: str = Field(
        default_factory=lambda: str(uuid.uuid4()),
    )
    url: str = Field(
        description="Normalised canonical URL of the found page.",
    )
    title: Optional[str] = Field(
        default=None,
        description="Page title from search result or <title> tag.",
    )
    snippet: str = Field(
        default="",
        description="Short excerpt returned by the search engine.",
    )
    full_text: Optional[str] = Field(
        default=None,
        description="Extracted body text (max 8 000 chars). None until fetch_content() runs.",
    )
    domain: str = Field(
        default="",
        description="Bare domain, e.g. 'nytimes.com'.",
    )
    content_type: ContentType = Field(
        default=ContentType.UNKNOWN,
    )
    published_at: Optional[datetime] = Field(
        default=None,
        description="Publication timestamp parsed from the page (UTC).",
    )
    scraped_at: datetime = Field(
        default_factory=lambda: datetime.now(timezone.utc),
    )
    query_used: str = Field(
        default="",
        description="The Permutation.text that found this result.",
    )
    scraper_id: str = Field(
        default="basic",
        description="'basic' or 'brightdata' — identifies which backend ran.",
    )
    http_status: Optional[int] = Field(
        default=None,
        description="HTTP status code from fetch_content(); None if not fetched.",
    )
    error: Optional[str] = Field(
        default=None,
        description="Non-None means fetch_content() failed; result is partial.",
    )


class ScrapedResultSet(BaseModel):
    """
    All scraping results collected for one PermutationSet.

    Produced by: BroadScraperAgent
    Consumed by: ChronologicalSimilarityAnalyzer
    """

    source_permutation_set: PermutationSet
    results: list[ScrapedResult] = Field(
        default_factory=list,
        description="Deduplicated results across all permutation queries.",
    )
    total_queries_issued: int = Field(
        default=0,
        description="Number of search() calls made (≤ len(permutations)).",
    )
    total_results_raw: int = Field(
        default=0,
        description="Results before deduplication.",
    )
    deduplication_removed: int = Field(
        default=0,
        description="How many results were dropped as duplicates.",
    )
    scrape_duration_seconds: float = Field(
        default=0.0,
    )


# ══════════════════════════════════════════════════════════════════════════════
#  Stage 4 → Stage 5  :  AnalyzedSet
# ══════════════════════════════════════════════════════════════════════════════

class RankedResult(BaseModel):
    """
    A ScrapedResult enriched with chronological rank and semantic similarity score.

    Produced by: ChronologicalSimilarityAnalyzer
    Consumed by: AISignatureDetectorAgent
    """

    scraped_result: ScrapedResult
    similarity_score: float = Field(
        ge=0.0,
        le=1.0,
        description="Cosine similarity between the original headline and this result's title+snippet.",
    )
    chronological_rank: int = Field(
        default=0,
        description="1 = the earliest result found across the entire result set.",
    )
    composite_score: float = Field(
        default=0.0,
        ge=0.0,
        le=1.0,
        description="0.6 × similarity_score + 0.4 × recency_weight.",
    )
    is_likely_original: bool = Field(
        default=False,
        description="True for the single result judged most likely to be the original source.",
    )


class AnalyzedSet(BaseModel):
    """
    Ranked and filtered candidates ready for AI-signature detection.

    Produced by: ChronologicalSimilarityAnalyzer
    Consumed by: AISignatureDetectorAgent
    """

    source_result_set: ScrapedResultSet
    ranked_results: list[RankedResult] = Field(
        default_factory=list,
        description="All results that passed the similarity threshold, sorted by composite_score.",
    )
    top_candidates: list[RankedResult] = Field(
        default_factory=list,
        description="Top N (default 10) by composite_score — passed to Stage 5.",
    )
    analysis_duration_seconds: float = Field(default=0.0)
    similarity_model: str = Field(
        default="all-MiniLM-L6-v2",
        description="sentence-transformers model used for embedding.",
    )


# ══════════════════════════════════════════════════════════════════════════════
#  Stage 5 output  :  ProvenanceReport
# ══════════════════════════════════════════════════════════════════════════════

class DetectionMethod(BaseModel):
    """
    Result from a single AI-text-detection method.

    method_name options:
    "statistical" | "stylometric" | "template_repetition" | "llm_judge"
    """

    method_name: str
    score: Optional[float] = Field(
        default=None,
        ge=0.0,
        le=1.0,
        description="0.0 = definitely human, 1.0 = definitely AI. None if the method did not run.",
    )
    label: Optional[str] = Field(
        default=None,
        description="'AI' | 'HUMAN' | 'UNCERTAIN'",
    )
    raw_response: dict[str, Any] = Field(
        default_factory=dict,
        description="Full API response or analysis result for auditability.",
    )
    error: Optional[str] = Field(
        default=None,
        description="Non-None → method failed; excluded from ensemble calculation.",
    )


class AISignatureResult(BaseModel):
    """
    Combined AI-detection verdict for one candidate article.

    Produced by: AISignatureDetectorAgent (one per top_candidate)
    """

    ranked_result: RankedResult
    detection_methods: list[DetectionMethod] = Field(
        default_factory=list,
        description="Results from all configured detection methods (successful and failed).",
    )
    ensemble_score: float = Field(
        default=0.0,
        ge=0.0,
        le=1.0,
        description=(
            "Weighted average across successful methods. "
            "Weights: Statistical 35%, Stylometric 25%, Template 20%, LLM Judge 20%."
        ),
    )
    is_ai_generated: bool = Field(
        default=False,
        description="True when ensemble_score >= PipelineConfig.ai_detection_threshold (default 0.65).",
    )
    confidence: float = Field(
        default=0.0,
        ge=0.0,
        le=1.0,
        description=(
            "Scales with number of successful methods: "
            "min(0.45 + 0.13 × n_successful, 0.97)."
        ),
    )
    explanation: str = Field(
        default="",
        description="Human-readable narrative summarising the detection verdict.",
    )


class ProvenanceReport(BaseModel):
    """
    Final output of the pipeline for one NewsItem.

    Serialised as JSON to data/outputs/<report_id>.json.
    """

    report_id: str = Field(
        default_factory=lambda: str(uuid.uuid4()),
    )
    source_item: NewsItem = Field(
        description="The original NewsItem that triggered this pipeline run.",
    )
    ai_signature_results: list[AISignatureResult] = Field(
        default_factory=list,
        description="One result per top_candidate analysed.",
    )
    earliest_source: Optional[RankedResult] = Field(
        default=None,
        description="The candidate with chronological_rank == 1.",
    )
    disinformation_risk: float = Field(
        default=0.0,
        ge=0.0,
        le=1.0,
        description="Pipeline-level risk score (max ensemble_score across all candidates).",
    )
    risk_label: RiskLabel = Field(
        default=RiskLabel.LOW,
        description="Categorical risk tier derived from disinformation_risk.",
    )
    pipeline_version: str = Field(default="0.1.0")
    generated_at: datetime = Field(
        default_factory=lambda: datetime.now(timezone.utc),
    )
    total_duration_seconds: float = Field(default=0.0)
    summary: str = Field(
        default="",
        description="One-paragraph human-readable narrative for the report.",
    )

    @classmethod
    def risk_label_from_score(cls, score: float) -> RiskLabel:
        """Derive a RiskLabel from a float disinformation_risk score."""
        if score >= 0.80:
            return RiskLabel.CRITICAL
        if score >= 0.60:
            return RiskLabel.HIGH
        if score >= 0.35:
            return RiskLabel.MEDIUM
        return RiskLabel.LOW
