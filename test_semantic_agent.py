#!/usr/bin/env python3
"""
Semantic Agent — Live Smoke Test
==================================
Verifies end-to-end that the SemanticAgent:
  1. Loads GEMINI_API_KEY from .env
  2. Calls the Gemini API with a structured-output schema
  3. Receives a response and parses it into typed Pydantic objects
  4. Returns a valid PermutationSet with the expected number of Permutations

Usage:
    python test_semantic_agent.py
    python test_semantic_agent.py --headline "Your custom headline here"
    python test_semantic_agent.py --count 5 --model gemini-2.0-flash

Prerequisites:
    • Add GEMINI_API_KEY=<your-key> to .env  (free at aistudio.google.com)
    • pip install -r requirements.txt
"""

from __future__ import annotations

import argparse
import asyncio
import os
import sys
import time
from pathlib import Path

# ── Load .env FIRST, before any src imports that read env vars ────────────────
from dotenv import load_dotenv

load_dotenv(Path(__file__).parent / ".env")

# ── Now safe to import from src/ ──────────────────────────────────────────────
from src.models.schemas import NewsItem, NewsSource, Permutation, PermutationSet
from src.agents.semantic_agent import SemanticAgent

# ── ANSI colours for terminal output ──────────────────────────────────────────
_GREEN  = "\033[92m"
_RED    = "\033[91m"
_YELLOW = "\033[93m"
_CYAN   = "\033[96m"
_BOLD   = "\033[1m"
_RESET  = "\033[0m"

def ok(msg: str)   -> str: return f"{_GREEN}✓{_RESET}  {msg}"
def err(msg: str)  -> str: return f"{_RED}✗{_RESET}  {msg}"
def warn(msg: str) -> str: return f"{_YELLOW}⚠{_RESET}  {msg}"
def info(msg: str) -> str: return f"{_CYAN}ℹ{_RESET}  {msg}"


# ── Core test logic ────────────────────────────────────────────────────────────

async def run_smoke_test(headline: str, count: int, model: str) -> bool:
    """
    Execute the full SemanticAgent pipeline for one headline.
    Returns True on success, False on failure.
    """
    print(f"\n{_BOLD}{'═' * 62}{_RESET}")
    print(f"{_BOLD}  Provenance Pipeline — SemanticAgent Smoke Test{_RESET}")
    print(f"{_BOLD}{'═' * 62}{_RESET}\n")

    # ── 1. Check API key ──────────────────────────────────────────────────────
    api_key = os.getenv("GEMINI_API_KEY") or os.getenv("GOOGLE_API_KEY")
    if not api_key:
        print(err("GEMINI_API_KEY not found in environment."))
        print(f"   {_YELLOW}→ Copy .env.example to .env and set GEMINI_API_KEY=<your-key>{_RESET}")
        print(f"   {_YELLOW}→ Get a free key at https://aistudio.google.com{_RESET}\n")
        return False
    masked = api_key[:8] + "..." + api_key[-4:]
    print(ok(f"GEMINI_API_KEY loaded  ({masked})"))

    # ── 2. Build the NewsItem ─────────────────────────────────────────────────
    news_item = NewsItem(
        headline=headline,
        source_type=NewsSource.MANUAL,
        source_channel="smoke_test",
    )
    print(ok(f"NewsItem created       item_id={news_item.item_id[:8]}…"))
    print(info(f"Headline: \"{news_item.headline}\""))
    print(info(f"Model:     {model}  |  Target count: {count}\n"))

    # ── 3. Instantiate the agent ──────────────────────────────────────────────
    try:
        agent = SemanticAgent(
            name="smoke_test",
            api_key=api_key,
            model=model,
            permutation_count=count,
        )
        print(ok("SemanticAgent instantiated"))
    except (ValueError, ImportError) as exc:
        print(err(f"Failed to instantiate SemanticAgent: {exc}"))
        return False

    # ── 4. Call the API ───────────────────────────────────────────────────────
    print(f"\n  ⏳ Calling {_CYAN}{model}{_RESET} API …\n")
    t0 = time.perf_counter()
    try:
        result: PermutationSet = await agent.process(news_item)
    except Exception as exc:
        print(err(f"agent.process() raised an exception: {exc}"))
        return False
    elapsed = time.perf_counter() - t0

    # ── 5. Validate the returned PermutationSet ───────────────────────────────
    print(f"  {'─' * 60}")
    failures: list[str] = []

    # Type check
    if not isinstance(result, PermutationSet):
        failures.append(f"Return type is {type(result).__name__}, expected PermutationSet")
    else:
        print(ok(f"Return type is PermutationSet"))

    # Source item preserved
    if result.source_item.item_id != news_item.item_id:
        failures.append("source_item.item_id mismatch — original NewsItem not preserved")
    else:
        print(ok(f"source_item.item_id matches original NewsItem"))

    # Permutation count
    actual_count = len(result.permutations)
    if actual_count == 0:
        failures.append("permutations list is empty")
    else:
        count_ok = actual_count == count
        msg = f"{actual_count} permutation(s) received (expected {count})"
        print(ok(msg) if count_ok else warn(msg + "  — Gemini may have produced fewer"))

    # All items are Permutation instances
    bad_types = [i for i, p in enumerate(result.permutations) if not isinstance(p, Permutation)]
    if bad_types:
        failures.append(f"Items at indices {bad_types} are not Permutation instances")
    elif result.permutations:
        print(ok(f"All {actual_count} items are valid Pydantic Permutation objects"))

    # All texts are non-empty strings
    empty_texts = [i for i, p in enumerate(result.permutations) if not p.text.strip()]
    if empty_texts:
        failures.append(f"Empty text field at indices {empty_texts}")
    elif result.permutations:
        print(ok(f"All permutation texts are non-empty strings"))

    # No verbatim repeat of the original headline
    verbatim = [
        p.text for p in result.permutations
        if p.text.strip().lower() == headline.strip().lower()
    ]
    if verbatim:
        print(warn(f"  {len(verbatim)} permutation(s) are verbatim copies of the original"))
    else:
        print(ok("No permutation is a verbatim copy of the original headline"))

    # Confidence values in range
    bad_conf = [
        (i, p.confidence) for i, p in enumerate(result.permutations)
        if not (0.0 <= p.confidence <= 1.0)
    ]
    if bad_conf:
        failures.append(f"Confidence out of [0,1] range at {bad_conf}")
    elif result.permutations:
        print(ok(f"All confidence scores are in [0.0, 1.0]"))

    # model_used populated
    if not result.model_used:
        print(warn("model_used field is empty"))
    else:
        print(ok(f"model_used = \"{result.model_used}\""))

    print(f"  {'─' * 60}")

    # ── 6. Print permutation table ────────────────────────────────────────────
    print(f"\n  {_BOLD}Generated Permutations{_RESET}\n")
    strategy_width = max((len(p.strategy) for p in result.permutations), default=10)
    header = f"  {'#':>3}  {'STRATEGY':<{strategy_width}}  {'CONF':>4}  TEXT"
    print(f"  {_CYAN}{header[2:]}{_RESET}")
    print(f"  {'─' * 58}")
    for i, p in enumerate(result.permutations, 1):
        conf_str = f"{p.confidence:.2f}"
        # Fallback permutations are flagged in yellow
        colour = _YELLOW if p.strategy == "fallback_original" else ""
        reset  = _RESET  if colour else ""
        print(
            f"  {i:>3}.  {colour}{p.strategy:<{strategy_width}}  "
            f"{conf_str:>4}  {p.text}{reset}"
        )
    print(f"  {'─' * 58}")
    print(f"\n  Elapsed: {elapsed:.2f}s   Total count: {result.total_count}")

    # ── 7. Final verdict ──────────────────────────────────────────────────────
    print(f"\n{'═' * 62}")
    if failures:
        print(f"{_RED}{_BOLD}  FAILED{_RESET}  —  {len(failures)} assertion(s) did not pass:")
        for f in failures:
            print(f"    {err(f)}")
        print(f"{'═' * 62}\n")
        return False

    is_fallback = any(p.strategy == "fallback_original" for p in result.permutations)
    if is_fallback:
        print(
            f"{_YELLOW}{_BOLD}  PARTIAL — fallback permutation returned.{_RESET}\n"
            f"  The API call likely failed; check your GEMINI_API_KEY and quota."
        )
    else:
        print(
            f"{_GREEN}{_BOLD}  ALL CHECKS PASSED ✓{_RESET}\n"
            f"  SemanticAgent is fully operational.\n"
            f"  PermutationSet is ready to hand off to BroadScraperAgent."
        )
    print(f"{'═' * 62}\n")
    return not is_fallback


# ── CLI entrypoint ─────────────────────────────────────────────────────────────

def _build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        description="Smoke test for SemanticAgent (calls live Gemini API)",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=(
            "Examples:\n"
            '  python test_semantic_agent.py\n'
            '  python test_semantic_agent.py --headline "War breaks out in Eastern Europe"\n'
            "  python test_semantic_agent.py --count 5 --model gemini-2.0-flash\n"
        ),
    )
    p.add_argument(
        "--headline",
        default="The Prime Minister is retiring after a decade in power",
        help="News headline to generate permutations for (default: PM retirement example)",
    )
    p.add_argument(
        "--count",
        type=int,
        default=10,
        metavar="N",
        help="Number of permutations to generate (default: 10)",
    )
    p.add_argument(
        "--model",
        default=os.getenv("GEMINI_MODEL", "gemini-2.0-flash"),
        help="Gemini model to use (default: gemini-2.0-flash)",
    )
    return p


if __name__ == "__main__":
    args = _build_parser().parse_args()
    success = asyncio.run(
        run_smoke_test(
            headline=args.headline,
            count=args.count,
            model=args.model,
        )
    )
    sys.exit(0 if success else 1)
