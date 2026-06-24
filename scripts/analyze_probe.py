#!/usr/bin/env python3
"""分析 probe_sources.sh 的 TSV 输出，报告三源命中率、独占差异、ETag 一致性。

用法:
    bash scripts/probe_sources.sh vids.txt | python3 scripts/analyze_probe.py

重点指标：
- 命中差异：某源 200 而其它非 200（排除 000 超时噪声）→ 三源覆盖范围是否等价
- ETag 一致性：三源都 200 时 ETag 是否相同 → 共享数据是否同一份
- 源间独占集合：s3 / edge.ivideo / edge.v.iask 各自独有的 (vid,ext)
"""
import sys
from collections import defaultdict, Counter

HOSTS = [
    "s3.ivideo.sina.com.cn",
    "sinacloud.net/edge.v.iask.com",
    "sinacloud.net/edge.ivideo.sina.com.cn",
]
SHORT = {
    "s3.ivideo.sina.com.cn": "s3.ivideo",
    "sinacloud.net/edge.v.iask.com": "edge.v.iask",
    "sinacloud.net/edge.ivideo.sina.com.cn": "edge.ivideo",
}

by_key = defaultdict(dict)  # (vid,ext) -> {host: (status,etag)}
for line in sys.stdin:
    line = line.rstrip("\n")
    if not line or "\t" not in line:
        continue
    parts = line.split("\t")
    if len(parts) != 5:
        continue
    vid, host, ext, status, etag = parts
    by_key[(vid, ext)][host] = (status, etag)

n_total = len(by_key)
n000 = sum(
    1
    for m in by_key.values()
    for h in HOSTS
    if m.get(h, ("000",))[0] == "000"
)
print(f"样本 (vid,ext) 组合: {n_total}，其中超时(000): {n000}")

# 命中率
hit = Counter()
for m in by_key.values():
    for h in HOSTS:
        if m.get(h, ("000",))[0] == "200":
            hit[h] += 1
print("\n=== 命中率（status==200 数）===")
for h in HOSTS:
    print(f"  {SHORT[h]:12s}: {hit[h]}/{n_total}")

# 命中差异（排除任一源 000）
print("\n=== 命中差异（排除000，某源200而其它非200）===")
diff = 0
for (vid, ext), m in by_key.items():
    st = {h: m.get(h, ("000",))[0] for h in HOSTS}
    if any(st[h] == "000" for h in HOSTS):
        continue
    two00 = [h for h in HOSTS if st[h] == "200"]
    if 0 < len(two00) < 3:
        non200 = [(SHORT[h], st[h]) for h in HOSTS if st[h] != "200"]
        print(f"  {vid}.{ext}: {[SHORT[h] for h in two00]}=200  {non200}")
        diff += 1
if diff == 0:
    print("  无差异（任一组合要么三源都200，要么三源都非200）")

# ETag 一致性
allsame = etagdiff = 0
examples = []
for m in by_key.values():
    if all(m.get(h, ("000",))[0] == "200" for h in HOSTS):
        etags = {h: m[h][1] for h in HOSTS}
        if len(set(etags.values())) == 1:
            allsame += 1
        else:
            etagdiff += 1
            if len(examples) < 5:
                examples.append(etags)
print(f"\n=== 三源都200: {allsame+etagdiff} | ETag一致: {allsame} | 不一致: {etagdiff} ===")
for e in examples:
    print(f"  {e}")

# 结论
equiv = diff == 0
print(f"\n=== 结论（排除000噪声后）：三源覆盖范围是否完全等价 = {equiv} ===")
if not equiv:
    print("  → 必须三个源都探测，否则漏独占文件（见上面命中差异）")
