#!/usr/bin/env python3
"""
AgentSwarm Data Collector
=========================
Polls the Attena API at regular intervals and saves market snapshots
to JSON files. Run this for 1-2 weeks before training the ML models.

Usage:
    python ml/collect_data.py
    
    # Or with custom interval:
    POLL_INTERVAL=120 python ml/collect_data.py   # every 2 minutes
"""

import os
import json
import time
import requests
import datetime
from pathlib import Path

# ── Config ──
ATTENA_BASE = os.environ.get("ATTENA_BASE_URL", "https://attena-api.fly.dev/api/search/")
API_KEY = os.environ.get("ATTENA_API_KEY", "")
POLL_INTERVAL = int(os.environ.get("POLL_INTERVAL", "300"))  # 5 minutes default
DATA_DIR = Path(__file__).parent / "data"
DATA_DIR.mkdir(exist_ok=True)

CATEGORIES = ["politics", "crypto", "geopolitics", "sports", "economics"]
SNAPSHOT_FILE = DATA_DIR / "market_snapshots.jsonl"  # append-only line-delimited JSON

session = requests.Session()
session.headers.update({
    "User-Agent": "AgentSwarm-Collector/1.0",
})
if API_KEY:
    session.headers["X-API-Key"] = API_KEY


def fetch_markets(category: str, sort: str = "volume", limit: int = 50) -> list:
    """Fetch markets from Attena API."""
    params = {
        "category": category,
        "sort": sort,
        "limit": limit,
        "agent": "true",  # skip LLM reranking for speed
    }
    try:
        resp = session.get(ATTENA_BASE, params=params, timeout=15)
        resp.raise_for_status()
        data = resp.json()
        return data.get("results", [])
    except Exception as e:
        print(f"  ✗ Error fetching {category}: {e}")
        return []


def fetch_cross_platform(query: str) -> dict:
    """Fetch the same query from both Kalshi and Polymarket for arb detection."""
    results = {"kalshi": [], "polymarket": []}
    for source in ["kalshi", "polymarket"]:
        params = {
            "q": query,
            "source": source,
            "sort": "volume",
            "limit": 30,
            "agent": "true",
        }
        try:
            resp = session.get(ATTENA_BASE, params=params, timeout=15)
            resp.raise_for_status()
            data = resp.json()
            results[source] = data.get("results", [])
        except Exception as e:
            print(f"  ✗ Error fetching {source}/{query}: {e}")
    return results


def save_snapshot(markets: list, category: str, timestamp: str):
    """Append snapshot to JSONL file."""
    with open(SNAPSHOT_FILE, "a") as f:
        for m in markets:
            record = {
                "snapshot_time": timestamp,
                "category_query": category,
                "market_id": m.get("market_id"),
                "title": m.get("title"),
                "source": m.get("source"),
                "category": m.get("category"),
                "subcategory": m.get("subcategory"),
                "yes_price": m.get("yes_price"),
                "no_price": m.get("no_price"),
                "volume": m.get("volume"),
                "volume_24h": m.get("volume_24h"),
                "close_time": m.get("close_time"),
                "ticker": m.get("ticker"),
                "outcome_label": m.get("outcome_label"),
            }
            f.write(json.dumps(record) + "\n")


def save_arb_snapshot(cross_data: dict, query: str, timestamp: str):
    """Save cross-platform data for arbitrage training."""
    arb_file = DATA_DIR / "arb_snapshots.jsonl"
    with open(arb_file, "a") as f:
        record = {
            "snapshot_time": timestamp,
            "query": query,
            "kalshi_count": len(cross_data["kalshi"]),
            "poly_count": len(cross_data["polymarket"]),
            "kalshi": [
                {
                    "market_id": m.get("market_id"),
                    "title": m.get("title"),
                    "yes_price": m.get("yes_price"),
                    "volume": m.get("volume"),
                    "ticker": m.get("ticker"),
                }
                for m in cross_data["kalshi"]
            ],
            "polymarket": [
                {
                    "market_id": m.get("market_id"),
                    "title": m.get("title"),
                    "yes_price": m.get("yes_price"),
                    "volume": m.get("volume"),
                    "ticker": m.get("ticker"),
                }
                for m in cross_data["polymarket"]
            ],
        }
        f.write(json.dumps(record) + "\n")


def collect_once():
    """Run one collection cycle."""
    now = datetime.datetime.utcnow()
    timestamp = now.isoformat() + "Z"
    total = 0

    print(f"\n[{now.strftime('%H:%M:%S')}] Collecting snapshots...")

    # 1. Collect by category
    for cat in CATEGORIES:
        markets = fetch_markets(cat, sort="volume", limit=50)
        if markets:
            save_snapshot(markets, cat, timestamp)
            total += len(markets)
            print(f"  ✓ {cat}: {len(markets)} markets")

    # 2. Collect trending
    trending = fetch_markets("", sort="trending", limit=30)
    if trending:
        save_snapshot(trending, "trending", timestamp)
        total += len(trending)
        print(f"  ✓ trending: {len(trending)} markets")

    # 3. Cross-platform snapshots for arbitrage
    for query in ["bitcoin", "election", "federal reserve", "ukraine"]:
        cross = fetch_cross_platform(query)
        save_arb_snapshot(cross, query, timestamp)
        print(f"  ✓ arb/{query}: K={len(cross['kalshi'])} P={len(cross['polymarket'])}")

    file_size = SNAPSHOT_FILE.stat().st_size / (1024 * 1024) if SNAPSHOT_FILE.exists() else 0
    print(f"  Total: {total} records | File: {file_size:.1f} MB")


def main():
    print("═" * 50)
    print("AgentSwarm Data Collector")
    print(f"  API:      {ATTENA_BASE}")
    print(f"  Interval: {POLL_INTERVAL}s")
    print(f"  Output:   {DATA_DIR}")
    print(f"  Categories: {CATEGORIES}")
    print("═" * 50)
    print("\nCollecting data. Press Ctrl+C to stop.")
    print("Run for 1-2 weeks for good training data.\n")

    while True:
        try:
            collect_once()
            time.sleep(POLL_INTERVAL)
        except KeyboardInterrupt:
            print("\n\nStopped. Data saved to:")
            print(f"  {SNAPSHOT_FILE}")
            print(f"  {DATA_DIR / 'arb_snapshots.jsonl'}")
            break
        except Exception as e:
            print(f"Error: {e}")
            time.sleep(30)


if __name__ == "__main__":
    main()
