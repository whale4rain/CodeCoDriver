from pathlib import Path

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt


BASE = Path(__file__).resolve().parent
CHARTS = BASE / "charts"
CHARTS.mkdir(exist_ok=True)

modes = ["with_memory", "without_memory"]
passed = [2, 1]
total = [6, 6]
avg_duration_ms = [81033, 61712]
memory_hits = [25, 0]
repair_attempts = [11, 12]
success_hits = [10, 0]
failure_hits = [15, 0]
refined_hits = [13, 0]

cases = [
    "health-response",
    "pagination-validation",
    "pagination-edge",
    "health-version",
    "link-header",
    "db-logging",
]
with_results = [True, False, True, False, False, False]
without_results = [True, False, False, False, False, False]


def chart_pass_rate():
    fig, ax = plt.subplots(figsize=(6, 4))
    rates = [p / t for p, t in zip(passed, total)]
    bars = ax.bar(modes, rates, color=["#4f9d69", "#d8b45f"])
    ax.bar_label(bars, fmt="%.0f%%", padding=2)
    ax.set_ylim(0, 1.15)
    ax.set_title("Memory A/B Pass Rate - V3 Quality Gating", fontweight="bold")
    ax.set_ylabel("Pass rate")
    ax.spines[["top", "right"]].set_visible(False)
    fig.tight_layout()
    fig.savefig(CHARTS / "pass-rate.png", dpi=160)
    plt.close(fig)


def chart_duration():
    fig, ax = plt.subplots(figsize=(6, 4))
    bars = ax.bar(modes, avg_duration_ms, color=["#5b8f8f", "#d8a95f"])
    ax.bar_label(bars, fmt="%.0f ms", padding=2)
    ax.set_title("Average Duration by Memory Mode", fontweight="bold")
    ax.set_ylabel("ms")
    ax.spines[["top", "right"]].set_visible(False)
    fig.tight_layout()
    fig.savefig(CHARTS / "avg-duration.png", dpi=160)
    plt.close(fig)


def chart_metrics():
    fig, ax = plt.subplots(figsize=(8, 4.5))
    width = 0.18
    x = range(len(modes))
    values = [memory_hits, repair_attempts, success_hits, failure_hits, refined_hits]
    labels = ["memory hits", "repair attempts", "success hits", "failure hits", "refined hits"]
    colors = ["#4f9d69", "#d8a95f", "#5b8f8f", "#c95d43", "#7f9bb3"]
    for i, (value, label, color) in enumerate(zip(values, labels, colors)):
        offset = (i - 2) * width
        ax.bar([j + offset for j in x], value, width, label=label, color=color)
    ax.set_xticks(list(x))
    ax.set_xticklabels(modes)
    ax.set_title("Memory Source and Repair Metrics", fontweight="bold")
    ax.set_ylabel("Count")
    ax.legend()
    ax.spines[["top", "right"]].set_visible(False)
    fig.tight_layout()
    fig.savefig(CHARTS / "metrics.png", dpi=160)
    plt.close(fig)


def chart_per_case():
    fig, ax = plt.subplots(figsize=(9, 4.5))
    x = range(len(cases))
    width = 0.35
    with_vals = [1 if value else 0 for value in with_results]
    without_vals = [1 if value else 0 for value in without_results]
    ax.bar([i - width / 2 for i in x], with_vals, width, label="with_memory", color="#4f9d69")
    ax.bar([i + width / 2 for i in x], without_vals, width, label="without_memory", color="#d8b45f")
    ax.set_xticks(list(x))
    ax.set_xticklabels(cases, rotation=18, ha="right")
    ax.set_ylim(0, 1.2)
    ax.set_yticks([0, 1])
    ax.set_yticklabels(["Not passed", "Passed"])
    ax.set_title("Per-Case Memory A/B Result - V3", fontweight="bold")
    ax.legend()
    ax.spines[["top", "right"]].set_visible(False)
    fig.tight_layout()
    fig.savefig(CHARTS / "per-case.png", dpi=160)
    plt.close(fig)


def main():
    chart_pass_rate()
    chart_duration()
    chart_metrics()
    chart_per_case()
    print("v3 charts generated")


if __name__ == "__main__":
    main()
