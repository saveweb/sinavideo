#!/usr/bin/env bash
# 对一批 vid 在三个源站上探测全部扩展名，输出 TSV 供 analyze_probe.py 消费。
#
# 三个源站共享部分数据但覆盖范围不同（info.md 2.4），此脚本用于复测三源存活率
# 与独占文件分布。建议低并发（-P 6）+ 长超时，避免偶发超时(000)误判为内容差异。
#
# 用法:
#   bash scripts/probe_sources.sh vids.txt | python3 scripts/analyze_probe.py
#   bash scripts/probe_sources.sh              # 无参数则自动从 records.jsonl 采样
#
# 输出格式: vid<TAB>host<TAB>ext<TAB>status<TAB>etag
set -euo pipefail
cd "$(dirname "$0")/.."

HOSTS=(
  "s3.ivideo.sina.com.cn"
  "sinacloud.net/edge.v.iask.com"
  "sinacloud.net/edge.ivideo.sina.com.cn"
)
EXTS=(flv mp4 hlv)
CONCURRENCY=6
TIMEOUT=20

VIDS_FILE="${1:-}"
if [ -z "$VIDS_FILE" ]; then
  echo "no vids file given, sampling from records.jsonl..." >&2
  VIDS_FILE="$(mktemp)"
  python3 scripts/sample_vids.py > "$VIDS_FILE"
  trap 'rm -f "$VIDS_FILE"' EXIT
fi

# 单个 vid 的探测逻辑抽成独立脚本，便于 xargs -P 在子进程中调用。
WORKER="$(mktemp)"
trap 'rm -f "$WORKER"' EXIT
cat > "$WORKER" <<EOF
#!/usr/bin/env bash
vid="\$1"
for ext in flv mp4 hlv; do
  for host in \\
    "s3.ivideo.sina.com.cn" \\
    "sinacloud.net/edge.v.iask.com" \\
    "sinacloud.net/edge.ivideo.sina.com.cn"; do
    out=\$(curl -s -o /dev/null -w "%{http_code}\t%{etag}" \\
      -I --max-time ${TIMEOUT} "http://\${host}/\${vid}.\${ext}" 2>/dev/null || true)
    code="\${out%%\$'\\t'*}"
    etag="\${out#*\$'\\t'}"
    [ -z "\$code" ] && code="000"
    [ -z "\$etag" ] && etag="-"
    printf '%s\t%s\t%s\t%s\t%s\n' "\$vid" "\$host" "\$ext" "\$code" "\$etag"
  done
done
EOF
chmod +x "$WORKER"

xargs -P "$CONCURRENCY" -I {} "$WORKER" {} < "$VIDS_FILE"
