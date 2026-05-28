"""
server.py — Web API that bridges the frontend UI to the existing PipelineRunner.

Serves:
  - Static files from /public (the DataScope UI)
  - POST /api/analyze  { "query": "headline text" }  → ProvenanceReport JSON

Usage:
  python server.py
  python server.py --port 3000
"""

from __future__ import annotations

import argparse
import asyncio
import json
import logging
import os
import sys
from datetime import datetime, timezone
from pathlib import Path

# Load .env before any src imports
from dotenv import load_dotenv
load_dotenv(Path(__file__).parent / "services" / "go-backend" / ".env")

from http.server import SimpleHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse
import threading

# Add the python-pipeline to sys.path so we can import from it
sys.path.insert(0, str(Path(__file__).parent / "services" / "python-pipeline"))

from src.config import PipelineConfig
from src.pipeline_runner import PipelineRunner

logger = logging.getLogger("provenance.server")

# ── Globals ───────────────────────────────────────────────────────────────────
_pipeline_runner: PipelineRunner | None = None
_event_loop: asyncio.AbstractEventLoop | None = None

PUBLIC_DIR = Path(__file__).parent / "public"


def get_runner() -> PipelineRunner:
    """Lazy-init the pipeline runner (expensive — loads ML models)."""
    global _pipeline_runner
    if _pipeline_runner is None:
        config = PipelineConfig()
        logger.info("Initializing PipelineRunner (this may take a moment)...")
        _pipeline_runner = PipelineRunner.from_config(config)
        logger.info("PipelineRunner ready.")
    return _pipeline_runner


def get_loop() -> asyncio.AbstractEventLoop:
    """Get or create a background event loop for running async pipeline."""
    global _event_loop
    if _event_loop is None or _event_loop.is_closed():
        _event_loop = asyncio.new_event_loop()
        t = threading.Thread(target=_event_loop.run_forever, daemon=True)
        t.start()
    return _event_loop


class APIHandler(SimpleHTTPRequestHandler):
    """
    Handles:
      - POST /api/analyze → runs the pipeline, returns ProvenanceReport JSON
      - GET /* → serves static files from public/
    """

    def __init__(self, *args, **kwargs):
        # Serve files from the public directory
        super().__init__(*args, directory=str(PUBLIC_DIR), **kwargs)

    def do_POST(self):
        parsed = urlparse(self.path)

        if parsed.path == "/api/analyze":
            self._handle_analyze()
        else:
            self.send_error(404, "Not Found")

    def _handle_analyze(self):
        try:
            # Read request body
            content_length = int(self.headers.get("Content-Length", 0))
            body = self.rfile.read(content_length)
            payload = json.loads(body)
            query = payload.get("query", "").strip()

            if not query:
                self._send_json({"error": "query is required"}, status=400)
                return

            # If the input is a URL, fetch its title/content to use as the headline
            headline = query
            if query.startswith("http://") or query.startswith("https://"):
                headline = self._extract_headline_from_url(query)

            logger.info(f"Analyzing: \"{headline[:80]}\"")

            # Run the full pipeline
            runner = get_runner()
            loop = get_loop()
            future = asyncio.run_coroutine_threadsafe(
                runner.run_pipeline(headline),
                loop,
            )

            # Wait for result (timeout: 120s for long pipelines)
            report = future.result(timeout=120)

            # Serialize using Pydantic's JSON serializer
            report_json = json.loads(report.model_dump_json())

            # Extract prevalent topics from results for the UI to use for images
            topics = self._extract_topics(report_json)
            report_json["_ui_topics"] = topics

            self._send_json(report_json)
            logger.info(f"Analysis complete. Risk: {report.risk_label.value}")

        except TimeoutError:
            self._send_json({"error": "Pipeline timed out (>120s). Try a shorter headline."}, status=504)
        except Exception as exc:
            logger.error(f"Pipeline error: {exc}", exc_info=True)
            self._send_json({"error": str(exc)}, status=500)

    def _extract_headline_from_url(self, url: str) -> str:
        """Fetch a URL and extract its title or first meaningful text."""
        try:
            import httpx
            from bs4 import BeautifulSoup
            resp = httpx.get(url, timeout=15, follow_redirects=True, headers={
                "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
            })
            soup = BeautifulSoup(resp.text, "html.parser")
            if soup.title and soup.title.string:
                return soup.title.string.strip()
            h1 = soup.find("h1")
            if h1:
                return h1.get_text(strip=True)
            og = soup.find("meta", property="og:title")
            if og and og.get("content"):
                return og["content"]
            return url
        except Exception as e:
            logger.warning(f"Could not fetch URL {url}: {e}")
            return url

    def _extract_topics(self, report: dict) -> list[str]:
        """
        Extract the most prevalent/interesting topics from the scraped results.
        Used by the frontend to generate relevant background images.
        """
        # Collect all titles and snippets
        texts = []
        for sig in report.get("ai_signature_results", []):
            sr = sig.get("ranked_result", {}).get("scraped_result", {})
            if sr.get("title"):
                texts.append(sr["title"])
            if sr.get("snippet"):
                texts.append(sr["snippet"])

        # Simple keyword extraction: find most common meaningful words
        stop_words = {
            "the", "a", "an", "is", "are", "was", "were", "be", "been", "being",
            "have", "has", "had", "do", "does", "did", "will", "would", "could",
            "should", "may", "might", "shall", "can", "need", "dare", "ought",
            "used", "to", "of", "in", "for", "on", "with", "at", "by", "from",
            "as", "into", "through", "during", "before", "after", "above", "below",
            "between", "out", "off", "over", "under", "again", "further", "then",
            "once", "here", "there", "when", "where", "why", "how", "all", "each",
            "every", "both", "few", "more", "most", "other", "some", "such", "no",
            "nor", "not", "only", "own", "same", "so", "than", "too", "very",
            "just", "because", "but", "and", "or", "if", "while", "about", "that",
            "this", "these", "those", "it", "its", "they", "them", "their", "we",
            "our", "you", "your", "he", "him", "his", "she", "her", "what", "which",
            "who", "whom", "whose", "i", "me", "my", "myself", "up", "down",
            "also", "been", "said", "says", "new", "one", "two", "first",
            "many", "much", "well", "back", "even", "still", "way", "take",
            "come", "make", "like", "get", "got", "see", "know", "think",
            "going", "want", "look", "use", "find", "give", "tell", "work",
            "call", "try", "ask", "seem", "feel", "leave", "put", "mean",
            "keep", "let", "begin", "show", "hear", "play", "run", "move",
            "live", "believe", "hold", "bring", "happen", "write", "provide",
            "sit", "stand", "lose", "pay", "meet", "include", "continue",
            "set", "learn", "change", "lead", "understand", "watch", "follow",
            "stop", "create", "speak", "read", "allow", "add", "spend", "grow",
            "open", "walk", "win", "offer", "remember", "love", "consider",
            "appear", "buy", "wait", "serve", "die", "send", "expect", "build",
            "stay", "fall", "cut", "reach", "kill", "remain", "missing",
        }

        word_counts = {}
        for text in texts:
            words = text.lower().split()
            for word in words:
                # Clean punctuation
                clean = "".join(c for c in word if c.isalnum())
                if len(clean) > 3 and clean not in stop_words:
                    word_counts[clean] = word_counts.get(clean, 0) + 1

        # Sort by frequency, take top 5 as topic keywords
        sorted_words = sorted(word_counts.items(), key=lambda x: x[1], reverse=True)
        topics = [w[0] for w in sorted_words[:5]]

        return topics if topics else ["news", "analysis", "data"]

    def _send_json(self, data: dict, status: int = 200):
        response = json.dumps(data, ensure_ascii=False)
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(response.encode())))
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
        self.wfile.write(response.encode())

    def do_OPTIONS(self):
        """Handle CORS preflight."""
        self.send_response(204)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Content-Type")
        self.end_headers()

    def log_message(self, format, *args):
        """Suppress default access logs for static files, keep API logs."""
        if "/api/" in (args[0] if args else ""):
            logger.info(format % args)


def main():
    parser = argparse.ArgumentParser(description="DataScope API Server")
    parser.add_argument("--port", type=int, default=3000, help="Port to listen on")
    parser.add_argument("--host", default="0.0.0.0", help="Host to bind to")
    args = parser.parse_args()

    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s  %(name)s  %(levelname)s  %(message)s",
        stream=sys.stdout,
    )

    # Pre-initialize the pipeline (loads models)
    print(f"\n  DataScope Server")
    print(f"  ────────────────────────────────────")
    print(f"  Initializing pipeline...")
    try:
        get_runner()
    except Exception as exc:
        print(f"  [WARN] Pipeline init failed: {exc}")
        print(f"  Server will still start — errors will surface on /api/analyze calls.\n")

    server = HTTPServer((args.host, args.port), APIHandler)
    print(f"  ✓ Serving UI from: {PUBLIC_DIR}")
    print(f"  ✓ API endpoint:    POST http://localhost:{args.port}/api/analyze")
    print(f"  ✓ Open browser:    http://localhost:{args.port}")
    print(f"  ────────────────────────────────────\n")

    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\n  Shutting down...")
        server.shutdown()


if __name__ == "__main__":
    main()
