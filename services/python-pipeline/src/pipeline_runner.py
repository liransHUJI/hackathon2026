"""
PipelineRunner
==============
Central orchestrator that wires Stages 2–5 into a single end-to-end call.

Quick-start (production / demo):
    from src.pipeline_runner import PipelineRunner
    runner = PipelineRunner.from_config()
    report = await runner.run_pipeline("PM announces retirement from politics")
    print(report.model_dump_json(indent=2))

Quick-start (testing with mocks):
    runner = PipelineRunner(
        semantic_agent=mock_semantic,
        scraper_agent=mock_scraper,
        analyzer_agent=mock_analyzer,
        detector_agent=mock_detector,
    )

Pipeline flow
─────────────
  headline (str)
    │
    ▼  Stage 1 — Ingestion (inline — no separate agent for manual input)
  NewsItem
    │
    ▼  Stage 2 — SemanticAgent
  PermutationSet  (N semantic variants of the headline)
    │
    ▼  Stage 3 — BroadScraperAgent
  ScrapedResultSet  (deduplicated web results for all variants)
    │
    ▼  Stage 4 — AnalyzerAgent
  AnalyzedSet  (chronologically sorted, similarity-filtered candidates)
    │
    ▼  Stage 5 — AISignatureDetectorAgent
  ProvenanceReport  ← final output

Telemetry
──────────
Every stage transition emits a structured log line visible in the terminal,
including elapsed time.  After run_pipeline() returns, per-stage timings are
available in runner.stage_logs (list of StageLog named tuples).

Error handling
───────────────
A try/except wraps the entire chain.  Any unhandled exception is caught,
logged with a full traceback, and returned as a ProvenanceReport with
risk_label=LOW and an error summary — the orchestrator never crashes the
caller.  Per-stage exceptions surface in the log before the fallback kicks in.
"""

from __future__ import annotations

import logging
import os
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Optional

from src.agents.analyzer_agent import AnalyzerAgent
from src.agents.detector_agent import AISignatureDetectorAgent
from src.agents.scraper_agent import BroadScraperAgent
from src.agents.semantic_agent import SemanticAgent
from src.config import PipelineConfig
from src.models.schemas import (
    NewsItem,
    NewsSource,
    ProvenanceReport,
    RiskLabel,
)

logger = logging.getLogger("provenance.pipeline")

# ── ANSI colour codes (used only in the stage-progress logger) ────────────────
_C  = "\033[96m"    # cyan
_G  = "\033[92m"    # green
_Y  = "\033[93m"    # yellow
_R  = "\033[91m"    # red
_B  = "\033[1m"     # bold
_D  = "\033[0m"     # reset

_STAGE_COLOURS = {1: _C, 2: _C, 3: _C, 4: _C, 5: _C}
_RISK_COLOURS  = {
    "LOW":      _G,
    "MEDIUM":   _Y,
    "HIGH":     _Y,
    "CRITICAL": _R,
}


# ══════════════════════════════════════════════════════════════════════════════
#  StageLog  (lightweight telemetry record — one per stage)
# ══════════════════════════════════════════════════════════════════════════════

@dataclass
class StageLog:
    """Timing and status snapshot captured after each pipeline stage."""
    stage_num:  int
    stage_name: str
    message:    str
    elapsed_s:  float
    started_at: datetime = field(default_factory=lambda: datetime.now(timezone.utc))

    def __str__(self) -> str:
        colour = _STAGE_COLOURS.get(self.stage_num, _C)
        return (
            f"  {colour}{_B}[Stage {self.stage_num}]{_D}  "
            f"{self.stage_name:<22}  {self.message}  "
            f"{_B}({self.elapsed_s:.1f}s){_D}"
        )


# ══════════════════════════════════════════════════════════════════════════════
#  PipelineRunner
# ══════════════════════════════════════════════════════════════════════════════

class PipelineRunner:
    """
    Coordinates the full provenance-detection pipeline for one headline.

    Accepts pre-built agent instances so it can be tested with mocks or wired
    into any asyncio framework.  Use from_config() for production convenience.
    """

    def __init__(
        self,
        semantic_agent:  SemanticAgent,
        scraper_agent:   BroadScraperAgent,
        analyzer_agent:  AnalyzerAgent,
        detector_agent:  AISignatureDetectorAgent,
        config: Optional[PipelineConfig] = None,
    ) -> None:
        self.semantic_agent  = semantic_agent
        self.scraper_agent   = scraper_agent
        self.analyzer_agent  = analyzer_agent
        self.detector_agent  = detector_agent
        self.config: PipelineConfig = config or PipelineConfig()

        self._stage_logs: list[StageLog] = []
        self._current_stage: int = 0

        logger.info("PipelineRunner initialised — ready to run.")

    # ── Factory ───────────────────────────────────────────────────────────────

    @classmethod
    def from_config(cls, config: Optional[PipelineConfig] = None) -> "PipelineRunner":
        """
        Construct all pipeline components from environment / config.

        Minimal .env requirements:
            GEMINI_API_KEY=<key>      (required — stages 2 and 4)

        Optional keys (graceful degradation if absent):
            ANTHROPIC_API_KEY         (stage 5 LLM Judge)
            SCRAPER_BACKEND=brightdata + BRIGHTDATA_API_KEY  (premium scraping)

        Raises:
            ValueError  if GEMINI_API_KEY is missing.
            ValueError  if SCRAPER_BACKEND=brightdata but BRIGHTDATA_API_KEY unset.
        """
        # Late imports keep startup fast and avoid circular-import issues.
        from src.scraper.factory import ScraperFactory  # noqa: PLC0415

        if config is None:
            config = PipelineConfig()

        logger.info("Building pipeline from config (backend=%r)…", config.scraper_backend)

        scraper = ScraperFactory.create(config)

        semantic = SemanticAgent(
            name="semantic",
            config=config,
        )
        broad_scraper = BroadScraperAgent(
            scraper=scraper,
            name="scraper",
            max_results_per_query=5,         # per-query DDG results cap
            concurrency=config.scraper_concurrency,
            config=config,
        )
        analyzer = AnalyzerAgent(
            name="analyzer",
            config=config,
        )
        detector = AISignatureDetectorAgent(
            name="detector",
            config=config,
        )

        return cls(
            semantic_agent=semantic,
            scraper_agent=broad_scraper,
            analyzer_agent=analyzer,
            detector_agent=detector,
            config=config,
        )

    # ── Primary public method ─────────────────────────────────────────────────

    async def run_pipeline(
        self,
        initial_headline: str,
        source_type: NewsSource = NewsSource.MANUAL,
        source_channel: str = "pipeline_runner",
    ) -> ProvenanceReport:
        """
        Execute the full provenance-detection pipeline for one headline.

        Args:
            initial_headline: The raw news headline to trace.
            source_type:      Origin type for the created NewsItem.
            source_channel:   Feed URL / channel handle / 'manual'.

        Returns:
            ProvenanceReport — always returned; on catastrophic failure, contains
            risk_label=LOW with an error description in summary.
        """
        self._stage_logs = []
        self._current_stage = 0
        t_pipeline = time.perf_counter()

        self._print_header(initial_headline)

        try:
            # ── Stage 1: Wrap headline in NewsItem ──────────────────────────
            self._current_stage = 1
            news_item = NewsItem(
                headline=initial_headline,
                source_type=source_type,
                source_channel=source_channel,
            )
            self._record_stage(1, "Ingestion", f'NewsItem created — id={news_item.item_id[:8]}…', 0.0)

            # ── Stage 2: Semantic permutation generation ────────────────────
            self._current_stage = 2
            self._log_progress(2, "SemanticAgent", "Generating semantic permutations…")
            t2 = time.perf_counter()
            perm_set = await self.semantic_agent.process(news_item)
            e2 = time.perf_counter() - t2
            self._record_stage(
                2, "SemanticAgent",
                f"Generated {_b(perm_set.total_count)} permutations  "
                f"(model: {perm_set.model_used})",
                e2,
            )

            if perm_set.total_count == 0:
                logger.warning("SemanticAgent produced 0 permutations — pipeline will have no search queries.")

            # ── Stage 3: Broad concurrent web search ────────────────────────
            self._current_stage = 3
            self._log_progress(
                3, "BroadScraperAgent",
                f"Launching {perm_set.total_count} concurrent search queries "
                f"(backend: {self.scraper_agent.scraper.backend_name}, "
                f"concurrency: {self.scraper_agent.concurrency})…",
            )
            t3 = time.perf_counter()
            scraped_set = await self.scraper_agent.gather_sources(perm_set)
            e3 = time.perf_counter() - t3
            self._record_stage(
                3, "BroadScraperAgent",
                f"{_b(scraped_set.total_results_raw)} raw results → "
                f"{_b(len(scraped_set.results))} after dedup  "
                f"({scraped_set.deduplication_removed} duplicate(s) removed)",
                e3,
            )

            # ── Stage 4: Chronological sort + semantic similarity filter ─────
            self._current_stage = 4
            n_candidates = len(scraped_set.results)
            # similarity_threshold may not exist on mock agents — fall back to config
            threshold_display = getattr(
                self.analyzer_agent, "similarity_threshold",
                self.config.similarity_threshold,
            )
            self._log_progress(
                4, "AnalyzerAgent",
                f"Evaluating {n_candidates} candidate(s) for semantic similarity "
                f"(threshold: {threshold_display})…",
            )
            t4 = time.perf_counter()
            analyzed_set = await self.analyzer_agent.process(scraped_set)
            e4 = time.perf_counter() - t4
            n_passed = len(analyzed_set.ranked_results)
            n_top    = len(analyzed_set.top_candidates)
            self._record_stage(
                4, "AnalyzerAgent",
                f"{n_candidates} candidates → {_b(n_passed)} true positives above threshold → "
                f"top {_b(n_top)} selected for detection",
                e4,
            )

            # ── Stage 5: AI-signature detection ─────────────────────────────
            self._current_stage = 5
            self._log_progress(
                5, "Detector",
                f"Running ensemble AI-detection on {n_top} candidate(s)…",
            )
            t5 = time.perf_counter()
            report = await self.detector_agent.detect(analyzed_set)
            e5 = time.perf_counter() - t5
            risk_colour = _RISK_COLOURS.get(report.risk_label.value, _Y)
            self._record_stage(
                5, "Detector",
                f"Disinformation risk: {risk_colour}{_B}{report.risk_label}{_D}  "
                f"(score: {report.disinformation_risk:.2f}, "
                f"{len(report.ai_signature_results)} candidate(s) analysed)",
                e5,
            )

            # ── Patch total_duration_seconds ─────────────────────────────────
            total_elapsed = time.perf_counter() - t_pipeline
            report = report.model_copy(update={"total_duration_seconds": total_elapsed})

            self._print_footer(total_elapsed)
            return report

        except Exception as exc:
            logger.error(
                "Pipeline failed at Stage %d: %s",
                self._current_stage, exc,
                exc_info=True,
            )
            total_elapsed = time.perf_counter() - t_pipeline
            print(
                f"\n  {_R}{_B}[Stage {self._current_stage}]{_D}  "
                f"Pipeline failed: {exc}\n"
                f"  {_Y}→ Returning minimal ProvenanceReport.  "
                f"See logs for full traceback.{_D}\n"
            )
            return self._failure_report(initial_headline, exc, total_elapsed)

    # ── Lifecycle helpers ─────────────────────────────────────────────────────

    async def close(self) -> None:
        """Release resources held by agents (httpx clients, etc.)."""
        if hasattr(self.scraper_agent.scraper, "close"):
            await self.scraper_agent.scraper.close()  # type: ignore[union-attr]
        if hasattr(self.detector_agent, "close"):
            await self.detector_agent.close()
        logger.info("PipelineRunner closed.")

    async def __aenter__(self) -> "PipelineRunner":
        return self

    async def __aexit__(self, *_: object) -> None:
        await self.close()

    # ── Stage logging ─────────────────────────────────────────────────────────

    @property
    def stage_logs(self) -> list[StageLog]:
        """Per-stage telemetry from the most recent run_pipeline() call."""
        return list(self._stage_logs)

    def _record_stage(
        self,
        stage_num: int,
        stage_name: str,
        message: str,
        elapsed_s: float,
    ) -> None:
        """Store a StageLog entry and print it to the terminal."""
        log = StageLog(
            stage_num=stage_num,
            stage_name=stage_name,
            message=message,
            elapsed_s=elapsed_s,
        )
        self._stage_logs.append(log)
        # Print the coloured line (uses print so it always shows on stdout even
        # if log level filtering would suppress it).
        print(str(log))
        # Also emit to the Python logger for file-logging integrations.
        logger.info(
            "Stage %d (%s): %s  [%.1fs]",
            stage_num, stage_name, message, elapsed_s,
        )

    def _log_progress(self, stage_num: int, stage_name: str, message: str) -> None:
        """Print a lightweight 'in-progress' line (not stored in stage_logs)."""
        colour = _STAGE_COLOURS.get(stage_num, _C)
        print(
            f"  {colour}[Stage {stage_num}]{_D}  "
            f"{stage_name:<22}  ⏳ {message}"
        )

    # ── Display helpers ───────────────────────────────────────────────────────

    def _print_header(self, headline: str) -> None:
        bar = "═" * 72
        print(f"\n{_B}{bar}{_D}")
        print(f"{_B}  Provenance Pipeline — Analysis Run{_D}")
        print(f"  Headline: \"{headline[:68]}\"")
        print(f"{_B}{bar}{_D}\n")

    def _print_footer(self, elapsed: float) -> None:
        bar = "─" * 72
        print(f"\n  {bar}")
        print(f"  {_G}{_B}Pipeline complete{_D}  —  total elapsed: {_B}{elapsed:.1f}s{_D}")
        print(f"  {bar}\n")

    # ── Failure fallback ──────────────────────────────────────────────────────

    def _failure_report(
        self,
        headline: str,
        exc: Exception,
        elapsed_s: float,
    ) -> ProvenanceReport:
        """
        Minimal ProvenanceReport returned when a pipeline stage raises.
        Allows the caller to always handle a well-typed return value.
        """
        news_item = NewsItem(
            headline=headline,
            source_type=NewsSource.MANUAL,
            source_channel="pipeline_runner",
        )
        return ProvenanceReport(
            source_item            = news_item,
            ai_signature_results   = [],
            disinformation_risk    = 0.0,
            risk_label             = RiskLabel.LOW,
            total_duration_seconds = elapsed_s,
            summary=(
                f"Pipeline failed at Stage {self._current_stage}: {type(exc).__name__}: {exc}. "
                f"No analysis was completed."
            ),
        )


# ── Module-level convenience ──────────────────────────────────────────────────

def _b(value: object) -> str:
    """Bold-wrap a value for terminal output."""
    return f"{_B}{value}{_D}"
