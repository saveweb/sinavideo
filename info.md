# 新浪视频源信息整理

## 一、基础参数

### 1.1 各种 ID 及说明

| 参数 | 数据类型 | 说明 | 作用 |
|------|---------|------|------|
| `vid` | int | 高清 FLV/HLV 视频 ID | 获取视频原档（全部时长 `<6min` 的视频、小部分时长 `>6min` 的早期视频） |
| `ipad_vid` | int | 低清 MP4 视频 ID | 获取视频低清版（大部分时长 `>6min` 的视频） |
| `video_id` | int | 新版新浪视频 ID | 获取视频标题、简介等关键信息 |
| `fileid` | int | 视频分片 ID | 见 1.2 详细说明 |

### 1.2 参数间的关系

**vid 和 fileid** —— `vid` 相当于所有 `fileid` 的根 ID：

- 时长 `<=6min` 时：原视频不会被分割，此时 `vid == fileid`
- 时长 `>6min` 时：原视频按 `6min/段` 被切分为多个分片直到无法切割为止，每个分片对应一个 `fileid`

```
vid
 | - fileid_1 (6min)
 | - fileid_2 (6min)
 | - fileid_3 (6min)
 ...
 | - fileid_n (剩余 <6min 时长)
```

**vid / ipad_vid / video_id**：

- `vid` 可转换为 `ipad_vid`（见 3.3）
- `vid` 和 `video_id` 可互相转换（见 3.1、3.2）

## 二、CDN 下载链接

底层存储为新浪云自己的 OSS（类 S3 协议），前端套了阿里 CDN。

### 2.1 源站地址（推荐）

```
s3.ivideo.sina.com.cn/{vid}.[flv|hlv]
```

视频存在 flv、hlv 两种格式，一般是 flv；若 flv 404 可尝试 hlv，仍 404 只能尝试获取 mp4。

### 2.2 CDN 端点（阿里 CDN 缓存）

| 链接格式 | 别称 | 覆盖范围 |
|---------|------|---------|
| `http://cdn.sinacloud.net/edge.v.iask.com/{vid}.{ext}` | 爱问服务器 | 2007–2016（数据更全） |
| `http://cdn.sinacloud.net/edge.ivideo.sina.com.cn/{vid}.{ext}` | SQL 服务器 | 2016–至今 |

### 2.3 直连存储桶（去掉 `cdn.` 前缀）

| 链接格式 | 说明 |
|---------|------|
| `http://sinacloud.net/edge.v.iask.com/{vid}.{ext}` | 直连爱问存储桶 |
| `http://sinacloud.net/edge.ivideo.sina.com.cn/{vid}.{ext}` | 直连 SQL 存储桶 |

CDN 返回 404 时可尝试直连存储桶，部分文件在 CDN 上已清除但存储桶中仍存在。

修改域名前缀（如 `cdn.sinacloud.net` → `xxx.sinacloud.net`）会返回 `BucketNotFound`，说明存储桶名与域名绑定。

> `http://cdn.sinacloud.net/edge.v.iask.com//` 返回一张图片（key 为 `//`），可用于验证存储桶是否在线。

### 2.4 三个源站共享数据但覆盖范围不同（务必都探测）

`s3.ivideo.sina.com.cn`、`sinacloud.net/edge.ivideo.sina.com.cn`、`sinacloud.net/edge.v.iask.com` 三个域名**共享部分数据**，但**不是同一份完整副本**——三者覆盖范围是部分重叠的并集。

- **共享数据是同一份**：当同一 vid 在三源都命中时，ETag 完全一致（如 `12333.flv` 三源均为 `4f2df1ef995c566fa59d89a6a8701706`）。
- **但各自都有独占文件**。批量测试（72 个 vid × 3 源 × 3 ext，排除超时噪声）实测到的独占命中：

  | 文件 | s3.ivideo | edge.ivideo | edge.v.iask |
  |------|-----------|-------------|-------------|
  | `88058398.hlv` | ✅ | ❌404 | ❌404 |
  | `89852718.hlv` | ❌404 | ✅ | ❌404 |
  | `109605216.hlv` | ❌404 | ❌404 | ✅ |
  | `5553654.flv`  | ✅ | ✅ | ❌404 |
  | `94186029.hlv` | ✅ | ❌404 | ✅ |

  三个入口各自都存在"只有自己命中、另两个 404"的文件，说明它们背后是不同的存储/缓存层。

**结论：存档时三个源站都要探测**，否则会漏掉独占文件。当前代码（`cmd/archive/cdn.go`）已把三者都加入 `sourceServers`，按 `s3.ivideo → edge.v.iask → edge.ivideo` 顺序探测，命中即停（同一 vid 不会重复下载）。

> 注：探测时需注意并发与超时——单次小样本（4 个 vid）会因全落在三源交集而误判为"同一后端"；低并发 + 足够样本量才能暴露独占差异。

### 2.5 三个存储桶的元数据

底层是三个独立的 OSS 桶（Owner 均为 `SINA00000000000VIASK`）：

| 桶（Project） | PoolName | 对象数 | 容量 | Last-Modified | 备注 |
|---------------|----------|--------|------|---------------|------|
| `edge.v.iask.com` | plVideo | 0 | 0 | 2012-04-18 | 老数据，对象已清空（CDN 缓存可能仍有残文件） |
| `edge.ivideo.sina.com.cn` | — | — | — | — | 拒绝读 meta（`no read acl`），但能 GET 文件 |
| `s3.ivideo.sina.com.cn` | S3Trans | 527,662,202 | ~5.03 PB | 2012-02-13 | 当前最大的活桶 |

查询命令：`curl "http://sinacloud.net/<bucket>/?meta&formatter=json"`

### 2.6 文件格式

- 扩展名按优先级尝试：`.mp4` → `.flv` → `.hlv`
- **2010 年 12 月之后**的 flv 视频以 **hlv** 格式保存，需将后缀名改为 `.hlv`
- 据称新浪只在 SQL 服务器上删除视频，爱问服务器数据更全，但未经证实，实际应两端点都尝试

### 2.7 ipad_vid 低清 MP4（分段视频兜底）

对于时长 `>=6min` 的分段视频，`vid` 拿不到原档时，可通过 `ipad_vid` 获取整段的低清 MP4：

```
s3.ivideo.sina.com.cn/{ipad_vid}.mp4
```

ipad_vid 与主档是**不同质量版本**（主档高清分段 vs ipad 低清整段），存档角度两者都应保留。

## 三、API 接口

### 3.1 通过 vid 获取 video_id

```
https://s.video.sina.com.cn/video/getvideoidbyvid?vid={vid}
```

返回示例：

```json
{"code": 1, "message": "OK", "data": {"video_id": "209238722"}}
```

- `code` 为 1 表示存在，为 0 表示不存在
- 分段 VID 在此 API 中通常显示为已删除（`code` 为 0），但仍可从 CDN 下载

### 3.2 通过 video_id 获取视频信息

**未失效视频**：

```
http://api.ivideo.sina.com.cn/public/video/play?appname=sinaplayer_pc&tags=sinaplayer_pc&applt=web&appver=V11220.210521.03&player=all&video_id={video_id}
```

返回示例：

```json
{
  "code": 1,
  "data": {
    "title": "tako100",
    "create_time": "1293851635",
    "length": "760500",
    "image": "https://p3.ivideo.sina.com.cn/video/209/238/722/209238722.jpg",
    "videos": [
      {"file_id": "44423596", "type": "flv", "size": "22450598"},
      {"file_id": "44377997", "type": "mp4", "size": "22450598"}
    ]
  }
}
```

- `code` 为 1 时返回完整视频信息
- `videos` 数组包含该视频的所有格式（flv/mp4），每个格式对应一个 file_id
- **分段视频的分段信息也包含在 `videos` 中**，通过不同的 `file_id` 区分
- `length` 单位为毫秒，`create_time` 为 Unix 时间戳

**其他等价 API**：

```
http://s.video.sina.com.cn/video/play?video_id={video_id}
http://s.video.sina.com.cn/video/h5play?video_id={video_id}
```

**2011 年及之后的失效视频**（play API 返回「视频已删除」时）：

```
https://interface.sina.cn/video/wap/videoinfo.d.json?vid={vid}
```

**新浪视频播放页**：

```
http://video.sina.com.cn/view/{video_id}.html
```

### 3.3 通过 vid 获取 ipad_vid（低清 MP4）

```
http://video.sina.com.cn/interface/video_ids/video_ids.php?v={vid}
```

返回示例：

```json
{"vid": 84626234, "ipad_vid": "84626460"}    // 有低清版
{"vid": 12333, "ipad_vid": false}             // 未转码低清版
```

- `ipad_vid` 是**混合类型**：有低清 MP4 时是字符串，否则为 JSON `false`
- 若 `ipad_vid` 为 `false`，说明该视频当初未转码为低清 MP4
- 若返回的 `ipad_vid` 等于 `vid` 本身，则无需重复下载
- 拿到 ipad_vid 后用 `s3.ivideo.sina.com.cn/{ipad_vid}.mp4` 下载低清整段

> 仅部分视频才有整段低清版 MP4。

### 3.4 play/url API（直接获取视频/链接）

```
http://api.ivideo.sina.com.cn/public/video/play/url?appname=web&appver=web&applt=web&tags=popview&direct=1&vid={vid}
```

- `direct=1`：直接返回视频流
- `direct=0`：返回视频链接（JSON），不直接吐视频
- 可用 vid 或 ipad_vid 作为参数

> 感谢 UID:23550787 及其朋友找出的 API。

## 四、从 B 站 av 号查找 VID

### 方法 1：通过 CSV 数据库查表

项目中的 `cid_info_sina.csv` 包含约 95 万条 B 站 CID → 新浪 VID 的映射：

```
cid, aid, page, title, subtitle, mid, author, cover, type, vid, duration
```

通过 `aid`（即 av 号）即可查到对应的 `vid`。

### 方法 2：通过相邻 CID 推算

适用于 **2010 年 3 月至 2012 年 10 月 17 日**间删除的视频（BP 中 CID 数据库无数据、但主站可卡出播放记录/稍后再看的视频）。

步骤：

1. 查询目标视频相邻的两个视频的 CID（如 av5823 → 查 av5822 和 av5824）
2. 取中间值即为目标 CID（如 8624 和 8626 之间 → 8625）
3. 使用 CID 反查功能输入 CID，即可得到对应的 VID

## 五、分段视频

### 5.1 真分段视频（原视频时长 ≥ 6 分钟）

由于部分视频过大，新浪会将原视频分段储存：

- **主 VID**：储存原视频数据的位置，无法直接获取原视频，只能获取所有分段后手动合成
- **分段 VID（fileid）**：储存分段时另外创建的 VID，在 API 的 `videos` 数组中以不同 `file_id` 返回

2009 年 5 月 1 日之后，新浪以 **6 分钟为一段**切割视频。主 VID 与分段 VID 之间会相差一段距离（受投稿速度和处理顺序影响）。

**搜索范围建议（将主 VID 设为 x）：**

| 时间段 | 搜索区间 |
|--------|---------|
| 2009、2010 年 | x-5000 ~ x+15000 |
| 2011、2012 年及以后 | x ~ x+10000 |

> 以上范围仅供参考，若多次搜索无果请考虑扩大范围。

### 5.2 伪分段视频（通常时长 ≤ 6 分钟）

明明时长小于 6 分钟，但实际储存位置不是查到的 VID。例如 av43001，记录的 VID 为 34856921，但实际位置在 34815563，相差 41358。

**搜索范围建议：** x-50000 ~ x+15000

### 5.3 主 VID 与分段 VID 的关系

- 主 VID 和伪分段 VID 看似无关，但它们的 **video_id** 有关联
- 一般情况下分段 VID 显示已删除，但可从 CDN 下载
- 主 video_id 和分段 video_id 很接近
- **方法**：通过反查 video_id 获取主 vid 的 video_id，然后在半径 60 内搜索已删除的视频，逐个下载排查

### 5.4 ipad_vid 兜底（推荐）

对于 `>=6min` 的分段视频，与其手动在搜索区间里逐个试相邻 VID，**更省事的做法是直接用 3.3 的 video_ids.php 把 vid 转成 ipad_vid，下载低清整段 MP4**。实测 ipad_vid 恰好就是早期靠相邻扫描找到的那个 VID（如 vid=84626234 → ipad_vid=84626460），说明「相邻扫描」本质上就是在找 ipad_vid，而 video_ids.php 能直接给出。

> 主档高清分段（hlv/flv）与 ipad 低清整段（mp4）是不同质量版本，存档角度两者都应保留。

## 六、批量搜索工具

- **存档工具**：`cmd/archive/` — 从 saveweb bittracker 领取 job（vid），自动查询 API、探测源站、下载视频和元数据，全部写入 WARC 后经 tus 上传。
  - **爬取策略（全源探测 + ETag 去重）**：三个源站覆盖范围是部分重叠的并集（2.4），因此对每个 id 在「**所有源 × 全扩展名**」上发 HEAD，收集全部 200 命中的候选；再用 **ETag 去重**——指向同一内容（ETag 相同）的多条 URL 只下载一次。这样在最大化召回率的同时避免重复字节/请求。
  - **id 集合**：主 vid + play API 返回的分段 file_id（主档高清，ext=mp4/flv/hlv）；再查 `video_ids.php` 补 ipad_vid（低清整段，ext=mp4）。主档与 ipad 的 ETag 必然不同（不同质量），不会被互相去重，两者都保留。
  - **探测顺序**：先打 `getvideoidbyvid` 拿 video_id，再打 play API 拿标题/file_id 列表，然后全源全 ext 探测候选并 ETag 去重后下载。
  - 响应体由 WARC 客户端在底层拦截落盘，业务层只需读完 body 触发记录完成。
- **源站覆盖率复测**：`scripts/probe_sources.sh` + `scripts/analyze_probe.py` — 分层抽样 vid，对三源全 ext 探测，报告命中率/独占差异/ETag 一致性。定期复测三源存活与覆盖率变化。
- **单 vid 探测诊断**：`cmd/probetest/` — 不依赖 WARC/tracker/tus 的 dry-run 工具，镜像 `cmd/archive` 的「全源全 ext 探测 → ETag 去重」逻辑，打印最终会下载哪些 URL、各自大小/ETag、去重节省了多少次，**不真正下载 body**。用于排查某个 vid 的命中情况和去重是否如预期。
  - 用法：`go run ./cmd/probetest/ <vid> [vid ...]`
  - 覆盖 case：主档+ipad 都在（ipad∈fileids）、仅 ipad 命中（主档全 404）、play API 失效兜底、ipad==false 跳过等。
- **IDM 下载器**：
  1. 使用通配符批量生成下载链接（如搜查 35080001–35089999）
  2. 设置通配符长度和升/降序
  3. IDM 最多同时开 1000 个任务
  4. 等待下载轮完 2 遍（防漏）后逐个检查视频

## 七、备注

- 新浪云产品现已无法创建和充值，存储桶处于只读/自然过期状态
- 没有发现 internal endpoint，所有访问均通过公网
- 优先以 B 站已有内容为主，新浪 CDN 作为补充源
- `s3.ivideo.sina.com.cn` 与 `sinacloud.net` 的 edge 桶**共享部分数据但覆盖范围不同**，各自有独占文件，存档时必须三个源站都探测（详见 2.4）
