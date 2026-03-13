#!/usr/bin/env python3
"""
AgentSwarm — ML Model Server
=============================
Lightweight HTTP server that serves the trained XGBoost model.
The Go Momentum agent calls this for predictions.

Usage:
    source ml/venv/bin/activate
    python ml/serve_model.py

Endpoint:
    POST http://localhost:5050/predict
    Body: {"features": [price, pct_1, pct_3, pct_5, z, std, vol_r, vol, days, extremity]}
    Response: {"prediction": 0/1, "probability": 0.73, "confidence": 0.73}
"""

import json
import os
import sys
from http.server import HTTPServer, BaseHTTPRequestHandler
from pathlib import Path

import numpy as np
import joblib

MODEL_DIR = Path(__file__).parent / "models"
MODEL_FILE = MODEL_DIR / "momentum_model.joblib"
PORT = int(os.environ.get("MODEL_PORT", "5050"))

# Load model at startup
model = None
if MODEL_FILE.exists():
    model = joblib.load(MODEL_FILE)
    print(f"✓ Loaded model from {MODEL_FILE}")
else:
    print(f"⚠️  No model found at {MODEL_FILE}")
    print("   Train first: make train")
    print("   Running with mock predictions.")


class PredictHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/predict":
            self.send_error(404)
            return

        content_length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(content_length)

        try:
            data = json.loads(body)
            features = np.array(data["features"]).reshape(1, -1)

            if model is not None:
                prediction = int(model.predict(features)[0])
                probability = float(model.predict_proba(features)[0][1])
            else:
                # Mock prediction for development
                z_score = features[0][4] if len(features[0]) > 4 else 0
                probability = min(1.0, max(0.0, abs(z_score) / 4.0))
                prediction = 1 if probability > 0.5 else 0

            response = {
                "prediction": prediction,
                "probability": probability,
                "confidence": probability if prediction == 1 else 1 - probability,
            }

            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(response).encode())

        except Exception as e:
            self.send_response(400)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"error": str(e)}).encode())

    def do_GET(self):
        if self.path == "/health":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({
                "status": "ok",
                "model_loaded": model is not None,
            }).encode())
            return
        self.send_error(404)

    def log_message(self, format, *args):
        # Suppress default logging for cleaner output
        pass


def main():
    server = HTTPServer(("0.0.0.0", PORT), PredictHandler)
    print(f"Model server running on http://localhost:{PORT}")
    print(f"  POST /predict  — get prediction")
    print(f"  GET  /health   — health check")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nShutting down model server.")
        server.shutdown()


if __name__ == "__main__":
    main()
