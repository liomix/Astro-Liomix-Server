# Astro Liomix Server

Astro Liomix 博客的 Go 后端。当前实现**评论能力**：标准库 `net/http` 的 REST API，按领域模块组织（`internal/comment` 自带实体、用例、仓储与传输），内存仓储占位，测试覆盖完整。

## 技术栈

- Go 1.24+，仅标准库（`net/http`、`log/slog`、`crypto/rand`），**零第三方依赖**
- 表驱动测试 + `go vet` + `golangci-lint`
- OpenAPI 3.1 契约（`api/openapi.yaml`）
- 开发规范：`docs/go-rule.md`

## 快速开始

```bash
# 直接运行（默认监听 :8080）
go run ./cmd/server

# 或用 Makefile
make run
```

验证：

```bash
curl -i -X POST http://localhost:8080/api/v1/posts/hello-astro/comments \
  -H 'Content-Type: application/json' \
  -d '{"author":{"name":"Ada"},"body":"Great write-up."}'

curl http://localhost:8080/healthz
```

## 目录结构

默认按**领域模块**组织：一个模块自包含全部层次，跨模块的基础能力放在 `platform`。

```
.
├── cmd/server/                 # 唯一入口：装配依赖 + 启动/优雅停机
├── internal/
│   ├── comment/                # 评论模块（一个 Go 包，自带全部分层）
│   │   ├── entity.go           # 实体、值对象、不变量校验
│   │   ├── port.go             # 本模块声明的外部依赖（Repository / Moderator）
│   │   ├── service.go          # 用例编排、分页、审核策略
│   │   ├── repo_memory.go      # 内存仓储适配器（占位）
│   │   ├── handler_http.go     # 路由注册与 handler
│   │   └── dto.go              # 请求/响应形状
│   └── platform/               # 跨模块横切能力，不含业务
│       ├── apperr/             # 全项目共享的错误类别与 ValidationError
│       ├── config/             # 环境变量解析与启动校验
│       ├── httpkit/            # 路由、中间件、JSON 封装、错误映射
│       └── id/                 # 加密随机标识符
├── api/openapi.yaml            # API 契约
├── configs/.env.example        # 配置参考
├── docs/go-rule.md             # Go 开发规范
├── Makefile                    # run/build/test/cover/lint
└── .golangci.yml               # 静态检查配置
```

### 模块边界与依赖方向

```
cmd ──▶ comment ──▶ platform
              └──▶ platform/httpkit ──▶ platform/apperr
                                     └─▶ platform/id
```

- 模块内：`handler → service → entity`，`repo` 实现 `port.go` 里的接口，`service` 只依赖接口。
- 模块间：单向依赖。跨模块只使用对方导出的类型与服务。
- `platform/*` 不依赖任何业务模块，否则会退化成上帝包。

`cmd/server` 是唯一装配点：换掉 `comment.NewMemoryRepository()` 那一行，就能把内存存储换成 SQL，模块内代码不需要改。

### 关于单包模块

当前每个模块是一个 Go 包，层次边界靠文件命名与评审守，**编译器不强制**。出现下列任一情况时，按 `docs/go-rule.md` 2.3 升级为子包（`comment/entity`、`comment/service` …）：

- 模块内文件超过 15 个，或单文件超过 400 行
- 出现第二组适配器（gRPC、消息消费、CLI）
- 想单独测试某一层，却不得不构造整个模块

## API

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET`  | `/healthz` / `/readyz` | 探活 / 就绪 |
| `GET`  | `/api/v1/posts/{slug}/comments` | 分页列出评论（默认仅已审核） |
| `POST` | `/api/v1/posts/{slug}/comments` | 发布评论（默认进入 pending） |
| `GET`  | `/api/v1/comments/{id}` | 查询单条评论 |

完整契约见 `api/openapi.yaml`。要点：

- 新评论默认 `pending`，需审核后才进入公开列表（`?status=approved` 默认行为）。
- 作者邮箱**永不回传**，仅用于审核与通知。
- 所有错误返回统一 JSON：`{"code","message","field?","requestId?"}`。
- 响应头回显 `X-Request-Id` 用于问题定位。

## 配置

全部通过环境变量覆盖（见 `configs/.env.example`），启动时一次性校验并汇总所有错误。关键项：

| 变量 | 默认 | 说明 |
|------|------|------|
| `APP_ENV` | `development` | 决定日志格式（production 用 JSON） |
| `HTTP_ADDR` | `:8080` | 监听地址 |
| `HTTP_REQUEST_TIMEOUT` | `10s` | 单请求 deadline |
| `HTTP_MAX_BODY_BYTES` | `32768` | 请求体上限 |
| `COMMENT_DEFAULT_PAGE_SIZE` | `20` | 列表默认页大小 |
| `COMMENT_MAX_PAGE_SIZE` | `100` | 列表页大小硬上限 |

## 开发

```bash
make test    # 运行测试
make cover   # 覆盖率（coverage.html）
make vet     # go vet
make lint    # golangci-lint（需先安装）
make fmt     # gofmt 全量格式化
```

当前覆盖率：`comment` 85%、`config` 97%、`httpkit` 76%、`id` 80%。

> Windows 下 `make test-race` 需要 cgo（安装 gcc，如 MinGW）；无 gcc 时用 `make test`。
> 这条限制已登记在下方「规范例外」中。

## 开发规范

工程约定以 `docs/go-rule.md` 为准，本仓库已落地的部分：

- 按领域模块组织（2.1–2.4），单包模式下层次靠文件纪律（2.3）
- 错误分类哨兵集中在 `platform/apperr`（10.1、10.8），逐层 `%w` 包装，`errors.Is/As` 判断
- 接口在消费方定义（4.1）：`port.go` 与 `service.go` 同包
- 构造函数 fail fast + functional options（9.2）；时钟、随机源、日志器全部可注入（16）
- `context` 首参贯穿阻塞操作（11）；HTTP 服务端显式超时 + 优雅停机（18.5）

### 规范例外

按 `docs/go-rule.md` 27 节「本项目的例外」登记：

| 违反的条目 | 本项目做法 | 理由 | 代价与缓解 | 复核时间 |
|---|---|---|---|---|
| 12.4 提交前跑 `-race` | 本地无法执行，依赖 CI | 本机无 gcc，`-race` 需要 cgo | 反馈延迟到流水线；PR 合并前必须等 `-race` 结果 | 换开发机时复核 |

## 待办

- [ ] 文章模块（`internal/post`）：实体、CRUD、slug 路由
- [ ] SQL 仓储适配器（PostgreSQL + 迁移）替换内存实现
- [ ] 审核能力（spam 过滤 / 人工审核队列）
- [ ] 认证与限流（JWT、rate limiter）
- [ ] 第二个领域模块出现后，`comment` 按 2.3 评估是否拆子包
