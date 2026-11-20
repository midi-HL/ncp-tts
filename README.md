# ncp-tts

一个用 Go 写的文字转语音服务，使用微软 Edge 的神经网络音色（晓晓、云希、云健等 300+ 音色），同时支持 **MCP** 和 **OpenAI 兼容的 HTTP API**。

**不需要 API Key，不需要注册 Azure，不需要绑卡。**

---

## 这是什么

任何支持 MCP 的客户端（Claude Desktop、Cursor、DSH、自建 agent）都能直接调用它：

- `tts_speak` —— 给一段文字，拿回一个 MP3 文件（或内联 base64）
- `tts_list_voices` —— 查可用音色，可按 `zh-CN` 这类语言前缀过滤

默认通过 **stdio** 提供 MCP 服务。需要让局域网内的设备调用时，用 `--listen` 启动 HTTP 模式。

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

下载的可执行文件可直接输出 MP3，不用装 Go 或 Python。HTTP 接口输出其他格式时需要 FFmpeg。

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

## 在局域网使用 OpenAI 兼容 API

在 Windows 上用 PowerShell 启动：

```powershell
.\ncp-tts-windows-amd64.exe --listen 0.0.0.0:8000
```

在 Linux 上启动：

```bash
./ncp-tts-linux-amd64 --listen 0.0.0.0:8000
```

客户端的 Base URL 填 `http://<运行服务的电脑的局域网 IP>:8000/v1`，同一台电脑也可以用 `http://127.0.0.1:8000/v1`。HTTP 接口不检查 API Key：可以省略 `Authorization`，客户端要求填 Key 时随便填一个非空值即可。只想在本机使用时，可监听 `127.0.0.1:8000`。

`--listen` 启动的是独立的 HTTP 服务。若同时需要 MCP，由 MCP 客户端另起一个不带 `--listen` 的进程。

| 接口 | 用途 |
|---|---|
| `GET /v1/models` | 列出 `edge-tts` 和兼容别名 `tts-1`，两者都使用同一个 Edge 后端 |
| `GET /v1/models/{id}` | 查询指定模型 |
| `GET /v1/voices` | 列出微软音色。可加 `?locale=zh-CN` 按语言前缀过滤；列表中的 `id` 就是合成时使用的音色名 |
| `POST /v1/audio/speech` | 合成语音，响应正文为音频数据 |

`/v1/voices` 是本项目扩展接口，返回带有 `object: "list"` 和 `data` 的 JSON 对象，每项音色包含 `id`、`object: "voice"`、`locale`、`gender` 和 `name`。

`POST /v1/audio/speech` 接收以下 JSON 字段：

| 字段 | 说明 |
|---|---|
| `model` | 必填，填 `edge-tts` 或 `tts-1` |
| `input` | 必填，要朗读的文本，最多 4096 个字符 |
| `voice` | 可省略，默认女声晓晓 `zh-CN-XiaoxiaoNeural`；可以传微软音色短名，也可以传 `alloy`、`echo`、`fable`、`onyx`、`nova`、`shimmer`，这些别名均映射到微软音色 |
| `speed` | 可省略，默认 `1.0`，范围 `0.25`–`4.0` |
| `response_format` | 可省略，默认 `mp3`；服务端的 `PATH` 中有 FFmpeg 时，还可以选 `wav`、`opus`、`aac`、`flac`、`pcm` |
| `stream_format` | 可省略，支持 `audio`（直接返回音频数据） |

当前不支持 `instructions` 控制语气或 `stream_format: "sse"`。

用 `curl` 列出模型和音色、合成一段语音（以下用 Bash 语法，省略 `voice` 会使用晓晓）：

```bash
curl -fsS http://127.0.0.1:8000/v1/models
curl -fsS 'http://127.0.0.1:8000/v1/voices?locale=zh-CN'
curl -fsS http://127.0.0.1:8000/v1/audio/speech \
  -H 'Content-Type: application/json' \
  --data '{"model":"edge-tts","input":"你好，这是晓晓的语音示例。"}' \
  --output speech.mp3
```

用 OpenAI Python SDK 指定云希音色和语速：

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:8000/v1", api_key="local")
with client.audio.speech.with_streaming_response.create(
    model="edge-tts",
    voice="zh-CN-YunxiNeural",
    input="你好，这是云希的语音示例。",
    speed=1.1,
) as response:
    response.stream_to_file("speech.mp3")
```

---

## 工具参数

### `tts_speak`

| 参数 | 必填 | 说明 |
|---|---|---|
| `text` | ✅ | 要读的文字 |
| `voice` | | 微软音色短名。默认 `zh-CN-XiaoxiaoNeural`（晓晓） |
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
