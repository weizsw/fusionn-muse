# mdc-ng 刮削、115strm 网盘串流与 Emby 媒体库联动指南

本文档详细介绍 **Fusionn-Muse** 处理完成后的下游联动体系：如何配合 **mdc-ng** 自动化元数据刮削、**115strm** 网盘秒级上传与软链接生成，以及 **Emby** 媒体服务器搭建，实现**“本地刮削 + 本地中文字幕 + 云端原画秒开”**的极致影视中心体验。

---

## 一、端到端数据流动与协作架构

```text
[qBittorrent]
      │ 下载完成（例如分类 whisparr）
      ▼ 触发 torrent_complete.py (Webhook POST)
[Fusionn-Muse]
      │ 广告过滤、代码识别、语音转录、LLM中文字幕翻译
      ▼ 输出视频文件与 .srt 字幕
/Volumes/External/automation/scraping
      │
      ▼ [mdc-ng] 监控此目录（采用兼容轮询模式，避免跨卷通知丢失）
      ├─ 抓取元数据（NFO/海报/剧照/演职员），写入 ──► /Volumes/External/strm/adults/ (Emby库)
      ├─ 提取/同步中文字幕，归档至 ──────────────► /Volumes/External/automation/subtitles/
      └─ 整理视频文件并重命名，移动至 ────────────► /Volumes/External/automation/finished/
                                                       │
                                                       ▼ [115strm 插件]
                                                    上传视频至 115 网盘
                                                    在 /Volumes/External/strm/adults/ 生成 .strm
                                                    删除本地 finished 内的大视频文件（释放磁盘）
                                                       │
                                                       ▼ [Emby 媒体服务器]
                                                    读取本地 NFO/海报 + 本地中文字幕
                                                    点击播放直接通过 .strm 秒开云端原画！
```

---

## 二、跨服务路径映射矩阵

为了确保所有容器与宿主机对同一个目录的认识保持一致，请参考以下映射关系：

| 目录功能 | 宿主机物理路径示例 | Muse 容器内路径 | mdc-ng 容器内路径 | Emby 容器内路径 |
|---------|------------------|----------------|------------------|----------------|
| **媒体存储根目录** | `/Volumes/External` | `/data` | `/external` | `/data`（按需映射） |
| **qB 下载目录** | `/Volumes/External/torrents` | `/data/torrents` | - | - |
| **Muse 产物输出目录** | `/Volumes/External/automation/scraping` | `/data/automation/scraping` | `/external/automation/scraping` | - |
| **mdc-ng 整理归档目录** | `/Volumes/External/automation/finished` | `/data/automation/finished` | `/external/automation/finished` | - |
| **中文字幕库** | `/Volumes/External/automation/subtitles` | `/data/automation/subtitles` | `/external/automation/subtitles` | - |
| **Emby 媒体库目录** | `/Volumes/External/strm/adults` | - | `/external/strm/adults` | `/data/adults` |

---

## 三、部署 mdc-ng 刮削服务

### 1. Docker Compose 配置

创建或添加到你的 `docker-compose.yml`：

```yaml
services:
  mdcng:
    image: mdcng/mdc:latest
    container_name: mdcng
    restart: unless-stopped
    environment:
      - PUID=501
      - PGID=20
      - TZ=Asia/Shanghai
    volumes:
      # 配置与数据目录
      - ./mdcng-data:/config
      # 将宿主机媒体根目录挂载至容器内 /external
      - /Volumes/External:/external
    ports:
      - "9208:9208"
```

### 2. mdc-ng Web 界面核心路径配置

启动容器后，浏览器访问 `http://<宿主机IP>:9208`，进入**“基础设置”**或**“路径设置”**：

1. **监控目录 (Watch Directory)**：
   - 路径填写：`/external/automation/scraping`
   - **关键设置：监控模式务必选择“兼容监控模式” (watcher_kind: 1)**
     > **为什么必须选兼容监控模式？**
     > 在 Docker 卷绑定挂载（Bind Mount）环境下，容器内的 Linux inotify 事件经常无法捕捉宿主机或其他容器的文件移动/写入事件。“兼容监控模式”采用定时轮询（Polling）扫描机制，能够 100% 可靠地检测到 Muse 新输出到 `scraping` 目录的文件。
2. **整理目录 (Target Folder)**：
   - 路径填写：`/external/automation/finished`
   - 说明：mdc-ng 刮削完成后，会将处理完毕的原始大视频重命名并安全移动到此目录，等待 115strm 上传。
3. **元数据目录 (Metadata Folder)**：
   - 路径填写：`/external/strm/adults`
   - 说明：mdc-ng 生成的 `.nfo` 影片详情、`-poster.jpg` 封面海报、`-fanart.jpg` 剧照背景图等直接写入此处。该目录直接作为本地 Emby 媒体库的物理存储！
4. **字幕库目录 (Subtitles Folder)**：
   - 路径填写：`/external/automation/subtitles`
   - 说明：归档管理提取出来的字幕文件，供播放器或刮削器联动读取。

---

## 四、115strm 云存储与流媒体软链接协同

### 1. 115strm 运行机制

在许多家庭影院（Homelab）实践中，由于视频文件体量巨大（单个视频 5GB~20GB），将全部视频长期保存在本地硬盘成本极高。**115strm** 方案完美解决了这一痛点：

1. **上传视频**：115strm 监听或定期扫描 `/Volumes/External/automation/finished`。
2. **网盘归档**：将该目录下的视频上传到你的 115 网盘指定目录。
3. **生成 .strm 文件**：上传成功后，115strm 会在对应的元数据目录 `/Volumes/External/strm/adults/<演员>/<番号>/<番号>.strm` 生成一个只有几百字节的文本文件，内容为直连 115 网盘的播放链接（或 alist / cd2 挂载路径）。
4. **清理本地视频**：生成 `.strm` 后，自动删除 `finished` 下的本地大视频文件。

### 2. 目录生成结果示例

处理完成后，在 `/Volumes/External/strm/adults/SNOS-411/` 目录下将呈现如下文件：

```text
/Volumes/External/strm/adults/SNOS-411/
├── SNOS-411.strm          # 115strm 生成的网盘串流文件（几十字节）
├── SNOS-411.nfo           # mdc-ng 生成的电影信息与演职员详情
├── SNOS-411-poster.jpg    # mdc-ng 生成的高清封面海报
├── SNOS-411-fanart.jpg    # mdc-ng 生成的背景剧照
└── SNOS-411.zh-CN.srt     # Muse 生成并由 mdc-ng 复制的高质量中文字幕
```

---

## 五、Emby 媒体服务器配置

### 1. Emby Docker Compose 配置示例

```yaml
services:
  emby:
    image: lscr.io/linuxserver/emby:latest
    container_name: emby
    restart: unless-stopped
    environment:
      - PUID=501
      - PGID=20
      - TZ=Asia/Shanghai
    volumes:
      - ./emby-config:/config
      # 映射元数据与 strm 目录到 Emby 容器内
      - /Volumes/External/strm/adults:/data/adults
    ports:
      - "8096:8096"
```

### 2. Emby 媒体库设置

1. 登录 Emby 管理后台（`http://<IP>:8096`），进入**设置 → 媒体库**。
2. 点击**添加媒体库**：
   - 内容类型：选择 **电影** 或 **其他**。
   - 显示名称：例如 `Adults`。
   - 文件夹：添加路径 `/data/adults`。
3. **重要配置项**：
   - **元数据下载器**：全部取消勾选（无需让 Emby 重复从互联网刮削，mdc-ng 已经提供了完美的本地 NFO 和海报）。
   - **图像提取器**：取消勾选屏幕截图提取（因为 `.strm` 是云端文件，本地截图会消耗不必要的网盘流量）。
   - **字幕下载器**：取消勾选（Muse 已经生成了精准匹配口语的专业中文字幕）。
4. 保存并刷新媒体库：
   - Emby 即可瞬间呈现精美的海报墙、标签分类与简介。
   - 点击播放时，Emby 直接读取本地中文字幕，视频由客户端或服务端经由 `.strm` 直连网盘原画极速串流！
