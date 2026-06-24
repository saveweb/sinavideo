#!/usr/bin/env python3
"""从 records.jsonl 分层抽样 vid，覆盖 1~9 位数，用于源站覆盖率批量探测。

用法:
    python3 scripts/sample_vids.py [每数量级样本数] > vids.txt

默认每数量级 8 个（小数量级全取）。固定随机种子保证可复现。
"""
import json
import random
import sys


def main():
    per_bucket = 8
    if len(sys.argv) > 1:
        per_bucket = int(sys.argv[1])

    random.seed(42)
    by_len = {i: [] for i in range(1, 10)}
    with open("records.jsonl") as f:
        for line in f:
            v = json.loads(line).get("vid")
            if v and v.isdigit():
                n = int(v)
                if 1 <= n <= 10**9:
                    by_len[len(str(n))].append(n)

    out = []
    for d in range(1, 10):
        pool = by_len[d]
        out.extend(random.sample(pool, min(len(pool), per_bucket)))
    out.sort()
    for v in out:
        print(v)


if __name__ == "__main__":
    main()
