#!/usr/bin/env python3
"""
run_demo.py — End-to-End Provenance Pipeline Demo
===================================================
Instantiates every pipeline stage, runs a full analysis on a news headline,
and renders the ProvenanceReport to the terminal + saves JSON to disk.

Prerequisites
─────────────
  1. Create .env  (copy from .env.example)
  2. Set GEMINI_API_KEY=<your-key>   (free: https://aistudio.google.com)
  3. (Optional) Set ANTHROPIC_API_KEY for the LLM judge
     — local AI-detection methods run without paid detector APIs

Usage
─────
  python run_demo.py
  python run_demo.py --headline "Chancellor resigns following budget crisis"
  python run_demo.py --headline "..." --permutations 5 --max-results 3
  python run_demo.py --json-only        # suppress stage telemetry, print JSON only
  python run_demo.py --no-save          # don't write JSON to data/outputs/
"""

from __future__ import annotations

# ── Load .env FIRST — before any src imports that read os.environ ─────────────
import os
import sys
from pathlib import Path

from dotenv import load_dotenv
load_dotenv(Path(__file__).parent / ".env")

import argparse
import asyncio
import json
import logging
import time
from datetime import datetime, timezone

# ── Now safe to import from src/ ─────────────────────────────────────────────
from src.agents.ingestion_agent import DEFAULT_RSS_FEEDS, IngestionAgent
from src.config import PipelineConfig
from src.models.schemas import (
    AISignatureResult,
    ProvenanceReport,
    RiskLabel,
)
from src.pipeline_runner import PipelineRunner

# ── ANSI colours ──────────────────────────────────────────────────────────────
_G  = "\033[92m"
_R  = "\033[91m"
_Y  = "\033[93m"
_C  = "\033[96m"
_M  = "\033[95m"
_B  = "\033[1m"
_D  = "\033[0m"
_DIM = "\033[2m"

_RISK_COLOURS = {"LOW": _G, "MEDIUM": _Y, "HIGH": _Y, "CRITICAL": _R}
_RISK_BARS    = {"LOW": "███░░░░░░░", "MEDIUM": "██████░░░░", "HIGH": "████████░░", "CRITICAL": "██████████"}


# ════════════════════════════════════════════════════════════════════════════
#  ARGUMENT PARSER
# ════════════════════════════════════════════════════════════════════════════

def _build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        description="End-to-end Provenance Pipeline demo",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=(
            "Examples:\n"
            '  python run_demo.py\n'
            '  python run_demo.py --headline "War breaks out in Eastern Europe"\n'
            "  python run_demo.py --permutations 5 --max-results 3\n"
            "  python run_demo.py --json-only\n"
        ),
    )
    p.add_argument(
        "--headline",
        default="Israeli Prime Minister Benjamin Netanyahu announces retirement from politics",
        help="News headline to analyse (default: Netanyahu retirement example)",
    )
    p.add_argument(
        "--auto", action="store_true",
        help=(
            "Auto-ingest: fetch the latest headline from a live RSS feed and "
            "run the pipeline on it.  Overrides --headline."
        ),
    )
    p.add_argument(
        "--feed-url", default=None, metavar="URL",
        help=(
            "RSS/Atom feed URL used by --auto "
            "(default: BBC World https://feeds.bbci.co.uk/news/world/rss.xml)"
        ),
    )
    p.add_argument(
        "--permutations", type=int, default=None, metavar="N",
        help="Override PERMUTATION_COUNT from .env (keep small for demo speed)",
    )
    p.add_argument(
        "--max-results", type=int, default=None, metavar="N",
        help="Override max_results_per_query (DuckDuckGo results per search)",
    )
    p.add_argument(
        "--threshold", type=float, default=None, metavar="T",
        help="Override similarity_threshold for Stage 4",
    )
    p.add_argument(
        "--json-only", action="store_true",
        help="Suppress stage telemetry; print only the final JSON report",
    )
    p.add_argument(
        "--no-save", action="store_true",
        help="Do not write report JSON to data/outputs/",
    )
    p.add_argument(
        "--verbose", action="store_true",
        help="Enable DEBUG-level logging to stderr",
    )
    return p


# ════════════════════════════════════════════════════════════════════════════
#  PRE-FLIGHT CHECKS
# ════════════════════════════════════════════════════════════════════════════

def _check_prerequisites() -> bool:
    """Print key status and return False if any required key is missing."""
    gemini_key   = os.getenv("GEMINI_API_KEY") or os.getenv("GOOGLE_API_KEY")
    anthropic_key= os.getenv("ANTHROPIC_API_KEY")

    def _key_line(name: str, key: str | None, required: bool = False) -> str:
        if key:
            masked = key[:8] + "…" + key[-4:]
            return f"  {_G}✓{_D}  {name:<20}  {_DIM}{masked}{_D}"
        tag = f"{_R}[REQUIRED]{_D}" if required else f"{_DIM}[optional — method skipped]{_D}"
        return f"  {_Y}–{_D}  {name:<20}  {tag}"

    print(f"\n  {_B}API Key Status{_D}")
    print(f"  {'─' * 54}")
    print(_key_line("GEMINI_API_KEY",    gemini_key,    required=True))
    print(_key_line("ANTHROPIC_API_KEY", anthropic_key, required=False))
    print(f"  {_G}✓{_D}  {'LOCAL_AI_DETECTORS':<20}  {_DIM}no paid API key needed{_D}")
    print()

    if not gemini_key:
        print(f"  {_R}{_B}Error:{_D} GEMINI_API_KEY is required.")
        print(f"  {_Y}→ Get a free key at https://aistudio.google.com")
        print(f"  → Add GEMINI_API_KEY=AIza... to your .env file{_D}\n")
        return False
    return True


# ════════════════════════════════════════════════════════════════════════════
#  REPORT DISPLAY
# ════════════════════════════════════════════════════════════════════════════

def _risk_bar(risk_label: str, score: float) -> str:
    filled   = round(score * 10)
    bar_body = "█" * filled + "░" * (10 - filled)
    colour   = _RISK_COLOURS.get(risk_label, _Y)
    return f"{colour}{_B}{bar_body}{_D}  {colour}{_B}{risk_label}{_D} ({score:.2f})"


def _print_report(report: ProvenanceReport, stage_logs: list, total_elapsed: float) -> None:
    """Render a structured, human-readable summary of the ProvenanceReport."""
    bar72  = "═" * 72
    bar68  = "─" * 68
    bar54  = "─" * 54

    headline = report.source_item.headline

    # ── Header ────────────────────────────────────────────────────────────
    print(f"\n{_B}{bar72}{_D}")
    print(f"{_B}  PROVENANCE REPORT{_D}")
    print(f"{_B}{bar72}{_D}")
    print(f"\n  {_B}Headline:{_D}  \"{headline[:70]}\"")
    print(f"  {_B}Report ID:{_D} {report.report_id}")
    print(f"  {_B}Generated:{_D} {report.generated_at.strftime('%Y-%m-%d %H:%M:%S UTC')}")

    # ── Pipeline telemetry table ──────────────────────────────────────────
    if stage_logs:
        print(f"\n  {_B}Pipeline Telemetry{_D}")
        print(f"  {bar54}")
        # Stage 1 doesn't have timing so we start from index 1 (stages 2-5)
        for log in stage_logs:
            name_col = f"Stage {log.stage_num}  {log.stage_name}"
            # Strip ANSI from message for the table column
            msg_plain = _strip_ansi(log.message)
            # Keep message short for table display
            msg_short = msg_plain[:48] + "…" if len(msg_plain) > 48 else msg_plain
            print(f"  {_C}▸{_D}  {name_col:<30}  {msg_short:<50}  {_DIM}{log.elapsed_s:>5.1f}s{_D}")
        print(f"  {bar54}")
        print(f"  {'':>34}  {'':>50}  {_B}{total_elapsed:>5.1f}s{_D}  total")

    # ── Risk assessment ───────────────────────────────────────────────────
    print(f"\n  {_B}Risk Assessment{_D}")
    print(f"  {bar54}")
    print(f"  Disinformation Risk:  {_risk_bar(report.risk_label.value, report.disinformation_risk)}")
    if report.summary:
        summary_lines = [report.summary[i:i+64] for i in range(0, len(report.summary), 64)]
        print(f"\n  Summary:")
        for line in summary_lines:
            print(f"    {line}")

    # ── Earliest source ───────────────────────────────────────────────────
    if report.earliest_source:
        es = report.earliest_source.scraped_result
        date_s = es.published_at.strftime("%Y-%m-%d") if es.published_at else "unknown date"
        print(f"\n  {_B}Earliest Source Identified{_D}  {_G}{_B}← likely origin{_D}")
        print(f"  {bar54}")
        print(f"  Domain:    {_B}{es.domain}{_D}")
        print(f"  Published: {date_s}")
        print(f"  Title:     \"{(es.title or '')[:65]}\"")
        print(f"  Composite: {report.earliest_source.composite_score:.3f}  "
              f"│  Similarity: {report.earliest_source.similarity_score:.3f}  "
              f"│  Rank: #{report.earliest_source.chronological_rank}")

    # ── AI Signature Results table ────────────────────────────────────────
    if report.ai_signature_results:
        print(f"\n  {_B}AI Signature Results  ({len(report.ai_signature_results)} candidates){_D}")
        print(f"  {bar68}")
        print(
            f"  {_B}{'#':>2}  {'DOMAIN':<26}  {'DATE':<10}  "
            f"{'ENS':>5}  {'AI?':>5}  {'CONF':>4}  METHODS{_D}"
        )
        print(f"  {bar68}")
        for i, sig in enumerate(
            sorted(report.ai_signature_results, key=lambda s: s.ranked_result.chronological_rank), 1
        ):
            sr      = sig.ranked_result.scraped_result
            date_s  = sr.published_at.strftime("%Y-%m-%d") if sr.published_at else "no date  "
            ai_c    = _R if sig.is_ai_generated else _G
            ai_str  = f"{ai_c}{_B}{'YES':>5}{_D}" if sig.is_ai_generated else f"{_G}{'no':>5}{_D}"
            orig    = f"  {_G}{_B}★{_D}" if sig.ranked_result.is_likely_original else ""
            method_scores = "  ".join(
                f"{_DIM}{m.method_name[:4]}={'ERR' if m.error else f'{m.score:.2f}'}{_D}"
                for m in sig.detection_methods
                if m.error is None or True  # always show all methods
            )
            print(
                f"  {i:>2}.  {sr.domain:<26}  {date_s}  "
                f"{sig.ensemble_score:>5.2f}  {ai_str}  {sig.confidence:>4.2f}  "
                f"{method_scores}{orig}"
            )
        print(f"  {bar68}")

    print(f"\n{_B}{bar72}{_D}")


def _strip_ansi(text: str) -> str:
    """Remove ANSI escape codes from a string."""
    import re
    return re.sub(r"\033\[[0-9;]*m", "", text)


def _save_report(report: ProvenanceReport, output_dir: Path) -> Path:
    """Serialise the ProvenanceReport to data/outputs/<report_id>.json."""
    output_dir.mkdir(parents=True, exist_ok=True)
    out_path = output_dir / f"{report.report_id}.json"

    # Use Pydantic's serialiser — handles datetime, enums, nested models
    raw_dict = json.loads(report.model_dump_json())
    out_path.write_text(json.dumps(raw_dict, indent=2, ensure_ascii=False))
    return out_path


# ════════════════════════════════════════════════════════════════════════════
#  MAIN
# ════════════════════════════════════════════════════════════════════════════

async def main(args: argparse.Namespace) -> int:
    """Returns exit code: 0 = success, 1 = pipeline error, 2 = setup error."""

    if args.verbose:
        logging.basicConfig(
            level=logging.DEBUG,
            format="%(asctime)s  %(name)s  %(levelname)s  %(message)s",
            stream=sys.stderr,
        )
    else:
        # Suppress most library noise; show only WARNING+ unless --verbose
        logging.basicConfig(level=logging.WARNING, stream=sys.stderr)
        # But always show our own pipeline logger at INFO
        logging.getLogger("provenance").setLevel(logging.INFO)

    if not args.json_only:
        _print_banner()

    # ── Pre-flight ──────────────────────────────────────────────────────
    if not _check_prerequisites():
        return 2

    # ── Build config with any CLI overrides ─────────────────────────────
    try:
        config = PipelineConfig()
    except Exception as exc:
        print(f"  {_R}Config error: {exc}{_D}\n")
        return 2

    # Apply CLI overrides that affect agent construction
    if args.permutations:
        # Override permutation_count for this run
        config = config.model_copy(update={"permutation_count": args.permutations})
    if args.threshold is not None:
        config = config.model_copy(update={"similarity_threshold": args.threshold})

    # ── Auto-ingest: pull a live headline from RSS if --auto is set ──────────
    if args.auto:
        feed_url = args.feed_url or DEFAULT_RSS_FEEDS[0]
        if not args.json_only:
            print(f"  {_C}Auto-ingesting latest headline from:{_D}")
            print(f"  {_DIM}{feed_url}{_D}\n")
        try:
            ingestor = IngestionAgent()
            items = await ingestor.fetch_latest(feed_urls=[feed_url], limit=1)
        except Exception as exc:
            print(f"\n  {_R}{_B}Ingestion error:{_D} {exc}\n")
            return 2

        if not items:
            print(
                f"\n  {_R}{_B}Error:{_D} No items returned from feed.\n"
                f"  {_Y}→ Is the feed URL reachable? Try a different --feed-url.{_D}\n"
            )
            return 2

        headline = items[0].headline
        if not args.json_only:
            pub = items[0].published_at
            pub_str = pub.strftime("%Y-%m-%d %H:%M UTC") if pub else "unknown date"
            print(f"  {_G}✓{_D}  Auto-ingested headline  {_DIM}({pub_str}){_D}")
            print(f"  {_B}\"{headline[:72]}\"{_D}\n")
    else:
        headline = args.headline

    # ── Build runner ────────────────────────────────────────────────────
    if not args.json_only:
        print(f"  {_C}Building pipeline components…{_D}")
    try:
        runner = PipelineRunner.from_config(config)
    except ValueError as exc:
        print(f"\n  {_R}{_B}Setup error:{_D} {exc}\n")
        return 2
    except ImportError as exc:
        print(f"\n  {_R}{_B}Import error:{_D} {exc}\n")
        return 2

    # Override max_results_per_query if specified
    if args.max_results is not None:
        runner.scraper_agent.max_results_per_query = args.max_results

    # ── Execute pipeline ─────────────────────────────────────────────────
    t_start = time.perf_counter()
    async with runner:
        report = await runner.run_pipeline(headline)
        total_elapsed = time.perf_counter() - t_start
        stage_logs    = runner.stage_logs

    # ── Display report ───────────────────────────────────────────────────
    if not args.json_only:
        _print_report(report, stage_logs, total_elapsed)

    # ── Print full JSON ──────────────────────────────────────────────────
    raw_dict  = json.loads(report.model_dump_json())
    json_text = json.dumps(raw_dict, indent=2, ensure_ascii=False)

    if args.json_only:
        print(json_text)
    else:
        print(f"\n  {_B}Full JSON Report{_D}")
        print(f"  {'─' * 68}")
        # Print the JSON with a 2-space indent prefix for visual alignment
        for line in json_text.splitlines():
            print(f"  {line}")
        print()

    # ── Save to disk ─────────────────────────────────────────────────────
    if not args.no_save:
        try:
            out_dir  = Path(__file__).parent / "data" / "outputs"
            out_path = _save_report(report, out_dir)
            if not args.json_only:
                print(f"  {_G}✓{_D}  Report saved to {_B}{out_path}{_D}\n")
        except Exception as exc:
            print(f"  {_Y}⚠{_D}  Could not save report: {exc}\n")

    # Indicate failure if the pipeline errored out
    if "Pipeline failed" in report.summary:
        return 1
    return 0


def _print_banner() -> None:
    bar = "═" * 72
    print(f"\n{_B}{bar}{_D}")
    print(f"{_B}  Provenance Pipeline  —  End-to-End Demo{_D}")
    print(f"  Tracing the origin of news in the age of AI.")
    print(f"{_B}{bar}{_D}")


if __name__ == "__main__":
    args = _build_parser().parse_args()
    sys.exit(asyncio.run(main(args)))
