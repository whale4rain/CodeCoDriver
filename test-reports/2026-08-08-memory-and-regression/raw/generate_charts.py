import json
import re
from pathlib import Path

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt


BASE = Path(__file__).resolve().parent.parent
RAW = BASE / "raw"
CHARTS = BASE / "charts"


def read_lines(name):
    return (RAW / name).read_text(encoding="utf-8").splitlines()


def parse_test_counts():
    passed = failed = skipped = 0
    for line in read_lines("go-test-json.txt"):
        line = line.strip()
        if not line:
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if not event.get("Test"):
            continue
        if event.get("Action") == "pass":
            passed += 1
        elif event.get("Action") == "fail":
            failed += 1
        elif event.get("Action") == "skip":
            skipped += 1
    return passed, failed, skipped


def parse_coverage():
    coverage = []
    for line in read_lines("go-test-cover.txt"):
        match = re.search(r"ok\s+(\S+)\s+.*coverage:\s+([0-9.]+)%", line)
        if match:
            coverage.append((match.group(1), float(match.group(2))))
    coverage.sort(key=lambda item: item[1], reverse=True)
    return coverage


def chart_test_summary(passed, failed, skipped):
    labels = ["Passed", "Skipped", "Failed"]
    values = [passed, skipped, failed]
    colors = ["#4f9d69", "#d8b45f", "#c95d43"]
    fig, ax = plt.subplots(figsize=(7, 4))
    bars = ax.bar(labels, values, color=colors)
    ax.bar_label(bars, padding=2)
    ax.set_ylim(0, max(values) * 1.18)
    ax.set_title("Go Test Summary", fontweight="bold")
    ax.set_ylabel("Tests")
    ax.spines[["top", "right"]].set_visible(False)
    fig.tight_layout()
    fig.savefig(CHARTS / "test-result-summary.png", dpi=160)
    plt.close(fig)


def chart_coverage(coverage):
    packages = [item[0].replace("codecodriver/", "") for item in coverage]
    values = [item[1] for item in coverage]
    fig, ax = plt.subplots(figsize=(9, 5))
    bars = ax.barh(packages, values, color="#5b8f66")
    ax.bar_label(bars, fmt="%.1f%%", padding=3)
    ax.set_xlim(0, 100)
    ax.set_title("Statement Coverage by Package", fontweight="bold")
    ax.set_xlabel("Coverage %")
    ax.invert_yaxis()
    ax.spines[["top", "right"]].set_visible(False)
    fig.tight_layout()
    fig.savefig(CHARTS / "package-coverage.png", dpi=160)
    plt.close(fig)


def chart_feature_matrix():
    categories = [
        "Full Go suite",
        "Memory package",
        "Runtime async memory",
        "PostgreSQL + Doubao",
        "Redis lease",
    ]
    values = [77, 6, 3, 4, 1]
    fig, ax = plt.subplots(figsize=(8, 4.5))
    bars = ax.bar(categories, values, color=["#3f6d8f", "#4f9d69", "#4f9d69", "#4f9d69", "#4f9d69"])
    ax.bar_label(bars, padding=2)
    ax.set_ylim(0, 90)
    ax.set_title("Focused Integration Test Matrix", fontweight="bold")
    ax.set_ylabel("Passing tests")
    ax.tick_params(axis="x", rotation=12)
    ax.spines[["top", "right"]].set_visible(False)
    fig.tight_layout()
    fig.savefig(CHARTS / "feature-matrix.png", dpi=160)
    plt.close(fig)


def main():
    passed, failed, skipped = parse_test_counts()
    chart_test_summary(passed, failed, skipped)
    chart_coverage(parse_coverage())
    chart_feature_matrix()
    print(f"charts generated: passed={passed} failed={failed} skipped={skipped}")


if __name__ == "__main__":
    main()
