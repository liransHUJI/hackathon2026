"""
BaseAgent
=========
Abstract base class for all five Provenance Pipeline agents.

Every agent:
  - Implements process(item) → result  (the core transformation)
  - Can be unit-tested by calling process() directly without any queue
  - Optionally wired into the queue-driven run() loop by PipelineRunner

Queue protocol:
  - run() reads from self.input_queue until it receives the sentinel value None.
  - On sentinel receipt, it forwards None to self.output_queue (shutdown propagation).
  - Per-item exceptions are caught, logged with exc_info=True, and skipped — the
    loop continues. Misconfiguration errors should be raised in __init__, not here.
"""

from __future__ import annotations

import asyncio
import logging
from abc import ABC, abstractmethod
from typing import Any, Optional

from src.config import PipelineConfig


class BaseAgent(ABC):
    """Abstract base for all pipeline agents."""

    def __init__(
        self,
        name: str,
        input_queue: Optional[asyncio.Queue[Any]] = None,
        output_queue: Optional[asyncio.Queue[Any]] = None,
        config: Optional[PipelineConfig] = None,
    ) -> None:
        self.name = name
        self.input_queue = input_queue
        self.output_queue = output_queue
        self.config = config or PipelineConfig()
        self.logger = logging.getLogger(f"provenance.agent.{name}")
        self._running = False

    # ── Abstract interface ────────────────────────────────────────────────────

    @abstractmethod
    async def process(self, item: Any) -> Any:
        """
        Core transformation for a single pipeline item.

        CONTRACT:
          - Must be callable without queues (enables direct unit testing).
          - Returns the transformed item, or None to drop the item silently.
          - Should raise on unrecoverable errors; run() will catch and log them.
        """
        ...

    # ── Queue-driven loop ─────────────────────────────────────────────────────

    async def run(self) -> None:
        """
        Read items from input_queue, process each, push results to output_queue.

        Stops when it receives the sentinel value `None`.
        Forwards `None` to output_queue so shutdown propagates through the pipeline.
        """
        if self.input_queue is None:
            raise RuntimeError(
                f"Agent '{self.name}' has no input_queue. "
                "Either set one or call process() directly."
            )

        self._running = True
        self.logger.info("Agent started.")

        while self._running:
            item = await self.input_queue.get()

            # Sentinel → graceful shutdown
            if item is None:
                self.logger.info("Received sentinel — shutting down.")
                if self.output_queue is not None:
                    await self.output_queue.put(None)
                self.input_queue.task_done()
                break

            try:
                result = await self.process(item)
                if result is not None and self.output_queue is not None:
                    await self.output_queue.put(result)
            except Exception:
                self.logger.error(
                    "Unhandled error processing item — item dropped.",
                    exc_info=True,
                )
            finally:
                self.input_queue.task_done()

        self._running = False
        self.logger.info("Agent stopped.")

    async def stop(self) -> None:
        """Signal the run() loop to exit after the current item."""
        self._running = False

    # ── Repr ──────────────────────────────────────────────────────────────────

    def __repr__(self) -> str:
        return (
            f"{self.__class__.__name__}("
            f"name={self.name!r}, "
            f"running={self._running})"
        )
