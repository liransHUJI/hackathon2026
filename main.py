#!/usr/bin/env python3
"""
Provenance Pipeline — CLI Entry Point
======================================
Usage:
    # Analyse a single headline
    python main.py --item "Prime Minister announces surprise retirement"

    # Poll an RSS feed (IngestionAgent — coming soon)
    python main.py --rss https://feeds.bbci.co.uk/news/rss.xml

    # Poll a Telegram channel (IngestionAgent — coming soon)
    python main.py --telegram @channelhandle

Outputs:
    ProvenanceReport JSON → data/outputs/<report_id>.json
"""

from __future__ import annotations

import argparse
import asyncio
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

from src.config import PipelineConfig
from src.models.schemas import NewsItem, NewsSource

OUTPUTS_DIR = Path("data/outputs")


# ── CLI ───────────────────────────────────────────────────────────────────────

def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="provenance",
        description="Trace the origin of a news headline and detect AI-generated text.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
examples:
  python main.py --item "PM announces retirement"
  python main.py --rss https://feeds.bbci.co.uk/news/rss.xml
  python main.py --telegram @breakingnews
        """,
    )
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument(
        "--item",
        metavar="HEADLINE",
        help='Analyse a single headline string.',
    )
    source.add_argument(
        "--rss",
        metavar="URL",
        help="Poll an RSS feed continuously.",
    )
    source.add_argument(
        "--telegram",
        metavar="CHANNEL",
        help="Poll a Telegram channel.",
    )
    return parser


# ── Pipeline stub ──────────────────────────────────────────────────────────────

async def run_once(headline: str, config: PipelineConfig) -> None:
    """
    Run the full pipeline for a single headline.

    Currently a stub — creates and persists a NewsItem so the data contract
    is exercised.  Swap the body with PipelineRunner.run_once() once agents
    are implemented.
    """
    news_item = NewsItem(
        headline=headline,
        source_type=NewsSource.MANUAL,
        source_channel="cli",
        published_at=datetime.now(timezone.utc),
    )

    _print_banner()
    print(f"  Headline   : {news_item.headline}")
    print(f"  Item ID    : {news_item.item_id}")
    print(f"  Source     : {news_item.source_type}")
    print(f"  Ingested   : {news_item.ingested_at.isoformat()}")
    print(f"  LLM model  : {config.llm_model}")
    print(f"  Scraper    : {config.scraper_backend}")
    print(f"{'─' * 62}\n")

    print("  ⚙  Agents not yet implemented — skeleton only.")
    print("  Next step: build src/agents/ modules (see docs/ARCHITECTURE.md)\n")

    # Persist the NewsItem so the output directory + serialisation are tested.
    OUTPUTS_DIR.mkdir(parents=True, exist_ok=True)
    out_path = OUTPUTS_DIR / f"{news_item.item_id}_news_item.json"
    out_path.write_text(
        json.dumps(json.loads(news_item.model_dump_json()), indent=2)
    )
    print(f"  NewsItem written → {out_path}\n")


def _print_banner() -> None:
    print(f"\n{'─' * 62}")
    print("  🔎  Provenance Pipeline  —  v0.1.0")
    print(f"{'─' * 62}")


# ── Entry point ───────────────────────────────────────────────────────────────

def main() -> None:
    parser = build_parser()
    args = parser.parse_args()

    config = PipelineConfig()

    if not config.has_anthropic_key:
        print(
            "[WARN] ANTHROPIC_API_KEY is not set. "
            "The Semantic Agent and LLM-Judge will not function.\n"
            "       Copy .env.example → .env and add your key.\n",
            file=sys.stderr,
        )

    if args.item:
        asyncio.run(run_once(args.item, config))

    elif args.rss:
        print(
            "[INFO] RSS ingestion not yet implemented.\n"
            "       Coming in src/agents/ingestion_agent.py",
            file=sys.stderr,
        )
        sys.exit(1)

    elif args.telegram:
        print(
            "[INFO] Telegram ingestion not yet implemented.\n"
            "       Coming in src/agents/ingestion_agent.py",
            file=sys.stderr,
        )
        sys.exit(1)


if __name__ == "__main__":
    main()
