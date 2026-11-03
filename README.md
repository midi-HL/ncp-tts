# ncp-tts

一个用 Go 写的 **MCP（Model Context Protocol）服务**，把文字变成语音，走微软 Edge 的神经网络音色（晓晓、云希、云健等 300+ 音色）。

**不需要 API Key，不需要注册 Azure，不需要绑卡。**

---

## 这是什么

任何支持 MCP 的客户端（Claude Desktop、Cursor、DSH、自建 agent）都能直接调用它：

- `tts_speak` —— 给一段文字，拿回一个 MP3 文件（或内联 base64）
- `tts_list_voices` —— 查可用音色，可按 `zh-CN` 这类语言前缀过滤

服务通过 **stdio** 通信，不监听端口、不需要网络配置，客户端拉起进程即可用。

---

## 快速开始

### 1. 下载对应平台的可执行文件

从 Releases 拿你系统的那一个：

| 平台 | 文件名 |
|---|---|
| Windows 64 位 | `ncp-tts-windows-amd64.exe` |
| Windows 32 位 | `ncp-tts-windows-386.exe` |
| Linux x86-64 | `ncp-tts-linux-amd64` |
| Linux x86 32 位 | `ncp-tts-linux-386` |
| Linux ARM64（树莓派 4/5、多数云主机） | `ncp-tts-linux-arm64` |
| Linux ARM 32 位（老树莓派、路由器） | `ncp-tts-linux-arm` |

单个文件，无依赖，不用装 Go、不用装 Python。

### 2. 先自检（确认网络通）

```bash
./ncp-tts-linux-amd64 --selftest
```

正常会打印：

```
voices=322 audio_bytes=14112
selftest ok
```

`voices` 是音色总数，`audio_bytes` 是实际拿到的音频字节数。**这一步失败说明是网络问题，不是配置问题**，先排查能不能访问 `speech.platform.bing.com`。

### 3. 接到你的客户端

**Claude Desktop**（`claude_desktop_config.json`）：

```json
{
  "mcpServers": {
    "ncp-tts": {
      "command": "/绝对路径/ncp-tts-linux-amd64",
      "args": ["--out", "/你想存音频的目录"]
    }
  }
}
```

**Cursor**（`~/.cursor/mcp.json`）格式相同。

Windows 上 `command` 写 `.exe` 的绝对路径，路径用双反斜杠或正斜杠。

`--out` 不给的话默认写系统临时目录。

---

## 工具参数

### `tts_speak`

| 参数 | 必填 | 说明 |
|---|---|---|
| `text` | ✅ | 要读的文字 |
| `voice` | | 音色短名，如 `zh-CN-XiaoxiaoNeural`。默认 `en-US-EmmaMultilingualNeural` |
| `rate` | | 语速增量，百分比，如 `+10%`、`-20%` |
| `volume` | | 音量增量，百分比 |
| `pitch` | | 音高增量，赫兹，如 `-20Hz` |
| `output` | | MP3 的绝对路径。不给则自动生成到 `--out` 目录 |
| `inline` | | `true` 则返回 base64 而不是写文件 |

### `tts_list_voices`

| 参数 | 说明 |
|---|---|
| `locale` | 语言前缀过滤，如 `zh-CN` |
| `contains` | 音色名子串（不区分大小写） |

中文常用音色：

| 短名 | 性别 | 特点 |
|---|---|---|
| `zh-CN-XiaoxiaoNeural` | 女 | 晓晓，最常用，自然 |
| `zh-CN-XiaoyiNeural` | 女 | 晓伊，偏年轻 |
| `zh-CN-YunxiNeural` | 男 | 云希，抖音小说解说常用 |
| `zh-CN-YunjianNeural` | 男 | 云健，稳重 |
| `zh-CN-YunyangNeural` | 男 | 云扬，新闻播报 |
| `zh-CN-liaoning-XiaobeiNeural` | 女 | 东北话 |
| `zh-CN-shaanxi-XiaoniNeural` | 女 | 陕西话 |

---

## 自己编译

```bash
git clone https://github.com/midi-HL/ncp-tts.git
cd ncp-tts
go build -o ncp-tts ./cmd/ncp-tts
```

需要 Go 1.24+。纯 Go，`CGO_ENABLED=0`，交叉编译不需要任何额外工具链：

```bash
GOOS=windows GOARCH=386 go build -o ncp-tts.exe ./cmd/ncp-tts
```

推送 `v*` 标签会触发 GitHub Actions 编译全部 6 个平台并附到 Release。

---

## 实现说明（给要改代码的人）

### 令牌算法是这套东西的核心难点

微软的 Read Aloud 接口要一个 `Sec-MS-GEC` 令牌。**老办法（时间戳拼盐做标准 SHA256）现在一律返回 403**，我实测验证过：

| 令牌 | 结果 |
|---|---|
| 标准 SHA256 | 403 |
| 空令牌 | 403 |
| 当前 v2 方案 | 成功 |

v2 令牌形如 `<16位hex>-<64位hex>`，两半都来自一套**故意混淆过的压缩函数**，不是标准 SHA-256。`internal/drm` 是逐位移植，`vectors_test.go` 里 24 组向量（10 组哈希 + 14 组令牌）是从参考实现导出的，改动那堆常数前先跑测试。

几个容易踩的点：

- `sha256State` 的初始状态、轮常数 14、两个 sigma 调整值都**故意偏离 FIPS 180-4**
- CS64 那部分用 14 组系数 + 双通道置换累加，溢出必须按 32 位截断（Go 里靠 `uint32` 自然回绕）
- 时间戳先加 Windows 纪元偏移，再按 300 秒取整，最后乘 1e7

### 其他实现细节

- **二进制帧的头部不是靠空行分隔的**，而是开头 2 字节给出大端长度。按 `\r\n\r\n` 找会一个音频字节都拿不到，这是最容易写错的地方
- **分块要按字节而不是字符**，上限 4096 字节，且不能把一个多字节字符或 `&amp;` 这种实体切开
- **时间轴偏移会跨块漂移**，用已产出的音频字节数按 48kbps 定码率重算，比信服务返回的 offset 准
- **403 时**用响应头的 `Date` 校正本地时钟偏差再重试一次
- **stdout 只能输出 JSON-RPC**，所有日志走 stderr，否则客户端解析会崩

---

## 已知限制

- 这是微软 Edge 浏览器的**内部接口**，不是官方 API。微软随时可能改协议，届时需要跟进更新
- **没有 SLA**，高频请求可能被临时限流（403 / 429）
- 仅供**个人学习、个人自动化、小流量非商业**用途。企业产品请用 Azure 官方 Speech 服务
- 依赖本地时钟，时钟偏差过大会 403（会自动校正一次）

---

## 许可

MIT
