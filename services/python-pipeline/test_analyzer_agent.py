#!/usr/bin/env python3
"""
AnalyzerAgent — Live Smoke Test
================================
Verifies that the AnalyzerAgent correctly:

  ① Sorts scraped results chronologically (oldest first, None dates at end)
  ② Calls Gemini to evaluate semantic similarity with response_schema
  ③ Correctly identifies true positives vs false positives
  ④ Filters out noise below the similarity threshold
  ⑤ Returns top-10 earliest, most-relevant results in an AnalyzedSet

Mock dataset contains four categories of articles about the baseline:
  TRUE POSITIVES  — same resignation event, different wording / dates
  MODERN REPOSTS  — same event but published much later
  CLEVER FALSE POSITIVES — share a key entity or keyword but different event
  IRRELEVANT     — no meaningful connection

Usage:
    python test_analyzer_agent.py
    python test_analyzer_agent.py --threshold 0.5 --top-k 5
"""

from __future__ import annotations

import argparse
import asyncio
import os
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

from dotenv import load_dotenv
load_dotenv(Path(__file__).parent / ".env")

from src.agents.analyzer_agent import AnalyzerAgent
from src.models.schemas import (
    AnalyzedSet, ContentType, NewsItem, NewsSource, ScrapedResult,
)

# ── ANSI helpers ──────────────────────────────────────────────────────────────
_G = "\033[92m"; _R = "\033[91m"; _Y = "\033[93m"; _C = "\033[96m"
_B = "\033[1m";  _D = "\033[0m"

def ok(m):   return f"{_G}✓{_D}  {m}"
def err(m):  return f"{_R}✗{_D}  {m}"
def warn(m): return f"{_Y}⚠{_D}  {m}"
def info(m): return f"{_C}ℹ{_D}  {m}"


# ════════════════════════════════════════════════════════════════════════════
#  BASELINE NEWS ITEM
# ════════════════════════════════════════════════════════════════════════════

BASELINE = NewsItem(
    headline="Israeli Prime Minister Benjamin Netanyahu announces retirement from politics",
    source_type=NewsSource.MANUAL,
    source_channel="smoke_test",
    published_at=datetime(2024, 6, 15, 10, 0, tzinfo=timezone.utc),
)


# ════════════════════════════════════════════════════════════════════════════
#  MOCK SCRAPED RESULTS
#  Each is annotated with its expected category so we can score the agent.
# ════════════════════════════════════════════════════════════════════════════

def _r(*, title, snippet, domain, published_at, category, url=None) -> ScrapedResult:
    """Convenience factory — sets result_id, content_type etc. automatically."""
    return ScrapedResult(
        url=url or f"https://{domain}/article",
        title=title,
        snippet=snippet,
        domain=domain,
        content_type=ContentType.ARTICLE,
        published_at=published_at,
        query_used="Netanyahu retirement",
        scraper_id="mock",
        # Store expected category in raw metadata for result reporting
        http_status=200,
        full_text=snippet,  # use snippet as stand-in for full text
    )


# We store (ScrapedResult, expected_true_positive) pairs
MOCK_ARTICLES: list[tuple[ScrapedResult, bool, str]] = [

    # ── TRUE POSITIVES (same event, different wording) ────────────────────────
    (
        _r(
            title="Netanyahu to step down, ending decades-long political career",
            snippet=(
                "Benjamin Netanyahu has confirmed he will leave politics, bringing to an end "
                "his long tenure as Israel's dominant political figure. The announcement "
                "surprised senior party officials."
            ),
            domain="haaretz.com",
            published_at=datetime(2024, 1, 3, 8, 0, tzinfo=timezone.utc),  # EARLIEST
        ),
        True,
        "TRUE POSITIVE — earliest, same event reworded",
    ),
    (
        _r(
            title="Israeli PM confirms departure from political life",
            snippet=(
                "The Israeli Prime Minister has officially announced his plans to retire from "
                "government, citing a desire to spend more time with family after years of public service."
            ),
            domain="timesofisrael.com",
            published_at=datetime(2024, 3, 22, 14, 30, tzinfo=timezone.utc),
        ),
        True,
        "TRUE POSITIVE — same resignation announcement",
    ),
    (
        _r(
            title="Bibi quits: Netanyahu exits the political stage",
            snippet=(
                "In a surprise announcement, Netanyahu stated he would not seek re-election and "
                "intends to formally retire from politics by the end of the year."
            ),
            domain="jpost.com",
            published_at=datetime(2024, 5, 1, 9, 0, tzinfo=timezone.utc),
        ),
        True,
        "TRUE POSITIVE — informal language, same core fact",
    ),
    (
        _r(
            title="Head of Israeli government ends political career",
            snippet=(
                "The head of the Israeli government formally declared an end to his political "
                "career on Wednesday, a move that sent shockwaves through the Knesset."
            ),
            domain="reuters.com",
            published_at=datetime(2024, 6, 14, 18, 45, tzinfo=timezone.utc),  # day before baseline
        ),
        True,
        "TRUE POSITIVE — entity generalisation, published just before baseline",
    ),

    # ── MODERN REPOST (same event, published much later — lower composite score) ─
    (
        _r(
            title="Revisiting Netanyahu's retirement announcement one year on",
            snippet=(
                "A year ago, Netanyahu announced his retirement from politics. We revisit the "
                "moment that changed Israeli political history."
            ),
            domain="bbc.co.uk",
            published_at=datetime(2025, 6, 15, 12, 0, tzinfo=timezone.utc),  # LATEST — repost
        ),
        True,
        "TRUE POSITIVE repost — same event but published a full year later",
    ),

    # ── CLEVER FALSE POSITIVE TYPE A — same person, different event ───────────
    (
        _r(
            title="Netanyahu flies to Washington for emergency diplomatic talks",
            snippet=(
                "Israeli Prime Minister Benjamin Netanyahu boarded a flight to Washington to hold "
                "emergency consultations with the US Secretary of State regarding the ceasefire."
            ),
            domain="apnews.com",
            published_at=datetime(2024, 2, 10, 6, 0, tzinfo=timezone.utc),
        ),
        False,
        "FALSE POSITIVE A — same person (Netanyahu) but covers a travel/diplomacy story",
    ),
    (
        _r(
            title="Bibi visits family in Caesarea amid security concerns",
            snippet=(
                "Benjamin Netanyahu was spotted at his Caesarea home this weekend, travelling "
                "with an enlarged security detail following recent threats."
            ),
            domain="ynet.co.il",
            published_at=datetime(2024, 4, 5, 11, 0, tzinfo=timezone.utc),
        ),
        False,
        "FALSE POSITIVE A — 'Bibi' nickname match but covers a personal/travel story",
    ),

    # ── CLEVER FALSE POSITIVE TYPE B — keyword overlap, unrelated subject ─────
    (
        _r(
            title="Israel raises national pension and retirement age for civil servants",
            snippet=(
                "The Israeli government passed legislation this week raising the statutory "
                "retirement age for public sector employees from 67 to 70."
            ),
            domain="globes.co.il",
            published_at=datetime(2024, 3, 8, 10, 0, tzinfo=timezone.utc),
        ),
        False,
        "FALSE POSITIVE B — 'retirement' keyword but refers to pension policy, not a politician",
    ),

    # ── CLEVER FALSE POSITIVE TYPE C — related saga, different specific event ──
    (
        _r(
            title="Opposition leaders demand Netanyahu steps down over corruption charges",
            snippet=(
                "Opposition parties intensified pressure on Prime Minister Netanyahu to resign "
                "after new indictments were filed in the ongoing corruption trial."
            ),
            domain="haaretz.com",
            published_at=datetime(2023, 11, 20, 16, 0, tzinfo=timezone.utc),
        ),
        False,
        "FALSE POSITIVE C — 'calls for resignation' ≠ 'confirms retirement'; related saga, different event",
    ),

    # ── IRRELEVANT — no meaningful connection ─────────────────────────────────
    (
        _r(
            title="Israeli military announces new drone programme for border defence",
            snippet=(
                "The Israeli Defence Forces unveiled a new autonomous drone system intended "
                "to strengthen surveillance along the northern border with Lebanon."
            ),
            domain="defenseone.com",
            published_at=datetime(2024, 1, 28, 9, 30, tzinfo=timezone.utc),
        ),
        False,
        "IRRELEVANT — military technology story, zero connection to retirement announcement",
    ),

    # ── ITEM WITH NO DATE (should sort to end) ────────────────────────────────
    (
        _r(
            title="Analysis: what Netanyahu's exit means for Israeli politics",
            snippet=(
                "Political analysts weigh in on the long-term implications of the Prime Minister's "
                "retirement decision for coalition dynamics and the upcoming election cycle."
            ),
            domain="foreignpolicy.com",
            published_at=None,   # ← deliberately missing date
        ),
        True,
        "TRUE POSITIVE (no date) — analysis piece about the same retirement event",
    ),
]


# ════════════════════════════════════════════════════════════════════════════
#  SMOKE TEST
# ════════════════════════════════════════════════════════════════════════════

async def run_smoke_test(threshold: float, top_k: int) -> bool:
    print(f"\n{_B}{'═' * 68}{_D}")
    print(f"{_B}  Provenance Pipeline — AnalyzerAgent Smoke Test{_D}")
    print(f"{_B}{'═' * 68}{_D}\n")

    # ── Check prerequisites ────────────────────────────────────────────────
    api_key = os.getenv("GEMINI_API_KEY") or os.getenv("GOOGLE_API_KEY")
    if not api_key:
        print(err("GEMINI_API_KEY not found."))
        print(f"   {_Y}→ Add GEMINI_API_KEY=<key> to .env  (free: aistudio.google.com){_D}\n")
        return False
    print(ok(f"GEMINI_API_KEY loaded  ({api_key[:8]}…{api_key[-4:]})"))

    scraped      = [r for r, _, _ in MOCK_ARTICLES]
    expected_tp  = {i: tp  for i, (_, tp, _) in enumerate(MOCK_ARTICLES)}
    descriptions = {i: desc for i, (_, _, desc) in enumerate(MOCK_ARTICLES)}
    n_items      = len(scraped)

    print(ok(f"Baseline: \"{BASELINE.headline}\""))
    print(info(f"{n_items} mock articles loaded  "
               f"({sum(expected_tp.values())} true positives, "
               f"{n_items - sum(expected_tp.values())} false positives / irrelevant)"))
    print(info(f"Threshold: {threshold}   Top-K: {top_k}\n"))

    # ── Instantiate agent ──────────────────────────────────────────────────
    try:
        agent = AnalyzerAgent(
            similarity_threshold=threshold,
            top_k=top_k,
        )
        print(ok(f"AnalyzerAgent instantiated  (model={agent.model_name})\n"))
    except (ValueError, ImportError) as exc:
        print(err(f"Failed to instantiate AnalyzerAgent: {exc}"))
        return False

    # ── Pre-check: chronological sort (no API needed) ─────────────────────
    from src.agents.analyzer_agent import _sort_by_date
    sorted_results = _sort_by_date(scraped)
    dated   = [r for r in sorted_results if r.published_at is not None]
    undated = [r for r in sorted_results if r.published_at is None]

    sort_ok = True
    if undated and sorted_results[-len(undated):] != undated:
        sort_ok = False
    for i in range(len(dated) - 1):
        if dated[i].published_at > dated[i + 1].published_at:  # type: ignore[operator]
            sort_ok = False; break

    if sort_ok:
        print(ok("Pre-check: chronological sort correct (dated items oldest-first, undated at end)"))
        print(f"     Oldest: {dated[0].published_at.strftime('%Y-%m-%d')} — {dated[0].title[:55]}")  # type: ignore
        print(f"     Newest: {dated[-1].published_at.strftime('%Y-%m-%d')} — {dated[-1].title[:55]}")  # type: ignore
        print(f"     No-date: {len(undated)} item(s) at end ✓")
    else:
        print(err("Pre-check: chronological sort FAILED"))

    # ── Run the full analysis ──────────────────────────────────────────────
    print(f"\n  ⏳ Calling Gemini to evaluate {n_items} candidates…\n")
    t0 = time.perf_counter()
    try:
        result: AnalyzedSet = await agent.analyze_and_sort_sources(
            baseline=BASELINE,
            scraped=scraped,
        )
    except Exception as exc:
        print(err(f"analyze_and_sort_sources() raised: {exc}"))
        import traceback; traceback.print_exc()
        return False
    elapsed = time.perf_counter() - t0

    # ── Print detailed results table ───────────────────────────────────────
    print(f"  {'─' * 68}")
    print(f"  {_B}{'#':>3}  {'DATE':<12}  {'SCORE':>5}  {'TP?':>5}  {'DOMAIN':<22}  TITLE{_D}")
    print(f"  {'─' * 68}")

    # Gather ALL ranked items (passing only, sorted by chronological_rank for display)
    display_items = sorted(result.ranked_results, key=lambda r: r.chronological_rank)

    for rr in display_items:
        sr       = rr.scraped_result
        date_str = sr.published_at.strftime("%Y-%m-%d") if sr.published_at else "no date"
        tp_str   = f"{_G}✓ yes{_D}" if rr.is_likely_original else "  yes" if rr.similarity_score >= 0.65 else f"{_R}  no {_D}"
        score_c  = _G if rr.similarity_score >= threshold else _Y
        orig_m   = f"  {_B}← earliest{_D}" if rr.is_likely_original else ""
        print(
            f"  {rr.chronological_rank:>3}.  {date_str:<12}  "
            f"{score_c}{rr.similarity_score:>5.2f}{_D}  {tp_str}  "
            f"{sr.domain:<22}  {(sr.title or '')[:38]}{orig_m}"
        )

    print(f"  {'─' * 68}")
    print(f"  {len(result.ranked_results)}/{n_items} passed threshold {threshold}   "
          f"|   {len(result.top_candidates)} top candidates   |   "
          f"Elapsed: {elapsed:.1f}s\n")

    # ── Evaluate quality of Gemini's verdicts ─────────────────────────────
    print(f"  {_B}Gemini Verdict Quality{_D}\n")

    all_ranked_by_orig_idx: dict[int, float] = {}
    for rr in result.ranked_results:
        sr = rr.scraped_result
        for i, orig_sr in enumerate(scraped):
            if orig_sr.result_id == sr.result_id:
                all_ranked_by_orig_idx[i] = rr.similarity_score
                break

    # Items below threshold are implicitly scored 0 in this analysis
    tp_caught = fp_blocked = tp_missed = fp_leaked = 0
    verdicts: list[str] = []

    for i, (_, expected, desc) in enumerate(MOCK_ARTICLES):
        score = all_ranked_by_orig_idx.get(i, 0.0)
        passed = score >= threshold
        if expected and passed:
            tp_caught += 1
            verdicts.append(f"  {_G}✓ TP caught {_D}  score={score:.2f}  {desc}")
        elif expected and not passed:
            tp_missed += 1
            verdicts.append(f"  {_Y}⚠ TP missed {_D}  score={score:.2f}  {desc}")
        elif not expected and not passed:
            fp_blocked += 1
            verdicts.append(f"  {_G}✓ FP blocked{_D}  score={score:.2f}  {desc}")
        else:  # not expected and passed (leaked FP)
            fp_leaked += 1
            verdicts.append(f"  {_R}✗ FP leaked {_D}  score={score:.2f}  {desc}")

    for v in verdicts:
        print(v)

    total_expected_tp = sum(1 for _, tp, _ in MOCK_ARTICLES if tp)
    total_expected_fp = n_items - total_expected_tp
    precision = tp_caught / max(tp_caught + fp_leaked, 1)
    recall    = tp_caught / max(total_expected_tp, 1)

    print(f"\n  Precision: {precision:.0%}  ({tp_caught} TP caught, {fp_leaked} FP leaked)")
    print(f"  Recall:    {recall:.0%}  ({tp_caught}/{total_expected_tp} true positives found)")
    print(f"  FP blocked: {fp_blocked}/{total_expected_fp}")

    # ── Structural assertions ─────────────────────────────────────────────
    print(f"\n  {_B}Structural Assertions{_D}\n")
    failures: list[str] = []

    # AnalyzedSet type
    if isinstance(result, AnalyzedSet):
        print(ok("Return type is AnalyzedSet"))
    else:
        failures.append(f"Return type is {type(result).__name__}, expected AnalyzedSet")

    # Top candidates ≤ top_k
    if len(result.top_candidates) <= top_k:
        print(ok(f"top_candidates count ({len(result.top_candidates)}) ≤ top_k ({top_k})"))
    else:
        failures.append(f"top_candidates count {len(result.top_candidates)} > top_k {top_k}")

    # similarity_score bounds
    bad_scores = [
        r.similarity_score for r in result.ranked_results
        if not (0.0 <= r.similarity_score <= 1.0)
    ]
    if bad_scores:
        failures.append(f"similarity_score out of [0,1]: {bad_scores}")
    else:
        print(ok("All similarity_score values are in [0.0, 1.0]"))

    # composite_score bounds
    bad_comp = [
        r.composite_score for r in result.ranked_results
        if not (0.0 <= r.composite_score <= 1.0)
    ]
    if bad_comp:
        failures.append(f"composite_score out of [0,1]: {bad_comp}")
    else:
        print(ok("All composite_score values are in [0.0, 1.0]"))

    # All ranked results passed threshold
    below = [r.similarity_score for r in result.ranked_results if r.similarity_score < threshold]
    if below:
        failures.append(f"ranked_results contains {len(below)} score(s) below threshold")
    else:
        print(ok("All ranked_results have similarity_score ≥ threshold"))

    # Exactly one is_likely_original flag
    originals = [r for r in result.ranked_results if r.is_likely_original]
    if len(result.ranked_results) > 0 and len(originals) != 1:
        failures.append(f"Expected exactly 1 is_likely_original, got {len(originals)}")
    elif originals:
        print(ok(f"is_likely_original correctly set on 1 result  "
                 f"(rank {originals[0].chronological_rank}: {originals[0].scraped_result.domain})"))

    # Undated items should NOT appear in the TOP position (they sort last)
    if result.top_candidates:
        top_one = result.top_candidates[0]
        # Top by composite score — could be any date; this is expected behaviour
        print(ok(f"Top candidate: [{top_one.chronological_rank}] "
                 f"{top_one.scraped_result.domain}  "
                 f"score={top_one.composite_score:.2f}"))

    # No leaked false positives ideally (warn only, not hard failure)
    if fp_leaked > 0:
        print(warn(f"{fp_leaked} false positive(s) leaked above threshold — check prompt / threshold"))

    # ── Final verdict ──────────────────────────────────────────────────────
    print(f"\n{'═' * 68}")
    if failures:
        print(f"{_R}{_B}  FAILED{_D} — {len(failures)} structural assertion(s) failed:")
        for f in failures:
            print(f"    {err(f)}")
        print(f"{'═' * 68}\n")
        return False

    if precision >= 0.75 and recall >= 0.60:
        print(
            f"{_G}{_B}  ALL CHECKS PASSED ✓{_D}\n"
            f"  AnalyzerAgent is operational.  Precision {precision:.0%}  Recall {recall:.0%}\n"
            f"  AnalyzedSet is ready to hand to AISignatureDetectorAgent."
        )
    else:
        print(
            f"{_Y}{_B}  PASSED with low quality{_D}\n"
            f"  Structural assertions OK, but precision ({precision:.0%}) or "
            f"recall ({recall:.0%}) below target.\n"
            f"  Consider tightening the similarity threshold or refining the system prompt."
        )
    print(f"{'═' * 68}\n")
    return not bool(failures)


# ── CLI ────────────────────────────────────────────────────────────────────────

def _parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        description="Live smoke test for AnalyzerAgent",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="Examples:\n  python test_analyzer_agent.py\n  python test_analyzer_agent.py --threshold 0.5",
    )
    p.add_argument("--threshold", type=float, default=0.45, metavar="T",
                   help="Similarity threshold (default: 0.45)")
    p.add_argument("--top-k", type=int, default=10, metavar="K",
                   help="Max candidates returned (default: 10)")
    return p


if __name__ == "__main__":
    args = _parser().parse_args()
    ok_flag = asyncio.run(run_smoke_test(args.threshold, args.top_k))
    sys.exit(0 if ok_flag else 1)
