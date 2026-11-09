#!/usr/bin/env python3
"""Render the command evidence without changing any result state."""

import csv
import json
import sys
from pathlib import Path


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: report.py RESULTS_TSV OUTPUT_PREFIX", file=sys.stderr)
        return 2

    source = Path(sys.argv[1])
    prefix = Path(sys.argv[2])
    rows = []
    with source.open(newline="", encoding="utf-8") as handle:
        for row in csv.DictReader(handle, delimiter="\t"):
            rows.append(row)

    blocking = [
        row
        for row in rows
        if row["required"] == "yes" and row["status"] != "pass"
    ]
    report = {
        "work_package": "WP-16",
        "gate": "PASS" if not blocking else "BLOCKED",
        "cases": rows,
        "blocking_cases": [row["case"] for row in blocking],
    }
    prefix.with_suffix(".json").write_text(
        json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )

    lines = [
        "# WP-16 集成与发布验收证据",
        "",
        f"- 发布门：**{report['gate']}**",
        "- `pass` 只表示对应命令真实返回成功；`not-run`、`blocked` 和 `fail` 不得折算为通过。",
        "",
        "| Case | Required | Status | Command | Evidence |",
        "| --- | --- | --- | --- | --- |",
    ]
    for row in rows:
        lines.append(
            f"| `{row['case']}` | {row['required']} | **{row['status']}** | "
            f"`{row['command']}` | `{row['log']}` |"
        )
    lines.extend(
        [
            "",
            "## Gate semantics",
            "",
            "任何 required case 不是 `pass` 都会阻断发布；真实 Runtime、真实桌面链路、",
            "未验证平台和生产恢复不能用单元测试或 demo 结果替代。",
            "",
        ]
    )
    prefix.with_suffix(".md").write_text("\n".join(lines), encoding="utf-8")
    print(f"WP-16 report: {prefix.with_suffix('.md')}")
    print(f"WP-16 gate: {report['gate']}")
    return 0 if not blocking else 1


if __name__ == "__main__":
    raise SystemExit(main())
