"""Data contracts (Pydantic v2 models) for every inter-stage boundary."""

from src.models.schemas import (
    AnalyzedSet,
    AISignatureResult,
    ContentType,
    DetectionMethod,
    NewsItem,
    NewsSource,
    Permutation,
    PermutationSet,
    ProvenanceReport,
    RankedResult,
    RiskLabel,
    ScrapedResult,
    ScrapedResultSet,
)

__all__ = [
    "AnalyzedSet",
    "AISignatureResult",
    "ContentType",
    "DetectionMethod",
    "NewsItem",
    "NewsSource",
    "Permutation",
    "PermutationSet",
    "ProvenanceReport",
    "RankedResult",
    "RiskLabel",
    "ScrapedResult",
    "ScrapedResultSet",
]
