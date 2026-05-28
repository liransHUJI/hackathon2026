"""
SemanticAgent
=============
Generates semantic permutations of a news headline using Google Gemini.

Why structured output matters
──────────────────────────────
A conversational LLM response like "Here are some paraphrases: 1. ..." would
crash BroadScraperAgent, which expects a typed list[Permutation].  We use
Gemini's `response_schema` parameter to force the model to emit valid JSON
that is automatically parsed into our Pydantic models — no regex, no
string-splitting, no silent data loss.

Flow
────
  NewsItem  →  SemanticAgent.process()  →  PermutationSet
                    │
                    ├─ _build_prompt(item)
                    ├─ _call_gemini_sync(prompt)   ← runs in thread executor
                    │      ├─ response_schema = list[Permutation]
                    │      └─ returns response.parsed  (already typed)
                    └─ _parse_response(response)   ← validate + fallback

Graceful degradation
─────────────────────
If the API call fails entirely (network error, quota exhaustion, invalid key),
the agent returns a PermutationSet containing only the original headline as a
single Permutation with confidence=0.5.  The pipeline continues with reduced
coverage rather than crashing.

Configuration
─────────────
  GEMINI_API_KEY   env var (required)
  GEMINI_MODEL     env var (default: gemini-2.0-flash)
  PERMUTATION_COUNT  env var via PipelineConfig (default: 10 for tests)
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
from typing import Any, Optional

from pydantic import TypeAdapter, ValidationError

from src.agents.base_agent import BaseAgent
from src.config import PipelineConfig
from src.models.schemas import NewsItem, Permutation, PermutationSet

logger = logging.getLogger("provenance.agent.semantic")

# ── Constants ─────────────────────────────────────────────────────────────────

DEFAULT_MODEL = "gemini-2.0-flash"
DEFAULT_TEMPERATURE = 1.0   # Higher temp → more lexical diversity in permutations
DEFAULT_COUNT = 10           # Overridden by PipelineConfig.permutation_count

# Gemini has a ~2M token context but we stay well under for speed/cost.
_MAX_OUTPUT_TOKENS = 4096

# ── System prompt ─────────────────────────────────────────────────────────────

_SYSTEM_PROMPT = """\
You are an expert computational linguist specialising in paraphrase generation \
and semantic equivalence for investigative journalism tools.

TASK
────
Given a news headline, generate a precise number of semantic permutations.
Each permutation must satisfy ALL of the following:

  1. PRESERVE MEANING — identical facts, identical people/entities, identical event.
     Adding or removing any factual detail is a critical failure.

  2. DIFFERENT SURFACE FORM — use different vocabulary, word order, or sentence
     structure from the original and from every other permutation in the list.

  3. NATURAL LANGUAGE — reads like an authentic news headline or article sentence.

  4. SEARCH-READY — effective as a standalone web search query that would surface
     articles about the same underlying story.

TRANSFORMATION STRATEGIES (apply varied strategies across the output list)
──────────────────────────────────────────────────────────────────────────
  synonym            Replace words with precise synonyms
                     "retiring" → "stepping down", "quitting", "resigning"

  entity_generalization  Replace specific role titles with generic equivalents
                     "Prime Minister" → "head of government", "national leader"
                     "Chancellor" → "finance minister", "economy chief"

  passive_voice      Convert active constructions to passive
                     "X announces Y" → "Y is announced by X"

  active_voice       Convert passive constructions to active

  temporal_paraphrase  Rephrase time references naturally
                     "this week" → "in recent days", "on Tuesday"

  structural_reframe   Change sentence structure, not vocabulary
                     "X confirms Y" → "Y confirmed as X's decision"

  journalistic_rewrite  Reframe as reported speech or third-person news prose
                     "PM to retire" → "Reports confirm the Premier plans to retire"

  paraphrase         Free-form rewording that preserves all facts

STRICT PROHIBITIONS
────────────────────
  ✗  Do NOT reproduce the original headline verbatim (even with minor punctuation changes)
  ✗  Do NOT add facts not present in the original (dates, quotes, names, etc.)
  ✗  Do NOT omit key facts (who, what) from the original
  ✗  Do NOT produce questions or hypotheticals
  ✗  Do NOT include meta-commentary ("Here is a paraphrase...")

Set confidence = 1.0 if the permutation fully preserves meaning.
Set confidence < 0.9 only if the permutation is somewhat more general/vague.
"""

# Type adapter for validating the parsed response
_PERMUTATION_LIST_ADAPTER: TypeAdapter[list[Permutation]] = TypeAdapter(
    list[Permutation]
)


# ══════════════════════════════════════════════════════════════════════════════
#  SemanticAgent
# ══════════════════════════════════════════════════════════════════════════════

class SemanticAgent(BaseAgent):
    """
    Generates N semantic permutations of a NewsItem headline via Gemini.

    Inherits from BaseAgent so it can be plugged into the asyncio queue
    pipeline or called directly (agent.process(news_item)) in tests.

    Args:
        name:               Agent identifier for logging.
        api_key:            Gemini API key.  Falls back to GEMINI_API_KEY env var.
        model:              Gemini model string.  Falls back to GEMINI_MODEL env var.
        permutation_count:  How many permutations to generate.  Falls back to
                            PipelineConfig.permutation_count (default 10 for tests).
        input_queue:        asyncio.Queue — set by PipelineRunner; None for direct use.
        output_queue:       asyncio.Queue — set by PipelineRunner; None for direct use.
        config:             PipelineConfig instance; created from .env if not provided.
    """

    def __init__(
        self,
        name: str = "semantic",
        api_key: Optional[str] = None,
        model: Optional[str] = None,
        permutation_count: Optional[int] = None,
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

        # Resolve API key: explicit arg → env var → error
        _key = (
            api_key
            or os.getenv("GEMINI_API_KEY")
            or os.getenv("GOOGLE_API_KEY")
        )
        if not _key:
            raise ValueError(
                "No Gemini API key found.\n"
                "  • Add GEMINI_API_KEY=<your-key> to .env  (get one free at aistudio.google.com)\n"
                "  • Or pass api_key= directly to SemanticAgent()"
            )

        self.model_name: str = (
            model
            or os.getenv("GEMINI_MODEL", DEFAULT_MODEL)
        )
        self.permutation_count: int = (
            permutation_count
            or self.config.permutation_count
            or DEFAULT_COUNT
        )

        # Defer SDK import so the module loads even when google-genai is absent
        # (allows other parts of the codebase to import without the package).
        try:
            from google import genai  # noqa: PLC0415
            from google.genai import types  # noqa: PLC0415

            self._client = genai.Client(api_key=_key)
            self._types = types
            self._sdk_available = True
        except ImportError as exc:
            raise ImportError(
                "google-genai package is required for SemanticAgent.\n"
                "Run: pip install google-genai>=1.0.0"
            ) from exc

        # Anthropic fallback — used when Gemini quota is exhausted or unavailable
        self._anthropic_key: Optional[str] = (
            os.getenv("ANTHROPIC_API_KEY")
        )

        logger.info(
            "SemanticAgent ready — model=%s  count=%d",
            self.model_name,
            self.permutation_count,
        )

    # ── BaseAgent implementation ──────────────────────────────────────────────

    async def process(self, item: NewsItem) -> PermutationSet:  # type: ignore[override]
        """
        Generate permutations for a single NewsItem.

        Callable directly (no queue needed):
            pset = await agent.process(news_item)

        Or automatically via the run() queue loop once wired by PipelineRunner.
        """
        permutations = await self._generate(item)
        return PermutationSet(
            source_item=item,
            original_query=item.headline,
            permutations=permutations,
            model_used=self.model_name,
        )

    # ── Generation pipeline ───────────────────────────────────────────────────

    async def _generate(self, item: NewsItem) -> list[Permutation]:
        """
        Async entry point: build prompt → call Gemini (in executor) → parse.
        Returns at least one Permutation (fallback) even on total failure.
        """
        prompt = _build_prompt(item.headline, self.permutation_count)

        loop = asyncio.get_event_loop()
        try:
            response = await loop.run_in_executor(
                None,           # default ThreadPoolExecutor
                self._call_gemini_sync,
                prompt,
            )
        except Exception as exc:
            logger.error(
                "Gemini API call failed for '%s…': %s",
                item.headline[:60],
                exc,
                exc_info=True,
            )
            if self._anthropic_key:
                logger.info("Falling back to Anthropic claude-haiku for permutation generation.")
                try:
                    return await self._generate_with_anthropic(item)
                except Exception as anthropic_exc:
                    logger.error(
                        "Anthropic fallback also failed: %s", anthropic_exc, exc_info=True
                    )
            logger.warning("Falling back to single original-headline permutation.")
            return [_fallback_permutation(item.headline)]

        return self._parse_response(response, item.headline)

    async def _generate_with_anthropic(self, item: NewsItem) -> list[Permutation]:
        """Generate permutations via Anthropic claude-haiku using tool use for structured output."""
        import anthropic  # noqa: PLC0415

        client = anthropic.AsyncAnthropic(api_key=self._anthropic_key)
        prompt = _build_prompt(item.headline, self.permutation_count)

        response = await client.messages.create(
            model="claude-haiku-4-5-20251001",
            max_tokens=4096,
            system=_SYSTEM_PROMPT,
            tools=[
                {
                    "name": "submit_permutations",
                    "description": "Submit the generated semantic permutations as structured data.",
                    "input_schema": {
                        "type": "object",
                        "properties": {
                            "permutations": {
                                "type": "array",
                                "items": {
                                    "type": "object",
                                    "properties": {
                                        "text": {"type": "string"},
                                        "strategy": {"type": "string"},
                                        "confidence": {"type": "number"},
                                    },
                                    "required": ["text", "strategy", "confidence"],
                                },
                            }
                        },
                        "required": ["permutations"],
                    },
                }
            ],
            tool_choice={"type": "tool", "name": "submit_permutations"},
            messages=[{"role": "user", "content": prompt}],
        )

        for block in response.content:
            if block.type == "tool_use" and block.name == "submit_permutations":
                raw = block.input.get("permutations", [])
                permutations = _PERMUTATION_LIST_ADAPTER.validate_python(raw)
                logger.info(
                    "Anthropic fallback generated %d permutations for '%s…'",
                    len(permutations),
                    item.headline[:60],
                )
                return permutations if permutations else [_fallback_permutation(item.headline)]

        logger.warning("Anthropic response contained no tool_use block; using fallback.")
        return [_fallback_permutation(item.headline)]

    def _call_gemini_sync(self, prompt: str) -> Any:
        """
        Synchronous Gemini API call executed inside a ThreadPoolExecutor.

        Uses response_schema=list[Permutation] to instruct Gemini to return
        a JSON array whose elements match the Permutation Pydantic model.
        The SDK converts the Pydantic model to a JSON Schema automatically.
        """
        config = self._types.GenerateContentConfig(
            system_instruction=_SYSTEM_PROMPT,
            response_mime_type="application/json",
            response_schema=list[Permutation],
            temperature=DEFAULT_TEMPERATURE,
            max_output_tokens=_MAX_OUTPUT_TOKENS,
        )
        return self._client.models.generate_content(
            model=self.model_name,
            contents=prompt,
            config=config,
        )

    def _parse_response(self, response: Any, original_headline: str) -> list[Permutation]:
        """
        Extract and validate list[Permutation] from a Gemini response.

        Tries three strategies in order:
          1. response.parsed  — SDK auto-parses when response_schema was set
          2. JSON decode + Pydantic TypeAdapter — manual fallback
          3. Single fallback permutation using the original headline
        """
        # ── Strategy 1: SDK structured-output path ────────────────────────────
        parsed = getattr(response, "parsed", None)
        if parsed is not None:
            try:
                validated = _PERMUTATION_LIST_ADAPTER.validate_python(
                    [p.model_dump() if hasattr(p, "model_dump") else p for p in parsed]
                )
                if validated:
                    logger.debug("Parsed %d permutations via response.parsed", len(validated))
                    return validated
            except (ValidationError, Exception) as exc:
                logger.warning("response.parsed validation failed (%s); trying JSON path.", exc)

        # ── Strategy 2: Manual JSON decode ────────────────────────────────────
        raw_text = getattr(response, "text", None)
        if raw_text:
            try:
                data = json.loads(raw_text)
                # Gemini sometimes wraps the list in {"permutations": [...]}
                if isinstance(data, dict):
                    data = data.get("permutations", data.get("items", list(data.values())[0]))
                validated = _PERMUTATION_LIST_ADAPTER.validate_python(data)
                if validated:
                    logger.debug("Parsed %d permutations via JSON decode", len(validated))
                    return validated
            except (json.JSONDecodeError, ValidationError, Exception) as exc:
                logger.warning("JSON decode fallback also failed (%s).", exc)

        # ── Strategy 3: Emergency fallback ────────────────────────────────────
        logger.error(
            "Could not parse any permutations from Gemini response. "
            "Returning original headline as fallback."
        )
        return [_fallback_permutation(original_headline)]


# ── Module-level helpers (stateless, easily unit-tested) ──────────────────────

def _build_prompt(headline: str, count: int) -> str:
    """
    Construct the user-turn prompt for Gemini.

    Keeping this as a pure function (no self) makes it trivial to test
    independently and to swap the prompting strategy in the future.
    """
    return (
        f"Generate exactly {count} semantic permutations of the following news headline.\n\n"
        f'ORIGINAL HEADLINE: "{headline}"\n\n'
        f"Requirements:\n"
        f"  • Produce exactly {count} permutations — no more, no fewer.\n"
        f"  • Vary the strategies across the {count} items "
        f"(synonym, entity_generalization, passive_voice, active_voice, "
        f"temporal_paraphrase, structural_reframe, journalistic_rewrite, paraphrase).\n"
        f"  • Each permutation must independently describe the same story "
        f"without referencing the original headline.\n"
        f"  • Set confidence = 1.0 for permutations that perfectly preserve meaning.\n"
        f"  • Set confidence < 0.9 only for permutations that are slightly more general.\n"
    )


def _fallback_permutation(headline: str) -> Permutation:
    """
    Emergency permutation used when the LLM call fails entirely.

    Returning the original headline preserves minimal pipeline functionality:
    BroadScraperAgent will search for the original text, yielding some results
    even if semantic breadth is lost.
    """
    return Permutation(
        text=headline,
        strategy="fallback_original",
        confidence=0.5,
    )
