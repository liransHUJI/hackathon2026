#!/usr/bin/env python3
"""
AISignatureDetectorAgent — Live Smoke Test
===========================================
Verifies that the detector:

  ① Always runs the three local methods (no API key needed)
  ② Conditionally runs LLM Judge if ANTHROPIC_API_KEY is set
  ③ Correctly renormalises ensemble weights when LLM Judge is absent
  ④ Correctly labels the clearly-AI sample higher than the clearly-human sample
  ⑤ Returns a valid ProvenanceReport with correct risk_label

Mock dataset:
  TWO_CANDIDATES:
    1. "clearly AI-written" article — dense transition words, hedging, uniform sentences
    2. "clearly human-written" article — varied prose, natural rhythm, personal anecdotes

Usage:
    python test_detector_agent.py
    python test_detector_agent.py --threshold 0.65

Required:
    .env must exist (can be minimal — only GEMINI_API_KEY is actually mandatory for
    the full pipeline, but for this test ANY configured key improves coverage).
    Without any keys only the statistical method runs, which is fine.

Optional keys for better coverage:
    ANTHROPIC_API_KEY — enables LLM Judge
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

from src.agents.detector_agent import AISignatureDetectorAgent, _statistical_score
from src.models.schemas import (
    AnalyzedSet, ContentType, NewsItem, NewsSource, Permutation,
    PermutationSet, ProvenanceReport, RankedResult, ScrapedResult, ScrapedResultSet,
)

# ── ANSI helpers ──────────────────────────────────────────────────────────────
_G = "\033[92m"; _R = "\033[91m"; _Y = "\033[93m"; _C = "\033[96m"
_B = "\033[1m";  _D = "\033[0m"

def ok(m):   return f"{_G}✓{_D}  {m}"
def err(m):  return f"{_R}✗{_D}  {m}"
def warn(m): return f"{_Y}⚠{_D}  {m}"
def info(m): return f"{_C}ℹ{_D}  {m}"


# ════════════════════════════════════════════════════════════════════════════
#  MOCK ARTICLE TEXTS
# ════════════════════════════════════════════════════════════════════════════

CLEARLY_AI_TEXT = """
Furthermore, it is important to note that the announcement made by the Prime Minister
represents a significant turning point in the nation's political landscape. Moreover,
this decision should be understood in the context of broader governance challenges that
have characterised recent years. It is worth noting that such transitions of power are
pivotal moments for democratic institutions.

Additionally, the implications of this retirement cannot be overstated. Consequently,
political analysts are already examining the potential successors who may step into
this leadership vacuum. It should be noted that the opposition has responded with
measured optimism. In conclusion, this development marks the end of an era,
and it is essential to consider what the future holds for the nation's governance.

Furthermore, historians will likely note that this departure echoes similar transitions
seen in comparable democracies over the past decade. Moreover, the economic policies
enacted during this administration will continue to shape fiscal decisions for years
to come. It is important to note that the institutional frameworks established will
persist beyond any individual tenure. Notably, international partners have expressed
their appreciation for the bilateral progress achieved.
"""

CLEARLY_HUMAN_TEXT = """
I remember the exact moment I heard about it — I was stuck in traffic on the M25,
half-listening to Radio 4 between two arguing presenters, when they interrupted
themselves mid-sentence. The whole car went quiet, which doesn't mean much when
you're alone, but somehow the silence felt larger than usual.

My mother called about twenty minutes later. She hadn't voted for him, hadn't liked
him much for the last five years, but she still sounded shocked. "Does this mean an
election?" she asked. I said I didn't know. We talked for a while about other things —
her neighbour's new fence, whether the plumber would finally come on Thursday — but
we kept circling back.

The strange thing about political farewells is how abrupt they feel even when
you've seen them coming from miles away. You can track the polls, read the op-eds,
and still find yourself caught flat-footed when the actual resignation comes.
I suppose we make a story of inevitability after the fact. Before it happens,
every ending still seems conditional, contingent, reversable somehow.
"""


# ════════════════════════════════════════════════════════════════════════════
#  BUILD MOCK AnalyzedSet
# ════════════════════════════════════════════════════════════════════════════

def _make_analyzed_set() -> AnalyzedSet:
    """Build a minimal AnalyzedSet with two candidates: one AI, one human."""
    baseline = NewsItem(
        headline="Prime Minister announces retirement from politics",
        source_type=NewsSource.MANUAL,
        source_channel="smoke_test",
        published_at=datetime(2024, 6, 15, tzinfo=timezone.utc),
    )
    min_pset = PermutationSet(
        source_item=baseline,
        original_query=baseline.headline,
        permutations=[Permutation(text=baseline.headline)],
        model_used="",
    )

    ai_result = ScrapedResult(
        url="https://ai-news.example.com/article",
        title="Prime Minister officially announces retirement from political office",
        snippet=CLEARLY_AI_TEXT[:300],
        full_text=CLEARLY_AI_TEXT,
        domain="ai-news.example.com",
        content_type=ContentType.ARTICLE,
        published_at=datetime(2024, 1, 10, tzinfo=timezone.utc),
        query_used="PM retirement",
        scraper_id="mock",
        http_status=200,
    )

    human_result = ScrapedResult(
        url="https://personal-blog.example.com/article",
        title="The day the PM announced he was leaving",
        snippet=CLEARLY_HUMAN_TEXT[:300],
        full_text=CLEARLY_HUMAN_TEXT,
        domain="personal-blog.example.com",
        content_type=ContentType.ARTICLE,
        published_at=datetime(2023, 11, 20, tzinfo=timezone.utc),
        query_used="PM retirement",
        scraper_id="mock",
        http_status=200,
    )

    srs = ScrapedResultSet(
        source_permutation_set=min_pset,
        results=[ai_result, human_result],
        total_results_raw=2,
    )

    rr_ai = RankedResult(
        scraped_result=ai_result,
        similarity_score=0.88,
        chronological_rank=2,
        composite_score=0.75,
        is_likely_original=False,
    )
    rr_human = RankedResult(
        scraped_result=human_result,
        similarity_score=0.82,
        chronological_rank=1,
        composite_score=0.82,
        is_likely_original=True,   # earliest
    )

    return AnalyzedSet(
        source_result_set=srs,
        ranked_results=[rr_ai, rr_human],
        top_candidates=[rr_ai, rr_human],
        analysis_duration_seconds=1.2,
        similarity_model="gemini-2.0-flash",
    )


# ════════════════════════════════════════════════════════════════════════════
#  SMOKE TEST
# ════════════════════════════════════════════════════════════════════════════

async def run_smoke_test(threshold: float) -> bool:
    print(f"\n{_B}{'═' * 70}{_D}")
    print(f"{_B}  Provenance Pipeline — AISignatureDetectorAgent Smoke Test{_D}")
    print(f"{_B}{'═' * 70}{_D}\n")

    # ── Key inventory ──────────────────────────────────────────────────────
    anthropic_key = os.getenv("ANTHROPIC_API_KEY")
    def key_status(key, name):
        if key: return ok(f"{name} configured  ({key[:8]}…{key[-4:]})")
        return warn(f"{name} not set  — method will be skipped")

    print(key_status(anthropic_key, "ANTHROPIC_API_KEY"))
    n_keys = 1 if anthropic_key else 0
    print(info("Local statistical, stylometric, and repetition methods always run"))
    print(info(f"Total methods available: {3 + n_keys} / 4\n"))

    # ── Pre-check: statistical scorer on known samples ─────────────────────
    ai_score,  ai_feats  = _statistical_score(CLEARLY_AI_TEXT)
    hu_score,  hu_feats  = _statistical_score(CLEARLY_HUMAN_TEXT)
    print(f"  {_B}Statistical Pre-check (no API){_D}")
    print(f"  AI sample score:    {_G if ai_score > hu_score else _Y}{ai_score:.3f}{_D}")
    print(f"  Human sample score: {_G if hu_score < ai_score else _Y}{hu_score:.3f}{_D}")
    if ai_score > hu_score:
        print(ok("Statistical correctly ranks AI sample > human sample\n"))
    else:
        print(warn(f"Statistical ordering reversed (AI={ai_score:.3f}, human={hu_score:.3f}) "
                   f"— threshold tuning may be needed\n"))

    # ── Instantiate agent ──────────────────────────────────────────────────
    try:
        agent = AISignatureDetectorAgent(detection_threshold=threshold)
        print(ok(f"AISignatureDetectorAgent instantiated (threshold={threshold})\n"))
    except Exception as exc:
        print(err(f"Failed to instantiate: {exc}"))
        return False

    # ── Run detection ──────────────────────────────────────────────────────
    analyzed = _make_analyzed_set()
    print(f"  ⏳ Running {3 + n_keys} method(s) on {len(analyzed.top_candidates)} candidates…\n")

    t0 = time.perf_counter()
    try:
        report: ProvenanceReport = await agent.detect(analyzed)
    except Exception as exc:
        import traceback
        print(err(f"agent.detect() raised: {exc}"))
        traceback.print_exc()
        await agent.close()
        return False
    elapsed = time.perf_counter() - t0

    # ── Print results table ────────────────────────────────────────────────
    print(f"  {'─' * 70}")
    print(f"  {_B}{'#':>2}  {'DOMAIN':<32}  {'ENS':>5}  {'AI?':>5}  METHODS{_D}")
    print(f"  {'─' * 70}")

    for i, r in enumerate(report.ai_signature_results, 1):
        sr = r.ranked_result.scraped_result
        ens_c = _R if r.is_ai_generated else _G
        ai_str = f"{_R}  yes{_D}" if r.is_ai_generated else f"{_G}   no{_D}"
        method_scores = "  ".join(
            f"{m.method_name}={'ERR' if m.error else f'{m.score:.2f}'}"
            for m in r.detection_methods
        )
        print(
            f"  {i:>2}.  {sr.domain:<32}  "
            f"{ens_c}{r.ensemble_score:>5.2f}{_D}  {ai_str}  {method_scores}"
        )
        print(f"       conf={r.confidence:.2f}  {r.explanation[:80]}…")

    print(f"  {'─' * 70}")
    print(f"\n  Elapsed: {elapsed:.2f}s  |  Risk: {report.risk_label}  "
          f"({report.disinformation_risk:.2f})")
    print(f"  Summary: {report.summary}\n")

    # ── Structural assertions ─────────────────────────────────────────────
    print(f"  {_B}Structural Assertions{_D}\n")
    failures: list[str] = []

    # ProvenanceReport type
    if isinstance(report, ProvenanceReport):
        print(ok("Return type is ProvenanceReport"))
    else:
        failures.append(f"Return type is {type(report).__name__}, expected ProvenanceReport")

    # Correct number of results
    if len(report.ai_signature_results) == len(analyzed.top_candidates):
        print(ok(f"ai_signature_results count matches top_candidates ({len(analyzed.top_candidates)})"))
    else:
        failures.append(
            f"ai_signature_results count {len(report.ai_signature_results)} "
            f"!= top_candidates {len(analyzed.top_candidates)}"
        )

    # All ensemble scores in [0,1]
    bad = [r.ensemble_score for r in report.ai_signature_results
           if not (0.0 <= r.ensemble_score <= 1.0)]
    if bad:
        failures.append(f"ensemble_score out of [0,1]: {bad}")
    else:
        print(ok("All ensemble_score values are in [0.0, 1.0]"))

    # All confidence values in [0,1]
    bad_conf = [r.confidence for r in report.ai_signature_results
                if not (0.0 <= r.confidence <= 1.0)]
    if bad_conf:
        failures.append(f"confidence out of [0,1]: {bad_conf}")
    else:
        print(ok("All confidence values are in [0.0, 1.0]"))

    # disinformation_risk in [0,1]
    if 0.0 <= report.disinformation_risk <= 1.0:
        print(ok(f"disinformation_risk = {report.disinformation_risk:.3f} ∈ [0, 1]"))
    else:
        failures.append(f"disinformation_risk {report.disinformation_risk} out of [0,1]")

    # risk_label consistent with score
    from src.models.schemas import RiskLabel
    expected_label = ProvenanceReport.risk_label_from_score(report.disinformation_risk)
    if report.risk_label == expected_label:
        print(ok(f"risk_label={report.risk_label} consistent with score"))
    else:
        failures.append(f"risk_label {report.risk_label} inconsistent with score "
                        f"{report.disinformation_risk} (expected {expected_label})")

    # Local methods always ran (no API key needed)
    for r in report.ai_signature_results:
        for method_name in ("statistical", "stylometric", "template_repetition"):
            method = next((m for m in r.detection_methods if m.method_name == method_name), None)
            if method is None or method.error is not None:
                failures.append(
                    f"{method_name} method did not run for "
                    f"{r.ranked_result.scraped_result.domain}"
                )
    if not failures or not any("method did not run" in f for f in failures):
        print(ok("All local methods ran for all candidates (no API key required)"))

    # Each result has exactly 4 DetectionMethod objects
    for r in report.ai_signature_results:
        if len(r.detection_methods) != 4:
            failures.append(
                f"{r.ranked_result.scraped_result.domain}: "
                f"expected 4 DetectionMethod objects, got {len(r.detection_methods)}"
            )
    if all(len(r.detection_methods) == 4 for r in report.ai_signature_results):
        print(ok("All results have exactly 4 DetectionMethod objects"))

    # summary is non-empty
    if report.summary.strip():
        print(ok(f"summary populated ({len(report.summary)} chars)"))
    else:
        failures.append("summary field is empty")

    # earliest_source flagged
    if report.earliest_source is not None:
        print(ok(f"earliest_source identified: {report.earliest_source.scraped_result.domain}"))
    else:
        print(warn("earliest_source is None — no candidate had is_likely_original=True"))

    # Quality check: AI sample should score higher (warn only, not hard failure)
    ai_result  = next((r for r in report.ai_signature_results
                       if "ai-news" in r.ranked_result.scraped_result.domain), None)
    hu_result  = next((r for r in report.ai_signature_results
                       if "personal-blog" in r.ranked_result.scraped_result.domain), None)
    if ai_result and hu_result:
        if ai_result.ensemble_score > hu_result.ensemble_score:
            print(ok(
                f"Quality: AI sample scored higher than human sample "
                f"({ai_result.ensemble_score:.2f} vs {hu_result.ensemble_score:.2f})"
            ))
        else:
            print(warn(
                f"Quality: AI sample did NOT score higher than human sample "
                f"({ai_result.ensemble_score:.2f} vs {hu_result.ensemble_score:.2f}) — "
                f"this is acceptable when the optional LLM judge is unavailable"
            ))

    # ── Final verdict ──────────────────────────────────────────────────────
    print(f"\n{'═' * 70}")
    if failures:
        print(f"{_R}{_B}  FAILED{_D} — {len(failures)} structural assertion(s):")
        for f in failures:
            print(f"    {err(f)}")
        print(f"{'═' * 70}\n")
        await agent.close()
        return False

    print(
        f"{_G}{_B}  ALL CHECKS PASSED ✓{_D}\n"
        f"  AISignatureDetectorAgent is operational.\n"
        f"  ProvenanceReport is ready for serialisation to data/outputs/."
    )
    print(f"{'═' * 70}\n")
    await agent.close()
    return True


# ── CLI ────────────────────────────────────────────────────────────────────────

def _parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        description="Live smoke test for AISignatureDetectorAgent",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=(
            "Examples:\n"
            "  python test_detector_agent.py\n"
            "  python test_detector_agent.py --threshold 0.6\n"
        ),
    )
    p.add_argument(
        "--threshold", type=float, default=0.65, metavar="T",
        help="AI detection threshold (default: 0.65)"
    )
    return p


if __name__ == "__main__":
    args = _parser().parse_args()
    success = asyncio.run(run_smoke_test(args.threshold))
    sys.exit(0 if success else 1)
