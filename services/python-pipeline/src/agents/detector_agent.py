"""
AISignatureDetectorAgent  —  Stage 5
======================================
Runs four AI-detection methods in parallel on each top candidate from
ChronologicalSimilarityAnalyzer, then combines them into a final
ProvenanceReport.

Pipeline position
─────────────────
  AnalyzerAgent  →  [AnalyzedSet]  →  AISignatureDetectorAgent  →  [ProvenanceReport]

Detection methods & weights
────────────────────────────
  statistical          0.35  — local linguistic analysis, always runs
  stylometric          0.25  — local style/diversity analysis, always runs
  template_repetition  0.20  — local repetition/boilerplate analysis, always runs
  llm_judge            0.20  — Claude Haiku as structured evaluator, optional

Graceful degradation
─────────────────────
  If a method fails or a key is missing, it is excluded
  from the ensemble and its weight is redistributed to successful methods.
  The pipeline always produces a ProvenanceReport — it never crashes.

  Minimum scenario: the three local methods run without paid API credentials.

Ensemble formula
─────────────────
  score      = Σ (w_i / Σ w_successful) × score_i   (successful methods only)
  confidence = min(0.45 + 0.13 × n_successful, 0.97)
  is_ai      = score >= ai_detection_threshold        (default 0.65)
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
import re
import statistics
import time
from typing import Any, Optional

from src.agents.base_agent import BaseAgent
from src.config import PipelineConfig
from src.models.schemas import (
    AISignatureResult,
    AnalyzedSet,
    DetectionMethod,
    ProvenanceReport,
    RankedResult,
    RiskLabel,
)

logger = logging.getLogger("provenance.agent.detector")

# ── Detection method names ────────────────────────────────────────────────────
_STATISTICAL = "statistical"
_STYLOMETRIC = "stylometric"
_TEMPLATE_REPETITION = "template_repetition"
_LLM_JUDGE  = "llm_judge"

# ── Ensemble weights (must sum to 1.0) ───────────────────────────────────────
_METHOD_WEIGHTS: dict[str, float] = {
    _STATISTICAL: 0.35,
    _STYLOMETRIC: 0.25,
    _TEMPLATE_REPETITION: 0.20,
    _LLM_JUDGE:   0.20,
}

# ── LLM Judge model ───────────────────────────────────────────────────────────
_JUDGE_MODEL     = "claude-haiku-4-5-20251001"  # cost-efficient for high volume
_JUDGE_MAX_TOKENS = 512
_JUDGE_TEMPERATURE = 0.1                         # near-zero for consistency

# ── Text limits ───────────────────────────────────────────────────────────────
_MAX_TEXT_CHARS  = 5_000   # keep detector prompts and local analysis bounded
_MIN_TEXT_CHARS  = 50      # skip detection for very short snippets

# ── Transition/hedging phrases flagged by statistical analysis ────────────────
_TRANSITION_PHRASES = [
    "furthermore", "moreover", "additionally", "consequently", "therefore",
    "in conclusion", "in summary", "to summarize", "it is important to note",
    "it is worth noting", "it should be noted", "significantly", "notably",
    "in addition", "as a result", "thus", "hence", "accordingly",
    "this highlights", "this demonstrates", "this underscores",
]
_HEDGING_PHRASES = [
    "it is important to note", "it should be noted", "it is worth noting",
    "it is crucial to", "it is essential to", "one must consider",
    "we must acknowledge", "it bears mentioning", "it cannot be overstated",
    "it is widely recognized", "needless to say", "it goes without saying",
]
_TEMPLATE_PHRASES = [
    "it is important to note", "it is worth noting", "this article explores",
    "in today's rapidly evolving", "this underscores the importance",
    "a complex and multifaceted", "only time will tell", "it remains to be seen",
    "the broader implications", "stakeholders must consider",
]
_WORD_RE = re.compile(r"\b[\w']+\b")


# ══════════════════════════════════════════════════════════════════════════════
#  AISignatureDetectorAgent
# ══════════════════════════════════════════════════════════════════════════════

class AISignatureDetectorAgent(BaseAgent):
    """
    Stage 5: parallel multi-method AI-text detection → ProvenanceReport.

    Direct use (no queues):
        report = await agent.detect(analyzed_set)

    Queue-driven use (via PipelineRunner):
        report = await agent.process(analyzed_set)

    Args:
        anthropic_api_key:  Overrides ANTHROPIC_API_KEY env var.
        detection_threshold: Overrides PipelineConfig.ai_detection_threshold.
    """

    def __init__(
        self,
        name: str = "detector",
        anthropic_api_key: Optional[str] = None,
        detection_threshold: Optional[float] = None,
        input_queue: Optional[asyncio.Queue[Any]] = None,
        output_queue: Optional[asyncio.Queue[Any]] = None,
        config: Optional[PipelineConfig] = None,
    ) -> None:
        super().__init__(
            name=name,
            input_queue=input_queue,
            output_queue=output_queue,
            config=config,
        )

        # ── API keys ──────────────────────────────────────────────────────────
        _anthropic_key: Optional[str] = (
            anthropic_api_key
            or os.getenv("ANTHROPIC_API_KEY")
            or self.config.anthropic_api_key
            or None
        )

        # ── Decision threshold ────────────────────────────────────────────────
        self.detection_threshold: float = (
            detection_threshold
            if detection_threshold is not None
            else self.config.ai_detection_threshold
        )

        # ── Anthropic client (optional) ───────────────────────────────────────
        self._anthropic_client: Any = None
        if _anthropic_key:
            try:
                import anthropic  # noqa: PLC0415
                self._anthropic_client = anthropic.Anthropic(api_key=_anthropic_key)
                logger.info("Anthropic client initialised for LLM Judge.")
            except ImportError:
                logger.warning(
                    "anthropic package not installed — LLM Judge disabled. "
                    "Run: pip install anthropic"
                )
        else:
            logger.info("No ANTHROPIC_API_KEY — LLM Judge will be skipped.")

        logger.info(
            "AISignatureDetectorAgent ready — "
            "local_methods=3  llm_judge=%s  threshold=%.2f",
            bool(self._anthropic_client),
            self.detection_threshold,
        )

    # ── Lifecycle ──────────────────────────────────────────────────────────────

    async def close(self) -> None:
        """Lifecycle hook kept for PipelineRunner compatibility."""
        return None

    # ── BaseAgent.process (queue path) ────────────────────────────────────────

    async def process(self, item: AnalyzedSet) -> ProvenanceReport:  # type: ignore[override]
        """Queue-driven entry point called by PipelineRunner."""
        return await self.detect(item)

    # ── Primary public method ─────────────────────────────────────────────────

    async def detect(self, analyzed: AnalyzedSet) -> ProvenanceReport:
        """
        Run all detection methods on every top_candidate and build the report.

        Args:
            analyzed: AnalyzedSet from Stage 4 (ChronologicalSimilarityAnalyzer).

        Returns:
            ProvenanceReport — always returned, even if all external APIs fail.
        """
        t0 = time.perf_counter()
        source_item = analyzed.source_result_set.source_permutation_set.source_item

        if not analyzed.top_candidates:
            logger.warning("AISignatureDetectorAgent received an empty AnalyzedSet.")
            return self._empty_report(source_item, t0)

        # ── Process all top candidates in parallel ────────────────────────────
        tasks = [self._analyze_candidate(rr) for rr in analyzed.top_candidates]
        ai_results: list[AISignatureResult] = await asyncio.gather(*tasks)

        # ── Compute pipeline-level risk ───────────────────────────────────────
        max_score    = max((r.ensemble_score for r in ai_results), default=0.0)
        risk_label   = ProvenanceReport.risk_label_from_score(max_score)
        earliest     = next(
            (rr for rr in analyzed.ranked_results if rr.is_likely_original), None
        )
        summary      = _build_summary(source_item.headline, ai_results, max_score, risk_label)
        elapsed      = time.perf_counter() - t0

        logger.info(
            "Detection complete: %d candidates, max_score=%.2f, risk=%s, elapsed=%.2fs",
            len(ai_results), max_score, risk_label, elapsed,
        )
        return ProvenanceReport(
            source_item             = source_item,
            ai_signature_results    = ai_results,
            earliest_source         = earliest,
            disinformation_risk     = round(max_score, 4),
            risk_label              = risk_label,
            total_duration_seconds  = elapsed,
            summary                 = summary,
        )

    # ── Candidate-level analysis ──────────────────────────────────────────────

    async def _analyze_candidate(self, ranked: RankedResult) -> AISignatureResult:
        """Run all 4 detection methods in parallel for one candidate."""
        text = _extract_text(ranked)
        if len(text) < _MIN_TEXT_CHARS:
            logger.debug(
                "Candidate '%s…' too short (%d chars) — using neutral scores.",
                text[:40], len(text),
            )

        # All four methods run concurrently; three are free/local.
        stat_task       = asyncio.to_thread(_run_statistical, text)
        stylometric_task = asyncio.to_thread(_run_stylometric, text)
        repetition_task = asyncio.to_thread(_run_template_repetition, text)
        llm_judge_task  = self._run_llm_judge(text)

        methods: list[DetectionMethod] = list(
            await asyncio.gather(
                stat_task,
                stylometric_task,
                repetition_task,
                llm_judge_task,
            )
        )

        ensemble_score, is_ai, confidence, explanation = _compute_ensemble(
            methods, self.detection_threshold
        )

        return AISignatureResult(
            ranked_result    = ranked,
            detection_methods= methods,
            ensemble_score   = round(ensemble_score, 4),
            is_ai_generated  = is_ai,
            confidence       = round(confidence, 4),
            explanation      = explanation,
        )

    # ── Method 4: LLM Judge ───────────────────────────────────────────────────

    async def _run_llm_judge(self, text: str) -> DetectionMethod:
        """
        Use Claude Haiku as a structured AI-text evaluator.
        Scores 5 linguistic dimensions and returns a weighted overall score.
        """
        if not self._anthropic_client:
            return DetectionMethod(
                method_name=_LLM_JUDGE,
                error="ANTHROPIC_API_KEY not configured",
            )
        truncated = text[:_MAX_TEXT_CHARS]
        loop = asyncio.get_event_loop()
        try:
            result = await loop.run_in_executor(
                None, self._call_llm_judge_sync, truncated
            )
            return result
        except Exception as exc:
            logger.warning("LLM Judge call failed: %s", exc)
            return DetectionMethod(method_name=_LLM_JUDGE, error=str(exc))

    def _call_llm_judge_sync(self, text: str) -> DetectionMethod:
        """Synchronous Anthropic call (run inside ThreadPoolExecutor)."""
        user_prompt = (
            "Analyze the following article text and evaluate whether it appears "
            "to be AI-generated.\n\n"
            f'TEXT:\n"""\n{text}\n"""\n\n'
            "Score these 5 dimensions from 0.0 (definitely human) to 1.0 (definitely AI):\n"
            "  1. lexical_uniformity     — repetitive, formulaic vocabulary\n"
            "  2. structural_regularity  — predictable sentence/paragraph patterns\n"
            "  3. hedging_density        — excessive qualifiers ('it is important to note...')\n"
            "  4. factual_confidence     — unnaturally flat, assertive tone\n"
            "  5. originality            — 0=highly original/human, 1=boilerplate/generic\n\n"
            "Return ONLY a JSON object with this exact structure:\n"
            '{\n'
            '  "lexical_uniformity": <float>,\n'
            '  "structural_regularity": <float>,\n'
            '  "hedging_density": <float>,\n'
            '  "factual_confidence": <float>,\n'
            '  "originality": <float>,\n'
            '  "overall_score": <float 0-1, your weighted judgment>,\n'
            '  "reasoning": "<one sentence>"\n'
            "}"
        )
        response = self._anthropic_client.messages.create(
            model=_JUDGE_MODEL,
            max_tokens=_JUDGE_MAX_TOKENS,
            temperature=_JUDGE_TEMPERATURE,
            system=(
                "You are an expert AI-text detection analyst. "
                "Respond ONLY with valid JSON — no markdown fences, no preamble."
            ),
            messages=[{"role": "user", "content": user_prompt}],
        )
        content = response.content[0].text if response.content else "{}"
        # Strip possible markdown fences
        content = re.sub(r"^```(?:json)?\s*", "", content.strip())
        content = re.sub(r"\s*```$", "", content)
        data: dict[str, Any] = json.loads(content)

        overall = float(data.get("overall_score", 0.5))
        overall = max(0.0, min(1.0, overall))
        label   = "AI" if overall >= 0.65 else "HUMAN" if overall < 0.35 else "UNCERTAIN"

        return DetectionMethod(
            method_name=_LLM_JUDGE,
            score=overall,
            label=label,
            raw_response=data,
        )

    # ── Utility ───────────────────────────────────────────────────────────────

    def _empty_report(self, source_item: Any, t0: float) -> ProvenanceReport:
        return ProvenanceReport(
            source_item            = source_item,
            ai_signature_results   = [],
            disinformation_risk    = 0.0,
            risk_label             = RiskLabel.LOW,
            total_duration_seconds = time.perf_counter() - t0,
            summary                = "No candidates were available for AI-signature analysis.",
        )


# ══════════════════════════════════════════════════════════════════════════════
#  Method 3: Statistical / Linguistic Analysis  (module-level, no side effects)
# ══════════════════════════════════════════════════════════════════════════════

def _run_statistical(text: str) -> DetectionMethod:
    """
    Pure-Python linguistic fingerprint analysis.  No external calls.

    Five features (each 0=human, 1=AI):
      1. sentence_uniformity   — low coefficient-of-variation in sentence lengths
      2. burstiness            — negative burstiness B=(σ-μ)/(σ+μ) → AI
      3. transition_density    — >3 AI-typical phrases per 100 words → AI
      4. hedging_density       — >2 hedging phrases per 100 words → AI
      5. paragraph_homogeneity — uniform paragraph lengths → AI

    Feature weights: burstiness (30%), transition_density (25%),
                     sentence_uniformity (15%), hedging_density (15%),
                     paragraph_homogeneity (15%).
    """
    if not text or len(text.strip()) < _MIN_TEXT_CHARS:
        return DetectionMethod(
            method_name=_STATISTICAL,
            score=0.5,
            label="UNCERTAIN",
            raw_response={"note": "text too short for reliable analysis"},
        )

    score, features = _statistical_score(text)
    label = "AI" if score >= 0.65 else "HUMAN" if score < 0.35 else "UNCERTAIN"

    return DetectionMethod(
        method_name=_STATISTICAL,
        score=round(score, 3),
        label=label,
        raw_response={"features": features, "aggregate_score": round(score, 3)},
    )


def _run_stylometric(text: str) -> DetectionMethod:
    """Local stylometry analysis focused on diversity and structural regularity."""
    if not text or len(text.strip()) < _MIN_TEXT_CHARS:
        return DetectionMethod(
            method_name=_STYLOMETRIC,
            score=0.5,
            label="UNCERTAIN",
            raw_response={"note": "text too short for reliable analysis"},
        )

    score, features = _stylometric_score(text)
    label = "AI" if score >= 0.65 else "HUMAN" if score < 0.35 else "UNCERTAIN"
    return DetectionMethod(
        method_name=_STYLOMETRIC,
        score=round(score, 3),
        label=label,
        raw_response={"features": features, "aggregate_score": round(score, 3)},
    )


def _run_template_repetition(text: str) -> DetectionMethod:
    """Local repetition analysis for boilerplate phrases and repeated n-gram patterns."""
    if not text or len(text.strip()) < _MIN_TEXT_CHARS:
        return DetectionMethod(
            method_name=_TEMPLATE_REPETITION,
            score=0.5,
            label="UNCERTAIN",
            raw_response={"note": "text too short for reliable analysis"},
        )

    score, features = _template_repetition_score(text)
    label = "AI" if score >= 0.65 else "HUMAN" if score < 0.35 else "UNCERTAIN"
    return DetectionMethod(
        method_name=_TEMPLATE_REPETITION,
        score=round(score, 3),
        label=label,
        raw_response={"features": features, "aggregate_score": round(score, 3)},
    )


def _statistical_score(text: str) -> tuple[float, dict[str, float]]:
    """Return (ai_score 0-1, features_dict). Exposed for unit testing."""
    words  = text.lower().split()
    n_words = max(len(words), 1)

    # ── Feature 1: Sentence length uniformity ─────────────────────────────────
    sentences = [s.strip() for s in re.split(r"[.!?]+", text) if s.strip()]
    sen_lens  = [len(s.split()) for s in sentences if s.split()]

    if len(sen_lens) >= 3:
        mean_sl = statistics.mean(sen_lens)
        std_sl  = statistics.stdev(sen_lens)
        cv_sl   = std_sl / max(mean_sl, 1.0)
        # Low CV → uniform → AI; scale so CV=0→1.0(AI), CV=0.5→0.0(human)
        uniformity = max(0.0, min(1.0, 1.0 - cv_sl * 2.0))
    else:
        uniformity = 0.5

    # ── Feature 2: Burstiness ─────────────────────────────────────────────────
    if len(sen_lens) >= 3:
        mean_b = statistics.mean(sen_lens)
        std_b  = statistics.stdev(sen_lens)
        denom  = std_b + mean_b
        burst  = (std_b - mean_b) / denom if denom else 0.0
        # B in [-1, 1]: negative → AI (uniform), positive → human (bursty)
        # Map: B=-1 → ai=1.0, B=0 → ai=0.5, B=1 → ai=0.0
        burstiness_ai = max(0.0, min(1.0, 0.5 - burst / 2.0))
    else:
        burstiness_ai = 0.5

    # ── Feature 3: Transition word density ───────────────────────────────────
    n_transition = sum(text.lower().count(p) for p in _TRANSITION_PHRASES)
    trans_density = n_transition / (n_words / 100.0)
    # > 3 per 100 words → AI; 0 = human; scale 0–6 → 0–1
    transition_ai = max(0.0, min(1.0, trans_density / 6.0))

    # ── Feature 4: Hedging phrase density ─────────────────────────────────────
    n_hedge = sum(text.lower().count(p) for p in _HEDGING_PHRASES)
    hedge_density = n_hedge / (n_words / 100.0)
    # > 2 per 100 words → AI; scale 0–4 → 0–1
    hedging_ai = max(0.0, min(1.0, hedge_density / 4.0))

    # ── Feature 5: Paragraph length homogeneity ──────────────────────────────
    paragraphs = [p.strip() for p in re.split(r"\n{2,}", text) if p.strip()]
    if len(paragraphs) >= 3:
        para_lens = [len(p.split()) for p in paragraphs]
        mean_pl = statistics.mean(para_lens)
        std_pl  = statistics.stdev(para_lens)
        cv_pl   = std_pl / max(mean_pl, 1.0)
        # Low CV → uniform → AI; scale so CV=0→1.0, CV≥0.7→0.0
        para_ai = max(0.0, min(1.0, 1.0 - cv_pl / 0.7))
    else:
        para_ai = 0.5

    features = {
        "sentence_uniformity":   round(uniformity, 3),
        "burstiness_ai":         round(burstiness_ai, 3),
        "transition_density_ai": round(transition_ai, 3),
        "hedging_density_ai":    round(hedging_ai, 3),
        "paragraph_homogeneity": round(para_ai, 3),
    }

    weights = {
        "sentence_uniformity":   0.15,
        "burstiness_ai":         0.30,
        "transition_density_ai": 0.25,
        "hedging_density_ai":    0.15,
        "paragraph_homogeneity": 0.15,
    }

    ai_score = sum(features[k] * weights[k] for k in features)
    return round(ai_score, 3), features


def _stylometric_score(text: str) -> tuple[float, dict[str, float]]:
    """Return a free local stylometry score where 1.0 means more AI-like."""
    words = _words(text)
    n_words = max(len(words), 1)
    sentences = _sentences(text)
    sentence_lengths = [len(_words(sentence)) for sentence in sentences if _words(sentence)]

    lexical_diversity = len(set(words)) / n_words
    lexical_uniformity = _clamp01((0.72 - lexical_diversity) / 0.42)

    word_lengths = [len(word) for word in words]
    word_cv = _coefficient_of_variation(word_lengths)
    word_length_uniformity = _clamp01(1.0 - word_cv / 0.55)

    sentence_cv = _coefficient_of_variation(sentence_lengths)
    sentence_regularization = _clamp01(1.0 - sentence_cv / 0.65)

    openings = [_sentence_opening(sentence) for sentence in sentences if _sentence_opening(sentence)]
    opening_diversity = len(set(openings)) / max(len(openings), 1)
    opening_uniformity = _clamp01((0.75 - opening_diversity) / 0.55)

    punctuation_counts = [
        sum(1 for char in sentence if char in ",;:()[]")
        for sentence in sentences
    ]
    punctuation_flatness = _clamp01(1.0 - _coefficient_of_variation(punctuation_counts) / 1.2)

    features = {
        "lexical_uniformity": round(lexical_uniformity, 3),
        "word_length_uniformity": round(word_length_uniformity, 3),
        "sentence_regularization": round(sentence_regularization, 3),
        "opening_uniformity": round(opening_uniformity, 3),
        "punctuation_flatness": round(punctuation_flatness, 3),
    }
    weights = {
        "lexical_uniformity": 0.30,
        "word_length_uniformity": 0.15,
        "sentence_regularization": 0.30,
        "opening_uniformity": 0.15,
        "punctuation_flatness": 0.10,
    }
    ai_score = sum(features[key] * weights[key] for key in features)
    return round(ai_score, 3), features


def _template_repetition_score(text: str) -> tuple[float, dict[str, float]]:
    """Return a free local score for repeated templates and boilerplate phrasing."""
    words = _words(text)
    sentences = _sentences(text)
    n_words = max(len(words), 1)
    lowered = text.lower()

    repeated_trigrams = _repeated_ngram_ratio(words, 3)
    repeated_fourgrams = _repeated_ngram_ratio(words, 4)

    phrase_hits = sum(lowered.count(phrase) for phrase in _TEMPLATE_PHRASES)
    phrase_density = phrase_hits / (n_words / 100.0)
    boilerplate_density = _clamp01(phrase_density / 4.0)

    openings = [_sentence_opening(sentence, size=3) for sentence in sentences]
    openings = [opening for opening in openings if opening]
    repeated_openings = 1.0 - (len(set(openings)) / max(len(openings), 1))

    paragraph_lens = [len(_words(paragraph)) for paragraph in _paragraphs(text)]
    paragraph_cv = _coefficient_of_variation(paragraph_lens)
    paragraph_template = _clamp01(1.0 - paragraph_cv / 0.7)

    features = {
        "repeated_trigrams": round(_clamp01(repeated_trigrams / 0.08), 3),
        "repeated_fourgrams": round(_clamp01(repeated_fourgrams / 0.05), 3),
        "boilerplate_density": round(boilerplate_density, 3),
        "repeated_sentence_openings": round(_clamp01(repeated_openings / 0.5), 3),
        "paragraph_template": round(paragraph_template, 3),
    }
    weights = {
        "repeated_trigrams": 0.25,
        "repeated_fourgrams": 0.20,
        "boilerplate_density": 0.25,
        "repeated_sentence_openings": 0.15,
        "paragraph_template": 0.15,
    }
    ai_score = sum(features[key] * weights[key] for key in features)
    return round(ai_score, 3), features


# ══════════════════════════════════════════════════════════════════════════════
#  Ensemble computation  (pure function — no side effects)
# ══════════════════════════════════════════════════════════════════════════════

def _compute_ensemble(
    methods: list[DetectionMethod],
    threshold: float,
) -> tuple[float, bool, float, str]:
    """
    Compute the weighted ensemble score from all detection methods.

    Returns:
        (ensemble_score, is_ai_generated, confidence, explanation)
    """
    successful = [m for m in methods if m.error is None and m.score is not None]

    if not successful:
        return 0.5, False, 0.0, "No detection methods ran successfully — verdict unavailable."

    # Renormalise weights to the successful subset
    total_w = sum(_METHOD_WEIGHTS[m.method_name] for m in successful)
    if total_w == 0.0:
        return 0.5, False, 0.0, "All method weights summed to zero."

    score = sum(
        (_METHOD_WEIGHTS[m.method_name] / total_w) * m.score  # type: ignore[operator]
        for m in successful
    )
    score = max(0.0, min(1.0, score))

    # Confidence scales with independent signals without over-claiming local heuristics.
    confidence = min(0.45 + 0.13 * len(successful), 0.97)

    is_ai = score >= threshold

    method_parts = ", ".join(
        f"{m.method_name}={m.score:.2f}" for m in successful
    )
    skipped = [m.method_name for m in methods if m.error is not None]
    skip_note = f"  (skipped: {', '.join(skipped)})" if skipped else ""
    verdict    = "AI-generated" if is_ai else "likely human-written"

    explanation = (
        f"Verdict: {verdict}. "
        f"Ensemble score {score:.2f} from {len(successful)} method(s) "
        f"[{method_parts}]{skip_note}. "
        f"Threshold={threshold:.2f}, confidence={confidence:.2f}."
    )

    return round(score, 4), is_ai, round(confidence, 4), explanation


# ══════════════════════════════════════════════════════════════════════════════
#  Helpers
# ══════════════════════════════════════════════════════════════════════════════

def _clamp01(value: float) -> float:
    return max(0.0, min(1.0, value))


def _words(text: str) -> list[str]:
    return [match.group(0).lower() for match in _WORD_RE.finditer(text)]


def _sentences(text: str) -> list[str]:
    return [sentence.strip() for sentence in re.split(r"[.!?]+", text) if sentence.strip()]


def _paragraphs(text: str) -> list[str]:
    return [paragraph.strip() for paragraph in re.split(r"\n{2,}", text) if paragraph.strip()]


def _coefficient_of_variation(values: list[int]) -> float:
    if len(values) < 2:
        return 0.5
    mean = statistics.mean(values)
    if mean <= 0.0:
        return 0.5
    return statistics.stdev(values) / mean


def _sentence_opening(sentence: str, size: int = 2) -> str:
    words = _words(sentence)
    return " ".join(words[:size])


def _repeated_ngram_ratio(words: list[str], size: int) -> float:
    if len(words) < size * 2:
        return 0.0
    counts: dict[tuple[str, ...], int] = {}
    for index in range(0, len(words) - size + 1):
        ngram = tuple(words[index:index + size])
        counts[ngram] = counts.get(ngram, 0) + 1
    repeated = sum(count - 1 for count in counts.values() if count > 1)
    return repeated / max(len(words) - size + 1, 1)


def _extract_text(ranked: RankedResult) -> str:
    """
    Best available text for detection from a RankedResult.
    Preference order: full_text → snippet → title.
    Truncated to _MAX_TEXT_CHARS.
    """
    sr   = ranked.scraped_result
    text = sr.full_text or sr.snippet or sr.title or ""
    return text[:_MAX_TEXT_CHARS].strip()


def _build_summary(
    headline: str,
    results: list[AISignatureResult],
    max_score: float,
    risk_label: RiskLabel,
) -> str:
    """One-paragraph human-readable narrative for the ProvenanceReport."""
    n = len(results)
    n_ai = sum(1 for r in results if r.is_ai_generated)
    risk_str = risk_label.value

    if n == 0:
        return "No candidates were analysed for AI-signature content."

    earliest = next(
        (r for r in results if r.ranked_result.is_likely_original), None
    )
    earliest_note = ""
    if earliest:
        domain = earliest.ranked_result.scraped_result.domain
        dt     = earliest.ranked_result.scraped_result.published_at
        date_s = dt.strftime("%Y-%m-%d") if dt else "unknown date"
        earliest_note = (
            f" The earliest source identified is {domain} ({date_s})."
        )

    return (
        f'Provenance analysis of "{headline[:80]}": '
        f"{n} candidate article(s) analysed. "
        f"{n_ai} of {n} appear AI-generated (max ensemble score: {max_score:.2f}). "
        f"Disinformation risk: {risk_str}.{earliest_note}"
    )
