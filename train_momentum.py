#!/usr/bin/env python3
"""
AgentSwarm — Momentum Agent ML Training
========================================
Trains an XGBoost model to predict short-term price movements
in prediction markets. Uses data collected by collect_data.py.

Inputs:  ml/data/market_snapshots.jsonl  (from collector)
Outputs: ml/models/momentum_model.joblib (for the Go agent)
         ml/models/momentum_features.json (feature config)

Usage:
    # Activate venv first
    source ml/venv/bin/activate
    python ml/train_momentum.py
    
Requirements: 1-2 weeks of collected data minimum.
"""

import json
import os
import sys
from pathlib import Path
from collections import defaultdict

import numpy as np
import pandas as pd
from sklearn.model_selection import TimeSeriesSplit
from sklearn.metrics import accuracy_score, precision_score, recall_score, f1_score, classification_report
import xgboost as xgb
import joblib

# ── Paths ──
DATA_DIR = Path(__file__).parent / "data"
MODEL_DIR = Path(__file__).parent / "models"
MODEL_DIR.mkdir(exist_ok=True)

SNAPSHOT_FILE = DATA_DIR / "market_snapshots.jsonl"
MODEL_FILE = MODEL_DIR / "momentum_model.joblib"
FEATURES_FILE = MODEL_DIR / "momentum_features.json"


def load_snapshots() -> pd.DataFrame:
    """Load JSONL snapshot data into a DataFrame."""
    if not SNAPSHOT_FILE.exists():
        print(f"✗ No data found at {SNAPSHOT_FILE}")
        print("  Run the collector first: make collect")
        print("  Let it run for at least 1-2 weeks.")
        sys.exit(1)

    records = []
    with open(SNAPSHOT_FILE) as f:
        for line in f:
            line = line.strip()
            if line:
                try:
                    records.append(json.loads(line))
                except json.JSONDecodeError:
                    continue

    df = pd.DataFrame(records)
    df["snapshot_time"] = pd.to_datetime(df["snapshot_time"])
    df = df.sort_values(["market_id", "snapshot_time"])

    print(f"✓ Loaded {len(df):,} snapshots")
    print(f"  Date range: {df['snapshot_time'].min()} → {df['snapshot_time'].max()}")
    print(f"  Unique markets: {df['market_id'].nunique():,}")
    print(f"  Categories: {df['category'].value_counts().to_dict()}")

    return df


def engineer_features(df: pd.DataFrame) -> pd.DataFrame:
    """
    Feature engineering for momentum prediction.
    
    For each market at each snapshot, we compute:
    - Price changes over different horizons (1h, 6h, 24h lookback)
    - Volume ratios (current vs rolling average)
    - Time to close
    - Price level features
    - Category-level momentum
    """
    features = []

    # Group by market
    for market_id, group in df.groupby("market_id"):
        group = group.sort_values("snapshot_time").copy()

        if len(group) < 6:
            continue  # need minimum history

        prices = group["yes_price"].values
        volumes = group["volume_24h"].fillna(0).values
        times = group["snapshot_time"].values

        for i in range(5, len(group)):
            row = group.iloc[i]
            current_price = prices[i]

            if current_price is None or np.isnan(current_price):
                continue

            # ── Price features ──
            # Lookback price changes (using available snapshots, not exact hours)
            price_1back = prices[i - 1] if i >= 1 else current_price
            price_3back = prices[i - 3] if i >= 3 else current_price
            price_5back = prices[i - 5] if i >= 5 else current_price

            pct_change_1 = (current_price - price_1back) / max(price_1back, 0.01)
            pct_change_3 = (current_price - price_3back) / max(price_3back, 0.01)
            pct_change_5 = (current_price - price_5back) / max(price_5back, 0.01)

            # Rolling stats
            window = prices[max(0, i - 5) : i]
            rolling_mean = np.mean(window)
            rolling_std = np.std(window) if len(window) > 1 else 0.01
            z_score = (current_price - rolling_mean) / max(rolling_std, 0.001)

            # ── Volume features ──
            vol_current = volumes[i]
            vol_window = volumes[max(0, i - 5) : i]
            vol_mean = np.mean(vol_window) if len(vol_window) > 0 else 1
            vol_ratio = vol_current / max(vol_mean, 1)

            # ── Time features ──
            close_time = pd.to_datetime(row.get("close_time"))
            snapshot_time = pd.to_datetime(row["snapshot_time"])
            days_to_close = -1
            if close_time is not pd.NaT and close_time is not None:
                try:
                    days_to_close = (close_time - snapshot_time).total_seconds() / 86400
                except:
                    days_to_close = -1

            # ── Price level ──
            price_level = current_price  # markets near 0.5 are most uncertain
            price_extremity = abs(current_price - 0.5) * 2  # 0 = uncertain, 1 = certain

            # ── Target: does price move >5% in next 2 snapshots? ──
            target = 0
            if i + 2 < len(group):
                future_price = prices[i + 2]
                if future_price is not None and not np.isnan(future_price):
                    future_change = abs(future_price - current_price) / max(current_price, 0.01)
                    if future_change > 0.05:
                        target = 1  # significant move detected

                    # Direction (for directional prediction)
                    direction = 1 if future_price > current_price else 0
            else:
                continue  # can't compute target without future data

            features.append({
                "market_id": market_id,
                "snapshot_time": str(row["snapshot_time"]),
                "category": row.get("category", "unknown"),
                "source": row.get("source", "unknown"),
                # Features
                "price": current_price,
                "pct_change_1": pct_change_1,
                "pct_change_3": pct_change_3,
                "pct_change_5": pct_change_5,
                "z_score": z_score,
                "rolling_std": rolling_std,
                "vol_ratio": vol_ratio,
                "vol_current": vol_current,
                "days_to_close": days_to_close,
                "price_extremity": price_extremity,
                # Target
                "target_big_move": target,
                "target_direction": direction,
            })

    feat_df = pd.DataFrame(features)
    print(f"\n✓ Engineered {len(feat_df):,} feature rows")
    print(f"  Target distribution (big_move): {feat_df['target_big_move'].value_counts().to_dict()}")

    return feat_df


def train_model(feat_df: pd.DataFrame):
    """Train XGBoost model with time-series cross-validation."""

    feature_cols = [
        "price",
        "pct_change_1",
        "pct_change_3",
        "pct_change_5",
        "z_score",
        "rolling_std",
        "vol_ratio",
        "vol_current",
        "days_to_close",
        "price_extremity",
    ]

    # Filter out rows with missing features
    train_df = feat_df.dropna(subset=feature_cols + ["target_big_move"])

    if len(train_df) < 100:
        print(f"\n✗ Only {len(train_df)} training rows — need at least 100.")
        print("  Run the data collector for longer.")
        sys.exit(1)

    X = train_df[feature_cols].values
    y = train_df["target_big_move"].values

    print(f"\n{'═' * 50}")
    print("Training XGBoost model")
    print(f"  Samples:  {len(X):,}")
    print(f"  Features: {len(feature_cols)}")
    print(f"  Positive: {y.sum():,} ({y.mean() * 100:.1f}%)")
    print(f"{'═' * 50}")

    # Handle class imbalance
    pos_count = y.sum()
    neg_count = len(y) - pos_count
    scale_pos_weight = neg_count / max(pos_count, 1)

    model = xgb.XGBClassifier(
        n_estimators=200,
        max_depth=4,
        learning_rate=0.05,
        subsample=0.8,
        colsample_bytree=0.8,
        scale_pos_weight=scale_pos_weight,
        eval_metric="logloss",
        random_state=42,
        use_label_encoder=False,
    )

    # Time-series cross-validation (NOT random — avoids lookahead bias)
    tscv = TimeSeriesSplit(n_splits=5)
    fold_scores = []

    print("\nCross-validation (time-series split):")
    for fold, (train_idx, val_idx) in enumerate(tscv.split(X)):
        X_train, X_val = X[train_idx], X[val_idx]
        y_train, y_val = y[train_idx], y[val_idx]

        model.fit(X_train, y_train, eval_set=[(X_val, y_val)], verbose=False)

        y_pred = model.predict(X_val)
        acc = accuracy_score(y_val, y_pred)
        prec = precision_score(y_val, y_pred, zero_division=0)
        rec = recall_score(y_val, y_pred, zero_division=0)
        f1 = f1_score(y_val, y_pred, zero_division=0)

        fold_scores.append({"acc": acc, "prec": prec, "rec": rec, "f1": f1})
        print(f"  Fold {fold + 1}: acc={acc:.3f} prec={prec:.3f} rec={rec:.3f} f1={f1:.3f}")

    avg_acc = np.mean([s["acc"] for s in fold_scores])
    avg_f1 = np.mean([s["f1"] for s in fold_scores])
    print(f"\n  Average: acc={avg_acc:.3f} f1={avg_f1:.3f}")

    # ── Train final model on all data ──
    print("\nTraining final model on all data...")
    model.fit(X, y, verbose=False)

    # Feature importance
    importance = dict(zip(feature_cols, model.feature_importances_))
    sorted_imp = sorted(importance.items(), key=lambda x: x[1], reverse=True)
    print("\nFeature importance:")
    for feat, imp in sorted_imp:
        bar = "█" * int(imp * 50)
        print(f"  {feat:20s} {imp:.3f} {bar}")

    # ── Save model ──
    joblib.dump(model, MODEL_FILE)
    print(f"\n✓ Model saved to {MODEL_FILE}")

    # Save feature config (so the Go agent knows what features to compute)
    feature_config = {
        "features": feature_cols,
        "model_path": str(MODEL_FILE),
        "training_samples": len(X),
        "avg_accuracy": float(avg_acc),
        "avg_f1": float(avg_f1),
        "class_distribution": {
            "positive": int(y.sum()),
            "negative": int(len(y) - y.sum()),
        },
        "feature_importance": {k: float(v) for k, v in sorted_imp},
        "trained_at": pd.Timestamp.now().isoformat(),
        "thresholds": {
            "z_score_trigger": 2.0,
            "min_confidence": 0.6,
            "vol_ratio_boost": 2.0,
        },
    }

    with open(FEATURES_FILE, "w") as f:
        json.dump(feature_config, f, indent=2)
    print(f"✓ Feature config saved to {FEATURES_FILE}")

    return model, feature_config


def backtest(feat_df: pd.DataFrame, model):
    """Simple backtest: simulate trading based on model predictions."""
    feature_cols = [
        "price", "pct_change_1", "pct_change_3", "pct_change_5",
        "z_score", "rolling_std", "vol_ratio", "vol_current",
        "days_to_close", "price_extremity",
    ]

    test_df = feat_df.dropna(subset=feature_cols).tail(500)  # last 500 rows
    if len(test_df) == 0:
        print("Not enough data for backtesting.")
        return

    X_test = test_df[feature_cols].values
    predictions = model.predict(X_test)
    probabilities = model.predict_proba(X_test)[:, 1]

    # Simulate paper trades
    capital = 1000
    position_size = 50  # $50 per trade
    pnl = 0
    trades = 0
    wins = 0

    print(f"\n{'═' * 50}")
    print("Backtest Results (last 500 snapshots)")
    print(f"{'═' * 50}")

    for i, (pred, prob) in enumerate(zip(predictions, probabilities)):
        if pred == 1 and prob > 0.6:  # only trade high-confidence predictions
            actual = test_df.iloc[i]["target_big_move"]
            trades += 1
            if actual == 1:
                pnl += position_size * 0.15  # average win on big move
                wins += 1
            else:
                pnl -= position_size * 0.05  # average loss on no move
            capital += pnl

    if trades > 0:
        print(f"  Trades:   {trades}")
        print(f"  Wins:     {wins} ({wins / trades * 100:.1f}%)")
        print(f"  P&L:      ${pnl:.2f}")
        print(f"  Capital:  ${capital:.2f}")
    else:
        print("  No trades triggered (model too conservative or not enough data).")


def main():
    print("═" * 50)
    print("AgentSwarm — Momentum Agent Trainer")
    print("═" * 50)

    # Load data
    df = load_snapshots()

    # Engineer features
    feat_df = engineer_features(df)

    if len(feat_df) < 50:
        print("\n⚠️  Not enough data for training yet.")
        print("   Keep running the collector (make collect) for a few more days.")
        sys.exit(0)

    # Train
    model, config = train_model(feat_df)

    # Backtest
    backtest(feat_df, model)

    print(f"\n{'═' * 50}")
    print("Next steps:")
    print("  1. If accuracy > 55%, the model has signal")
    print("  2. Export model for Go agent (see ml/serve_model.py)")
    print("  3. Retrain weekly with fresh data")
    print(f"{'═' * 50}")


if __name__ == "__main__":
    main()
