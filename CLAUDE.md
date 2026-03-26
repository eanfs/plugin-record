# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

plugin-record 是 Monibuca (m7s) 流媒体服务器的录制插件，模块路径为 `github.com/eanfs/plugin-record/v4`，包名 `record`。提供 FLV、FMP4、HLS、裸流格式的录制功能，支持本地存储和 S3/Minio 远程存储。

## 构建与运行

```bash
# 编译检查
go build ./...

# 运行方式：通过实例工程（monibuca-dev）引入此插件
cd ../../monibuca-dev && go run .

# 刷新 Go 模块索引
GOPROXY=proxy.golang.org go list -m github.com/eanfs/plugin-record/v4@v4.9.6
```

本插件不独立运行，需通过 `monibuca-dev/main.go` 中 `_ import` 引入，`monibuca-dev/go.mod` 中通过 `replace` 指向本地路径。

**当前无测试文件。**

## 核心架构

### 插件注册

`main.go` 中通过 `InstallPlugin(RecordPluginConfig, defaultYaml)` 注册插件，监听引擎事件：
- `FirstConfig` / `config.Config`：初始化数据库、启动后台维护任务
- `SEpublish`：根据 autorecord + filter 配置自动开始录制

### 录制器体系

```
IRecorder (接口, subscriber.go)
  └── Recorder (基础结构体, subscriber.go)
       ├── FLVRecorder (flv.go) — 完整实现，支持普通/事件两种模式
       ├── FMP4Recorder (fmp4.go) — 分片 MP4
       ├── HLSRecorder (hls.go) — M3U8 + TS 分片
       ├── MP4Recorder (mp4.go) — 部分实现，多数方法 TODO
       └── RawRecorder (raw.go) — H.264/H.265/AAC/PCM 裸流
```

`Recorder` 内嵌 `Subscriber`（引擎订阅者），通过订阅流数据写入文件。

### 录制模式

- **OrdinaryMode (0)**：连续录制（自动 + 手动触发）
- **EventMode (1)**：事件驱动录制，支持 beforeDuration/afterDuration 缓冲

### 分片机制

配置 `fragment` 后自动按时间间隔切片（在 I 帧处切割），每个分片独立文件，文件名含时间戳。

### 存储架构

- **本地存储**：文件路径 `{path}/{streamPath}/{fileName}.{ext}`
- **S3/Minio**：`storage.go` 实现，特性包括：
  - 信号量控制并发上传数（默认 4）
  - 指数退避重试（默认 3 次，10s 基础间隔）
  - 上传超时控制（默认 15 分钟）
  - 失败上传记录到数据库，后台每 30 分钟重试

### 数据库

- **SQLite**（默认）：存储 FLV 关键帧索引、录制记录、上传异常
- **MySQL**（可选）：配置 `mysqlDSN` 启用
- 数据模型：`EventRecord`、`Exception`、`FLVKeyframe`（entity.go）

### 后台维护任务

- **过期清理**：每小时检查，删除超过 `recordFileExpireDays` 天的录制文件
- **上传重试**：每 30 分钟重试失败的 Minio 上传（exception.go）
- **录制检查**：每 5 分钟检测并重启意外停止的录制（checker.go）

## 关键文件

| 文件 | 职责 |
|------|------|
| main.go | 插件初始化、配置、事件处理、自动录制触发 |
| subscriber.go | Recorder 基础结构体、IRecorder 接口、文件创建 |
| config.go | Record 配置结构体、FileWr/FileWriter 接口 |
| storage.go | Minio/S3 客户端管理、并发上传、重试策略 |
| restful.go | 管理 API：开始/停止录制、文件列表、分页 |
| restful_event.go | 事件录制 API、VOD 时间范围提取 |
| vod.go | 点播服务：FLV 回放（支持 start/end/speed 参数） |
| entity.go | 数据库模型定义 |

## API 端点

### 录制管理
- `GET /record/api/start?type=flv&streamPath=live/rtc&fileName=xxx&fragment=10s&duration=5m&append=1` — 开始录制（返回录制 ID）
- `GET /record/api/stop?id={id}` — 停止指定录制
- `GET /record/api/stop_stream?streamPath=live/rtc` — 停止流的所有录制
- `GET /record/api/list?type=[flv|mp4|hls|raw|fmp4|raw_audio]` — 列出录制文件
- `GET /record/api/list_page?type=...&pageSize=10&pageNum=1&streamPath=...` — 分页列表
- `GET /record/api/list/recording` — 列出正在进行的录制

### 点播回放
- `GET /record/{streamPath}.flv` — FLV 点播（支持 start/end/speed 查询参数）
- `GET /record/{streamPath}.mp4` — MP4 点播
- `GET /record/{streamPath}.m3u8` — HLS 点播

## 配置要点

- `autorecord: true` + `filter` 正则 → 自动录制匹配的流
- `fragment: 20s` → 每 20 秒切片一个文件
- `diskMaxPercent: 80.0` → 磁盘使用率超过阈值停止录制
- `recordFileExpireDays: 7` → 7 天后自动删除（0 = 不删除）
- `storage.endpoint` 非空时启用 Minio 上传
