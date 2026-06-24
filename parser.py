import csv
import json

vid_ok = 0
vid_empty = 0
vid_baddigit = 0


def fix_vid(vid):
    fixed_vid = ""
    for char in vid:
        if not char.isdigit():
            continue
        fixed_vid += char
    return fixed_vid.lstrip("0")


vids = []

records: dict[int, dict] = {}

with open("cid_info_sina.csv", newline="") as csvfile:
    reader = csv.DictReader(csvfile)
    for row in reader:
        record = {"mid": row["mid"], "title": row["title"], "author": row["author"]}

        vid = row["vid"]
        if not vid:
            vid_empty += 1
            record["vid"] = None
        elif fixed := fix_vid(vid):
            vid_ok += 1
            vids.append(fixed)
            record["vid"] = fixed
        else:
            vid_baddigit += 1
            print("fixed", row, "====>", vid)
            record["vid"] = None
        if record and record["vid"] not in records:
            records[record["vid"]] = record

print(f"vid_ok: {vid_ok}, vid_empty: {vid_empty}, vid_baddigit: {vid_baddigit}")
dedup_asc = list(set(vids))
dedup_asc.sort(key=lambda x: int(x))
print(f"dedup: {len(dedup_asc)}")
with open("records.jsonl", "w") as f:
    for vid in dedup_asc:
        f.write(json.dumps(records[vid], ensure_ascii=False) + "\n")
