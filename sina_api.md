:::: {.github-markdown-body color-mode="auto" light-theme="light" dark-theme="dark"}
::: github-markdown-content
## Sina API 信息

### 参数说明

#### 各种 ID 参数及说明

  -------------------------------------------------------------------------------------
  参数              数据类型          说明              作用
  ----------------- ----------------- ----------------- -------------------------------
  `vid`             int               高清 FLV/HLV 视频 获取视频原档\
                                      ID                （全部时长`<6min`的视频\
                                                        小部分时长`>6min`的早期视频）

  `ipad_vid`        int               低清 MP4 视频 ID  获取视频低清版\
                                                        （大部分时长`>6min`的视频）

  `video_id`        int               新版新浪视频 ID   获取视频标题、简介等关键信息

  `fileid`          int               视频分片 ID       见下文详细说明
  -------------------------------------------------------------------------------------

#### 各种参数间的关系

1.  `vid`和`fileid`

`vid`相当于所有`fileid`的根 ID

- 当视频时长`<=6min`时：

原视频不会被分割，此时`vid`就是`fileid`

    vid = fileid

- 当视频时长`>6min`时：

原视频按照`6min/段`被切分为多个分片直到无法切割为止，每个分片对应一个`fileid`

    vid
     | - fileid_1 (6min)
     | - fileid_2 (6min)
     | - fileid_3 (6min)
     | ...
     | - fileid_n (剩余<6min时长)

2.  `vid`和`ipad_vid` `video_id`

- `vid`可转换为`ipad_vid`

- `vid`和`video_id`可互相转换

------------------------------------------------------------------------

### API 列表

#### 获取视频文件

1.  对于没有分段的视频（时长`≤6min`），可以获取原档

以下是源站地址（推荐使用）：

    s3.ivideo.sina.com.cn/{vid}.[flv|hlv]

\*视频存在 flv、hlv 两种格式，一般是 flv，如果获取 flv 404 可以尝试
hlv，还是 404 只能尝试获取 mp4

其他视频地址

- 以下是 CDN 地址：

  时间        地址
  ----------- --------------------------------------------------------------------
  2007-2016   `http://cdn.sinacloud.net/edge.v.iask.com/{vid}.[flv|hlv]`
  2016-至今   `http://cdn.sinacloud.net/edge.ivideo.sina.com.cn/{vid}.[flv|hlv]`

- 以下是 API 地址：

<!-- -->

    http://api.ivideo.sina.com.cn/public/video/play/url?appname=web&appver=web&applt=web&tags=popview&direct=1&vid={vid}

\* 注：`direct`参数可设为`0`，这样获得的是视频链接而不是直接获取视频

2.  对于有分段的视频（时长`≥6min`），只能获取低清版

一般通过此接口获取到的视频都是整段的低清版 MP4 视频，所以能获取到高清
FLV/HLV
或互联网上能找到相应资源的时候不推荐使用此接口（除非找不到此资源）

- 1.使用`vid`获取对应的`ipad_vid`

<!-- -->

    http://video.sina.com.cn/interface/video_ids/video_ids.php?v={vid}

\* 注：仅部分视频才有整段的低清版
MP4，若返回内容中`ipad_vid`为`false`，则视频在当初没有转码为
MP4，若输入的`ipad_vid`为本身则返回本身

- 2.使用`ipad_vid`获取整段的低清 MP4 视频

推荐使用这个：

    s3.ivideo.sina.com.cn/{ipad_vid}.mp4

也可以使用这个：

    http://api.ivideo.sina.com.cn/public/video/play/url?appname=web&appver=web&applt=web&tags=popview&direct=1&vid={ipad_vid}

------------------------------------------------------------------------

#### 获取视频信息

1.  通过`vid`获取对应的`video_id`

<!-- -->

    https://s.video.sina.com.cn/video/getvideoidbyvid?vid={vid}

2.  通过`video_id`获取视频信息

未失效视频：

    http://api.ivideo.sina.com.cn/public/video/play?appname=sinaplayer_pc&tags=sinaplayer_pc&applt=web&appver=V11220.210521.03&player=all&video_id={video_id}

其他API

    http://s.video.sina.com.cn/video/play?video_id={video_id}
    http://s.video.sina.com.cn/video/h5play?video_id={video_id}

2011 年和之后的失效视频：

    https://interface.sina.cn/video/wap/videoinfo.d.json?vid={vid}

新浪视频播放页：

    http://video.sina.com.cn/view/{video_id}.html

------------------------------------------------------------------------

### 参考链接

1.  [新浪查询视频信息
    api](https://www.bilibili.com/opus/1119385863103447045/?from=readlist)
2.  [新浪视频可使用的下载链接](https://www.bilibili.com/opus/1088260084828471302/?from=readlist)
3.  [sina 新解析链接（只能解析 2011
    年的）](https://www.bilibili.com/opus/1093110318360428544/?from=readlist)
4.  [\[原创教程\]目前暂可用的新浪视频获取方式（2024）](https://www.bilibili.com/opus/907071323153367043)
:::
::::
