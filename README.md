# Astro Liomix Server

Astro Liomix 博客的 Go 后端。当前实现**评论**与**站点访问计数**两块能力：标准库 `net/http` 的 REST API，按领域模块组织（`internal/comment`、`internal/count` 各自自带实体、用例、仓储与传输），数据存 **PostgreSQL**，表结构与索引由模块自带的迁移在启动时应用，测试覆盖完整。

## 技术栈

- Go 1.25+；标准库为主（`net/http`、`log/slog`、`database/sql`、`crypto/rand`），唯一的第三方依赖是 PostgreSQL 驱动 [`jackc/pgx`](https://github.com/jackc/pgx)（经 `database/sql` 使用）
- PostgreSQL 15+；schema 由 `internal/<module>/migrations/*.sql` 在启动时迁移，不需要手工建表
- 表驱动测试 + `go vet` + `golangci-lint`
- OpenAPI 3.1 契约（`api/openapi.yaml`）
- 开发规范：`docs/go-rule.md`

## 快速开始

```bash
# 0. 准备数据库（只需一次）。表与索引由服务启动时自动创建，库本身要自己建：
#    psql -U postgres -c 'CREATE DATABASE "Blog"'
#    注意加引号：不加引号 Postgres 会把库名折成小写 blog。
#
#    密码两种给法，任选其一：
#      a) 写进 pgpass（推荐，psql 也认）：Windows 是
#         %APPDATA%\postgresql\pgpass.conf，内容是 localhost:5432:*:postgres:<密码>
#      b) 直接给完整连接串：DATABASE_URL=postgres://postgres:<密码>@localhost:5432/Blog?sslmode=disable

# 1. 运行（默认监听 :8080；development 下不设 DATABASE_URL 就连本机 Blog 库）
go run ./cmd/server

# 带本地审核口令，能把刚发的评论审过
ADMIN_TOKEN=dev-admin-token-0123456789 go run ./cmd/server

# 或用 Makefile
make run    # 等价于 go run
make dev    # 顺带注入开发用审核口令

# 其他环境必须显式给库地址（不设会启动失败，而不是悄悄连 localhost）
APP_ENV=production DATABASE_URL='postgres://blog:secret@db:5432/Blog?sslmode=disable' go run ./cmd/server
```

验证：

```bash
curl -i -X POST http://localhost:8080/api/v1/posts/hello-astro/comments \
  -H 'Content-Type: application/json' \
  -d '{"author":{"name":"Ada"},"body":"Great write-up."}'

curl -X POST http://localhost:8080/api/v1/count/site/visits \
  -H 'Content-Type: application/json' \
  -d '{"visitorId":"browser-abc"}'

curl http://localhost:8080/api/v1/count/site
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
│   │   ├── credential.go       # 作者删除凭据的签发与比对
│   │   ├── repo_postgres.go    # PostgreSQL 仓储适配器
│   │   ├── migrate.go          # 内嵌并应用本模块迁移
│   │   ├── migrations/         # 0001_comments.sql、0002_comment_constraints.sql
│   │   ├── handler_http.go     # 路由注册与 handler
│   │   └── dto.go              # 请求/响应形状
│   ├── count/                  # 计数模块：站点访问量 + 文章阅读量
│   │   ├── entity.go           # Visit / PostView / SiteStats / PostViews 与不变量
│   │   ├── port.go             # Repository：写入与读取共用一次原子操作
│   │   ├── service.go          # RecordSiteVisit / SiteStats / RecordPostView / PostViews
│   │   ├── repo_postgres.go    # PostgreSQL 仓储适配器（事务内做访客去重）
│   │   ├── migrate.go          # 内嵌并应用本模块迁移
│   │   ├── migrations/         # 0001_counters.sql、0002_counter_constraints.sql
│   │   ├── handler_http.go     # 路由注册与 handler
│   │   └── dto.go              # 请求/响应形状
│   └── platform/               # 跨模块横切能力，不含业务
│       ├── apperr/             # 全项目共享的错误类别与 ValidationError
│       ├── config/             # 环境变量解析与启动校验
│       ├── httpkit/            # 路由、中间件、JSON 编解码、错误映射
│       ├── postgres/           # 连接池、Executor 接口、DSN 脱敏、迁移执行器、约束冲突判定
│       │   └── postgrestest/   # 集成测试连开发库 + 事务回滚的助手（只被测试引用）
│       └── id/                 # 加密随机标识符
├── api/openapi.yaml            # API 契约
├── configs/.env.example        # 配置参考
├── docs/go-rule.md             # Go 开发规范
├── Makefile                    # run/build/test/cover/lint
└── .golangci.yml               # 静态检查配置
```

### 模块边界与依赖方向

```
cmd ──┬──▶ comment ──┐
      └──▶ count ────┴──▶ platform ──▶ platform/httpkit ──▶ platform/apperr
                                        └─▶ platform/id
```

- 模块内：`handler → service → entity`，`repo` 实现 `port.go` 里的接口，`service` 只依赖接口。
- 模块间：单向依赖，模块之间互不引用。跨模块只使用对方导出的类型与服务。
- `platform/*` 不依赖任何业务模块，否则会退化成上帝包。

`cmd/server` 是唯一装配点：`postgres.Open` 建连接池 → `comment.Migrate` / `count.Migrate` 应用各自模块的 SQL → 注入仓储与服务。新增一张表只需要在对应模块的 `migrations/` 下加一个文件，不用改装配代码，也不用另一个模块知道它。

### 关于单包模块

当前每个模块是一个 Go 包，层次边界靠文件命名与评审守，**编译器不强制**。出现下列任一情况时，按 `docs/go-rule.md` 2.3 升级为子包（`comment/entity`、`comment/service` …）：

- 模块内文件超过 15 个，或单文件超过 400 行
- 出现第二组适配器（gRPC、消息消费、CLI）
- 想单独测试某一层，却不得不构造整个模块

## API

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET`  | `/healthz` / `/readyz` | 探活 / 就绪 |
| `GET`  | `/api/v1/posts/{slug}/comments` | 按游标翻页列出评论（默认仅已审核） |
| `POST` | `/api/v1/posts/{slug}/comments` | 发布评论（默认进入 pending），可带 `Idempotency-Key` |
| `GET`  | `/api/v1/comments/counts?slugs=a,b` | 批量读取每篇的已审核评论数（卡片上的数字），最多 100 个 |
| `GET`  | `/api/v1/comments/{id}` | 查询单条评论（未审核的对外 404） |
| `PATCH` | `/api/v1/comments/{id}/status` | 改审核状态，需 `Authorization: Bearer $ADMIN_TOKEN` |
| `DELETE` | `/api/v1/comments/{id}` | 删除评论及其所有回复：管理员口令，或创建时拿到的作者凭据 |
| `GET`  | `/api/v1/count/site` | 站点累计访问量 / 访客数 |
| `POST` | `/api/v1/count/site/visits` | 上报一次访问，返回最新计数 |
| `GET`  | `/api/v1/count/posts?slugs=a,b` | 批量读取文章阅读量（列表页一次问完） |
| `POST` | `/api/v1/count/posts/views` | 文章阅读量 +1（`{"slug":"..."}`），返回该文最新值 |

完整契约见 `api/openapi.yaml`。协议层的约定（与 `docs/前后端 API 通信规范.md` 对齐）：

- 新评论默认 `pending`，需审核后才进入公开列表（`?status=approved` 默认行为）；没有审核入口时它们永远不会公开，所以 `ADMIN_TOKEN` 是这套评论能不能用的开关。
- 未审核的评论在 `GET /comments/{id}` 上也当作不存在（404），避免猜 ID 读到待审内容。
- 删除是级联的：删父评论会连同整棵回复子树一起删，不留悬空 `parentId`。
- 访客可以撤回自己刚发的评论：`POST` 的响应里带一个 `deleteToken`，把它当 Bearer 口令发给 `DELETE /comments/{id}` 即可。这个凭据只够删**那一条**，服务端只存摘要，读过一次就再也拿不到 —— 前端存在 localStorage 里，换设备就只剩管理员能删。导入的评论没有凭据，只能由管理员删。
- 评论数只算 `approved`：待审的评论不会出现在列表里，数字也就不该先跳一下。没人评论过的 slug 返回 `count: 0`，不是 404。
- 作者邮箱**永不回传**，仅用于审核与通知。
- 计数是**累计值**，不按天清零；`visitors` 按客户端提供的 `visitorId` 去重，不带 ID 的访问只加 `views`。
- 文章阅读量是**页面浏览量**，同一读者刷新会再加一；未读过的文章批量查询返回 `views: 0` 而不是缺项。
- 文章 slug 走请求体而不是路径：嵌套 slug（`notes/2026/hello`）含 `/`，路径通配符跨不了段。
- 计数写入与读取走同一次仓储调用，返回体已是写入后的快照，客户端不必再发一次 GET。
- **错误体是 RFC 9457 problem+json**（`application/problem+json`）：`{title,status,code,detail?,instance?,traceId,errors?}`。客户端只按 `code` 分支（如 `VALIDATION_FAILED`、`RESOURCE_NOT_FOUND`、`RESOURCE_CONFLICT`），`title`/`detail` 是给开发者看的，随时可能改文案。错误码注册表在 `internal/platform/apperr/codes.go`。
- **状态码分得清**：`400` 是请求本身读不出来（JSON 坏了、游标不是本服务发的），`413` 超过体积上限，`415` 不是 `application/json`，`422` 是读得出来但字段不合格 —— 422 的 `errors[]` **一次列出所有**不合格字段，`field` 直接对应请求体路径（`author.email`），前端可以逐个标红。
- **未知字段忽略而不是报错**：客户端先上新字段、服务端后支持，这个发布顺序才不会断。
- **列表用游标翻页**：`?limit=20&cursor=...`，响应是 `{items, nextCursor}`，`nextCursor` 为 `null` 表示到底。排序是全序 `(created_at DESC, id DESC)`，游标记的是上一页最后一行，所以翻页期间有人发评论也不会重复或漏项。
- **`parentId` 显式给 `null`**，不是省略字段：客户端要能区分「顶层评论」和「响应里没这个字段」。
- **每个 GET 都写了缓存语义**：公开数据 `public, max-age=0, must-revalidate` + `ETag`（`If-None-Match` 命中回 304），写操作和带凭据的响应 `no-store`。
- **`POST` 的幂等键**：客户端按「一次提交一个键」生成，`POST /comments` 带 `Idempotency-Key` 时，同键同体重发会回放首次响应（并带 `Idempotency-Replayed: true`），同键换内容返回 `409`。键存在 `idempotency_keys` 表里（唯一索引抢占），不是进程内 map —— 多实例下只有共享存储才算数。
- **删除语义**：成功 `204`；重复删除 `404`（目标状态已经达成，客户端应当当成功处理）。
- **401 一定带 `WWW-Authenticate`**：`Bearer realm="moderator"` 或 `Bearer realm="comment-author", error="invalid_token"`，客户端据此知道该拿哪种凭据。
- **读待审队列要凭据**：`?status=pending|all` 只有带管理员口令才给，否则 `401` —— 静默返回空列表会让人以为没东西可审。
- **`X-Request-Id` 回显在响应头和错误体里**，访问日志按**路由模板**（`/api/v1/comments/{id}`）记录，不按具体路径。跨源调用时它还出现在 `Access-Control-Expose-Headers` 里，否则前端 `headers.get()` 永远是 null。

## 配置

全部通过环境变量覆盖（见 `configs/.env.example`），启动时一次性校验并汇总所有错误。关键项：

| 变量 | 默认 | 说明 |
|------|------|------|
| `APP_ENV` | `development` | 决定日志格式（production 用 JSON） |
| `HTTP_ADDR` | `:8080` | 监听地址 |
| `HTTP_REQUEST_TIMEOUT` | `10s` | 单请求 deadline |
| `HTTP_MAX_BODY_BYTES` | `32768` | 请求体上限 |
| `HTTP_CORS_ORIGINS` | `*` | 允许跨源调用 API 的前端来源（逗号分隔，`*` 表示不限） |
| `COMMENT_DEFAULT_PAGE_SIZE` | `20` | 列表默认页大小 |
| `COMMENT_MAX_PAGE_SIZE` | `100` | 列表页大小硬上限 |
| `DATABASE_URL` | development 下 `postgres://postgres@localhost:5432/Blog?sslmode=disable`，其他环境**必填** | 连接串；密码可留给 pgpass |
| `DATABASE_MAX_OPEN_CONNS` | `10` | 连接池上限 |
| `DATABASE_MAX_IDLE_CONNS` | `5` | 空闲连接数，不得大于上限 |
| `DATABASE_CONN_MAX_LIFETIME` | `30m` | 连接回收周期 |
| `ADMIN_TOKEN` | 空 | 审核接口的 Bearer 口令，≥16 字符；为空时审核路由**不挂载** |

### 数据库

- 只要求**库存在**，表、约束、索引都由迁移建；已应用的迁移记在 `schema_migrations`，重复启动是空操作。
- 迁移按文件名单调执行，每个文件一个事务：语法错误会让整个文件回滚，数据库停在上一个版本而不是半迁移状态。
- 每个迁移事务都带 `SET LOCAL lock_timeout = '5s'`：拿不到表锁就让进程失败退出、由外部重启重试，而不是排在一条慢查询后面把所有请求一起堵死（PostgreSQL 的锁队列是先进先出）。
- 这个执行器在事务里跑 DDL，所以 `CREATE INDEX CONCURRENTLY` 用不了。评论表还小，普通建索引是瞬时的；真要在线给大表加索引，得手工单独执行，别写进迁移文件。
- 同时启动两个进程不会互相破坏（`CREATE TABLE IF NOT EXISTS` + 记录用 `ON CONFLICT DO NOTHING`），但多实例部署应把迁移拆成独立的启动任务。
- 删除评论的级联由 `parent_id ... ON DELETE CASCADE` 完成，服务层不遍历子树；`parent_id` 上补了独立索引，否则每次级联都要全表扫一遍（PostgreSQL 只给主键和唯一约束建索引，外键列要自己加）。
- 列表与计数共用一个复合索引 `(post_slug, status, created_at DESC, id DESC)`：过滤、排序、聚合都走它，不需要「先按 slug 取全部再排」的两段式。
- 「没有删除凭据」在库里是 `NULL` 而不是空字符串，配一条 `CHECK (delete_token_hash ~ '^[0-9a-f]{64}$')`；领域层仍用空串表示缺席，边界上互转。
- 字段上限在库里也有一份 `CHECK`（slug 200、姓名 80、邮箱 254、网址 200、正文 2000 字符），服务层校验失效或被脚本绕过时数据库仍然拒绝。
- 评论数用一条 `GROUP BY` 聚合回答整批 slug，不靠 `len(List(...))`。
- `timestamptz` 读出后统一转 UTC，接口的时间字符串不随服务器时区变化；Postgres 精度是微秒。
- 连接串进日志前会脱敏（`postgres.Redact`），启动失败的错误里不会带密码。
- 表设计复核过 [timescale/pg-aiguide](https://github.com/timescale/pg-aiguide) 的 PostgreSQL 规范：没有满足 hypertable 条件的时间序列表（评论、访客、阅读量都是「一行一实体」，不按时间追加），所以没引入 TimescaleDB。

## 开发

```bash
make test    # 运行测试
make cover   # 覆盖率（coverage.html）
make vet     # go vet
make lint    # golangci-lint（需先安装）
make fmt     # gofmt 全量格式化
```

集成测试直接用**开发库**（就是 `go run` 连的那个 `Blog`），没有第二个测试库要同步，也没有「跑之前先清空、跑完再补数据」的脚本：每个用例在自己的事务里跑，用例需要的空表也在事务里造，结束时回滚，开发数据一行不少。

```bash
# 连的就是开发库，密码同样走 pgpass
APP_ENV=development go test ./...    # 等价于 make test

# 想指到别处（比如另一台机器上的开发库）就换 DATABASE_URL
DATABASE_URL='postgres://postgres@localhost:5432/Blog?sslmode=disable' make test
```

要点：

- `APP_ENV` 必须是 `development`，否则集成用例直接 skip —— 它们会写库，不能指向生产。
- 开发库连不上时同样的用例 skip 带着原因打印出来，所以没装 PostgreSQL 的机器上 `go test ./...` 仍然会跑完（只跑单测）。
- 会制造数据库错误的用例（重复主键、悬空 `parentId`）各自一个事务：PostgreSQL 里任何一条语句失败都会让整个事务进入 aborted 状态，同一事务里期待第二个约束错误只会看到 25P02。
- 迁移执行器的两个用例绕不开 DDL，所以它们在开发库里建 `migrate_probe` / `migrate_broken` 这类探针表，`t.Cleanup` 负责删掉。
- 多个包会被并行迁移，`postgrestest.Migrate` 用 `pg_advisory_lock` 串行化，避免两个会话同时建 `schema_migrations` 撞系统目录。

当前覆盖率：`comment` 90.5%、`count` 93.8%、`config` 96.8%、`httpkit` 82.7%、`postgres` 75.6%、`idempotency` 71.4%、`id` 80%。

> Windows 下 `make test-race` 需要 cgo（安装 gcc，如 MinGW）；无 gcc 时用 `make test`。
> 这条限制已登记在下方「规范例外」中。

## 开发规范

工程约定以 `docs/go-rule.md` 为准，本仓库已落地的部分：

- 按领域模块组织（2.1–2.4），单包模式下层次靠文件纪律（2.3）
- 错误分类哨兵集中在 `platform/apperr`（10.1、10.8），逐层 `%w` 包装，`errors.Is/As` 判断
- 接口在消费方定义（4.1）：`port.go` 与 `service.go` 同包
- 请求体读取与解码统一走 `platform/httpkit.ReadBody` / `DecodeJSON`（18.3）：体积上限、`Content-Type` 校验、忽略未知字段、拒尾随数据，模块不再各写一份
- 构造函数 fail fast + functional options（9.2）；时钟、随机源、日志器全部可注入（16）
- `context` 首参贯穿阻塞操作（11）；HTTP 服务端显式超时 + 优雅停机（18.5）

### 规范例外

按 `docs/go-rule.md` 27 节「本项目的例外」登记：

| 违反的条目 | 本项目做法 | 理由 | 代价与缓解 | 复核时间 |
|---|---|---|---|---|
| 12.4 提交前跑 `-race` | 本地无法执行，依赖 CI | 本机无 gcc，`-race` 需要 cgo | 反馈延迟到流水线；PR 合并前必须等 `-race` 结果 | 换开发机时复核 |
| 18.6 `POST` 创建返回 201 + `Location` | `POST /api/v1/count/site/visits` 返回 200 + 最新计数 | 一次访问是计数器递增，没有可寻址资源，`Location` 无处可指 | 客户端不能按 REST 惯例假定拿到资源地址；契约里写明 200 的语义 | 引入带 ID 的持久访问记录时复核 |

## 待办

- [ ] 文章模块（`internal/post`）：实体、CRUD、slug 路由；slug 校验随之从 `comment`、`count` 收拢
- [ ] 访客去重表需要清理策略：`site_visitors` 只增不减，长期运行要按天归档或加 TTL
- [ ] 审核辅助：待审列表分页、spam 自动判定（`Moderator` 接口已留好插槽）
- [ ] 限流：发评论与读计数都还没有速率限制，公开接口可能被刷（要按端点分级，且计数在多实例下需要共享存储；进程内桶只会在单实例时看起来有效）
- [ ] 幂等键的清理目前只在启动时 `Prune` 一次，长期运行应改成定时任务（`idempotency_keys` 只增不减）
- [ ] `/readyz` 目前只看进程存活，应加一次数据库 ping
- [ ] 已有两个领域模块，`comment` 与 `count` 按 2.3 评估是否拆子包
