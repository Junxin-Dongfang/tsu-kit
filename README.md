# tsu-kit

组织内通用 Go 包集合（framework-neutral）。每个子包**不依赖任何特定服务框架**,可被任意 Go 项目直接 `go get` 引用,零额外 `replace`。

## 包

| 包 | 用途 |
|---|---|
| `observability/logging` | 结构化日志核心:分级 `Step/Info/Warn/Error` → 可插拔 `Sink`,活跃 span 时附为 span event;默认 stdout JSON sink。 |
| `observability/tracing` | OTel tracing:`Init`/`Provider`/`Sampler`、carrier `Encode/Decode`、HTTP 中间件、replay body 捕获。 |
| `observability/metrics` | Prometheus 指标:`Registry`/`Init`、Go runtime collector、HTTP `PromHandler`、due-free 记录原语(`ObserveNodeRoute`/`GateConnectionInc` 等)。 |

## 用法

```go
import "github.com/Junxin-Dongfang/tsu-kit/observability/logging"

logging.ConfigureProcess("my-service")
logging.Info(ctx, "started", "port", 8080)
```

```sh
go get github.com/Junxin-Dongfang/tsu-kit/observability/logging@v0.1.0
```

## 设计约束

- **框架中立**:子包只依赖 stdlib 与公共生态(prometheus / OpenTelemetry / fiber),不依赖任何 actor/RPC 框架。框架特定的接线(如把某框架内部 logger 路由进本库)由**消费方的适配层**承担,不进本库。
- **版本**:语义化 tag(`vX.Y.Z`),正常 git 历史。
