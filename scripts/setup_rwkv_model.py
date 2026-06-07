#!/usr/bin/env python3
"""Download and convert the latest RWKV World model for renia.

Supports RWKV-6/7 World models quantized for rwkv.cpp (Q4_0, Q5_1, Q8_0).
Recommended for 8GB VRAM:
  - RWKV-7 World 3B  Q5_1  (~2.3 GB, fastest, best quality)
  - RWKV-6 World 7B  Q4_0  (~4.2 GB, slower but stronger)

Usage:
  python3 scripts/setup_rwkv_model.py --model 7b3 --quant Q5_1
  python3 scripts/setup_rwkv_model.py --model 6b7 --quant Q4_0
"""

import argparse
import os
import subprocess
import sys
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parent.parent
RWKV_CPP = PROJECT_ROOT / "third_party" / "rwkv.cpp" / "python"
MODEL_DIR = PROJECT_ROOT / "models"

MODELS = {
    "7b3": {
        "repo": "BlinkDL/rwkv-7-world",
        "file": "RWKV-x070-World-2.9B-v3-20250211-ctx4096.pth",
        "desc": "RWKV-7 World 2.9B (latest architecture, best for Korean/multilingual)",
    },
    "6b7": {
        "repo": "BlinkDL/rwkv-6-world",
        "file": "RWKV-x060-World-7B-v3-20241112-ctx4096.pth",
        "desc": "RWKV-6 World 7B v3 (stronger reasoning, good for 8GB VRAM)",
    },
}

QUANTS = ["Q4_0", "Q5_1", "Q8_0"]


def run(cmd, cwd=None):
    print(f"[RUN] {' '.join(cmd)}")
    result = subprocess.run(cmd, cwd=cwd)
    if result.returncode != 0:
        sys.exit(result.returncode)


def download_with_hf_hub(repo_id, filename, local_dir):
    try:
        from huggingface_hub import hf_hub_download
    except ImportError:
        print("ERROR: huggingface_hub is not installed.")
        print("  pip install huggingface_hub")
        print("Or use wget/curl to download the .pth manually.")
        sys.exit(1)

    return hf_hub_download(
        repo_id=repo_id,
        filename=filename,
        local_dir=local_dir,
        local_dir_use_symlinks=False,
    )


def main():
    parser = argparse.ArgumentParser(description="Download & quantize RWKV World model")
    parser.add_argument(
        "--model",
        choices=list(MODELS.keys()),
        default="7b3",
        help="Model variant to download (default: 7b3)",
    )
    parser.add_argument(
        "--quant",
        choices=QUANTS,
        default="Q5_1",
        help="Quantization type (default: Q5_1)",
    )
    parser.add_argument(
        "--output",
        default=str(PROJECT_ROOT / "model.bin"),
        help="Output path for the final quantized model",
    )
    args = parser.parse_args()

    info = MODELS[args.model]
    MODEL_DIR.mkdir(parents=True, exist_ok=True)

    pth_path = MODEL_DIR / info["file"]
    if not pth_path.exists():
        print(f"\n>>> Downloading {info['desc']} from HuggingFace…")
        downloaded = download_with_hf_hub(info["repo"], info["file"], str(MODEL_DIR))
        pth_path = Path(downloaded)
    else:
        print(f"\n>>> Found existing {pth_path}")

    fp16_path = MODEL_DIR / pth_path.name.replace(".pth", "-FP16.bin")
    if not fp16_path.exists():
        print("\n>>> Converting PyTorch checkpoint to GGML FP16…")
        run(
            [
                sys.executable,
                str(RWKV_CPP / "convert_pytorch_to_ggml.py"),
                str(pth_path),
                str(fp16_path),
                "FP16",
            ]
        )
    else:
        print(f">>> Found existing FP16 conversion {fp16_path}")

    quant_path = MODEL_DIR / pth_path.name.replace(".pth", f"-{args.quant}.bin")
    if not quant_path.exists():
        print(f"\n>>> Quantizing to {args.quant}…")
        run(
            [
                sys.executable,
                str(RWKV_CPP / "quantize.py"),
                str(fp16_path),
                str(quant_path),
                args.quant,
            ]
        )
    else:
        print(f">>> Found existing quantization {quant_path}")

    output = Path(args.output)
    if output.exists() or output.is_symlink():
        old = output.with_suffix(".bin.bak")
        print(f">>> Backing up old model to {old}")
        output.rename(old)

    output.hardlink_to(quant_path)
    print(f"\n>>> Success! Installed {info['desc']} ({args.quant}) -> {output}")
    print(
        f">>> Size: {output.stat().st_size / 1024 / 1024:.1f} MB"
    )
    print(
        f">>> Run: ./{PROJECT_ROOT.name}/renia   (or set RWKV_MODEL_PATH={output})"
    )


if __name__ == "__main__":
    main()
