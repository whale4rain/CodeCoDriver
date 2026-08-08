from pathlib import Path

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt


BASE = Path(__file__).resolve().parent
CHARTS = BASE / "charts"
CHARTS.mkdir(exist_ok=True)

modes = ["with_memory", "without_memory"]
passed = [0, 1]
total = [2, 2]
avg_duration_ms = [80471, 163056]
memory_hits = [5, 0]
repair_attempts = [3, 3]


def chart_pass_rate():
    fig, ax = plt.subplots(figsize=(6, 4))
    rates = [p / t for p, t in zip(passed, total)]
    bars = ax.bar(modes, rates, color=["#4f9d69", "#d8b45f"])
    ax.bar_label(bars, fmt="%.0f%%", padding=2)
    ax.set_ylim(0, 1.15)
    ax.set_title("Memory A/B Pass Rate", fontweight="bold")
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
    fig, ax = plt.subplots(figsize=(7, 4))
    width = 0.35
    x = range(len(modes))
    ax.bar([i - width / 2 for i in x], memory_hits, width, label="memory hits", color="#4f9d69")
    ax.bar([i + width / 2 for i in x], repair_attempts, width, label="repair attempts", color="#d8a95f")
    ax.set_xticks(list(x))
    ax.set_xticklabels(modes)
    ax.set_title("Memory Hits and Repair Attempts", fontweight="bold")
    ax.set_ylabel("Count")
    ax.legend()
    ax.spines[["top", "right"]].set_visible(False)
    fig.tight_layout()
    fig.savefig(CHARTS / "metrics.png", dpi=160)
    plt.close(fig)


def main():
    chart_pass_rate()
    chart_duration()
    chart_metrics()
    print("charts generated")


if __name__ == "__main__":
    main()
