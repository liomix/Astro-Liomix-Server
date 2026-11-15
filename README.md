# Astro Liomix Server

Astro Liomix 博客的 Go 后端。当前实现**评论**、**站点访问计数**与**管理员控制台**三块能力：标准库 `net/http` 的 REST API，按领域模块组织（`internal/comment`、`internal/count`、`internal/admin` 各自自带实体、用例、仓储与传输），数据存 **PostgreSQL**，表结构与索引由模块自带的迁移在启动时应用，测试覆盖完整。

## 技术栈

- Go 1.25+；标准库为主（`net/http`、`log/slog`、`database/sql`、`crypto/rand`），唯一的第三方依赖是 PostgreSQL 驱动 [`jackc/pgx`](https://github.com/jackc/pgx)（经 `database/sql` 使用）
- PostgreSQL 15+；schema 由 `internal/<module>/migrations/*.sql` 在启动时迁移，不需要手工建表
- 表驱动测试 + `go vet` + `golangci-lint`
- OpenAPI 3.1 契约（`api/openapi.yaml`）
- 开发规范：`docs/go-rule.md`（Go）、`docs/前后端 API 通信规范.md`（接口契约）、`docs/PostgreSQL规范.md`（库表与查询）

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

# 带本地审核口令，能把刚发的评论审过（静态口令是应急通道）
ADMIN_TOKEN=dev-admin-token-0123456789 go run ./cmd/server

# 第一次运行前，先在数据库里创建管理员账号（密码只以 PBKDF2 摘要入库）
go run ./cmd/server admin create -username owner
#   默认生成 24 位随机密码并只打印一次；想自己指定就从管道读：
#   echo 'my-long-passphrase' | go run ./cmd/server admin create -username owner -password-stdin

# 然后启动服务并打开控制台（服务端渲染，不需要 Node）：
go run ./cmd/server
#   http://localhost:8080/admin

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

# 管理员登录，拿到一个 adm_ 开头的会话 token（只在响应里出现一次）
curl -X POST http://localhost:8080/api/v1/admin/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"owner","password":"dev-admin-password-123"}'

# 用会话 token 读待审队列、审核评论（不再需要 ADMIN_TOKEN）
curl -H "Authorization: Bearer $TOKEN" \
  'http://localhost:8080/api/v1/posts/hello-astro/comments?status=pending'
curl -X PATCH http://localhost:8080/api/v1/comments/<id>/status \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"status":"approved"}'
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
│   ├── admin/                  # 管理员模块：账号、登录会话、审计查询
│   │   ├── entity.go           # Account / Session、状态与字段不变量
│   │   ├── password.go         # PBKDF2-HMAC-SHA256 摘要（自带 scheme/cost/salt）
│   │   ├── token.go            # 会话 token 签发与摘要
│   │   ├── port.go             # Repository / AuditStore
│   │   ├── service.go          # 登录、会话、账号管理、改密、引导
│   │   ├── repo_postgres.go    # PostgreSQL 仓储适配器
│   │   ├── migrate.go          # 内嵌并应用本模块迁移
│   │   ├── migrations/         # 0001_admin.sql
│   │   ├── handler_http.go     # 路由、鉴权中间件、handler
│   │   ├── webui.go            # /admin 服务端渲染控制台（Cookie + CSRF + 静态资源）
│   │   ├── webui_templates/    # layout 与首页/评论管理/最近活动/邮件模板（内嵌进二进制）
│   │   ├── webui_assets/css/   # app.css：自带的设计系统（令牌 + 组件）
│   │   ├── webui_assets/js/    # app.js（toast/贪睡/确认）+ theme.js（明暗主题）
│   │   └── dto.go              # 请求/响应形状（不含任何密码字段）
│   └── platform/               # 跨模块横切能力，不含业务
│       ├── apperr/             # 全项目共享的错误类别与 ValidationError
│       ├── audit/              # 审计日志：Event / Recorder / Store + 迁移
│       ├── config/             # 环境变量解析与启动校验
│       ├── cursor/             # 游标分页的编解码（admin / audit 共用）
│       ├── httpkit/            # 路由、中间件、JSON 编解码、错误映射
│       ├── postgres/           # 连接池、Executor 接口、DSN 脱敏、迁移执行器、约束冲突判定
│       │   └── postgrestest/   # 集成测试连开发库 + 事务回滚的助手（只被测试引用）
│       ├── idempotency/        # 幂等键存储（表 + 迁移）
│       ├── mail/               # 通知邮件：SMTP 客户端、设置行（表 + 迁移）、异步队列
│       └── id/                 # 加密随机标识符
├── api/openapi.yaml            # API 契约
├── configs/.env.example        # 配置参考
├── docs/go-rule.md             # Go 开发规范
├── Makefile                    # run/build/test/cover/lint
└── .golangci.yml               # 静态检查配置
```

### 模块边界与依赖方向

```
cmd ──┬──▶ comment ──┬──┐
      ├──▶ count ────┴──┴──▶ platform ──▶ platform/httpkit ──▶ platform/apperr
      └──▶ admin ────┬──▶ platform/audit
                     └──▶ platform/cursor
```

- 模块内：`handler → service → entity`，`repo` 实现 `port.go` 里的接口，`service` 只依赖接口。
- 模块间：单向依赖，模块之间互不引用。跨模块只使用对方导出的类型与服务；`comment` 通过 `HTTPConfig.AdminAuthorizer` 接受 admin 会话，依赖的是 `platform/audit.Actor`，不是 admin 包。
- `platform/*` 不依赖任何业务模块，否则会退化成上帝包。审计的写入端口和存储都在 `platform/audit`，admin 与 comment 各自通过它落一条记录。

`cmd/server` 是唯一装配点：`postgres.Open` 建连接池 → 各模块 `Migrate`（含 `platform/audit`、`platform/idempotency`）应用 SQL → 注入仓储与服务 → 从环境变量引导第一个管理员。新增一张表只需要在对应模块的 `migrations/` 下加一个文件，不用改装配代码，也不用另一个模块知道它。

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
| `PATCH` | `/api/v1/comments/{id}/status` | 改审核状态，需管理员会话（或遗留 `ADMIN_TOKEN`） |
| `DELETE` | `/api/v1/comments/{id}` | 删除评论及其所有回复：管理员凭据，或创建时拿到的作者凭据 |
| `GET`  | `/api/v1/count/site` | 站点累计访问量 / 访客数 |
| `POST` | `/api/v1/count/site/visits` | 上报一次访问，返回最新计数 |
| `GET`  | `/api/v1/count/posts?slugs=a,b` | 批量读取文章阅读量（列表页一次问完） |
| `POST` | `/api/v1/count/posts/views` | 文章阅读量 +1（`{"slug":"..."}`），返回该文最新值 |
| `POST` | `/api/v1/admin/auth/login` | 管理员登录，返回 `adm_` 会话 token（只在响应里出现一次） |
| `POST` | `/api/v1/admin/auth/logout` | 注销当前会话（其他设备不受影响） |
| `GET`  | `/api/v1/admin/auth/session` | 当前账号与会话（含到期时间） |
| `GET`  | `/api/v1/admin/accounts` | 账号列表，游标分页，可按状态过滤 |
| `POST` | `/api/v1/admin/accounts` | 新建账号（用户名小写唯一，密码 ≥12 位） |
| `GET`  | `/api/v1/admin/accounts/{id}` | 读单个账号 |
| `PATCH` | `/api/v1/admin/accounts/{id}` | 改显示名 / 启用停用 |
| `DELETE` | `/api/v1/admin/accounts/{id}` | 删除账号（连带其所有会话） |
| `POST` | `/api/v1/admin/accounts/{id}/password` | 改密或重置（自己的必须给当前密码） |
| `GET`  | `/api/v1/admin/accounts/{id}/sessions` | 该账号的在线会话 |
| `DELETE` | `/api/v1/admin/accounts/{id}/sessions/{sessionId}` | 踢掉某个会话 |
| `GET`  | `/api/v1/admin/audit-logs` | 审计日志，游标分页，可按 `action` / `actorId` 过滤 |

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
- **正文存的是源，不是渲染结果**：`body` 是作者写的 Markdown，行内表情写成短码 `:huoying/hy-1:`，由阅读端拿同一份配置翻译成图片。存 `<img>` 会让那条评论把当时的资源地址焊死（换目录就全成破图），也会让 2000 字的额度被标记吃掉（一个标签 77 字符，一个短码 15），还会把字段变成「可信 HTML」。见 `internal/comment/migrations/0004_body_is_source.sql`。
- **`parentId` 显式给 `null`**，不是省略字段：客户端要能区分「顶层评论」和「响应里没这个字段」。
- **每个 GET 都写了缓存语义**：公开数据 `public, max-age=0, must-revalidate` + `ETag`（`If-None-Match` 命中回 304），写操作和带凭据的响应 `no-store`。
- **`POST` 的幂等键**：客户端按「一次提交一个键」生成，`POST /comments` 带 `Idempotency-Key` 时，同键同体重发会回放首次响应（并带 `Idempotency-Replayed: true`），同键换内容返回 `409`。键存在 `idempotency_keys` 表里（唯一索引抢占），不是进程内 map —— 多实例下只有共享存储才算数。
- **删除语义**：成功 `204`；重复删除 `404`（目标状态已经达成，客户端应当当成功处理）。
- **401 一定带 `WWW-Authenticate`**：`Bearer realm="moderator"` 或 `Bearer realm="comment-author", error="invalid_token"`，客户端据此知道该拿哪种凭据。
- **读待审队列要凭据**：`?status=pending|all` 只有带管理员口令才给，否则 `401` —— 静默返回空列表会让人以为没东西可审。
- **`X-Request-Id` 回显在响应头和错误体里**，访问日志按**路由模板**（`/api/v1/comments/{id}`）记录，不按具体路径。跨源调用时它还出现在 `Access-Control-Expose-Headers` 里，否则前端 `headers.get()` 永远是 null。

### 管理员与审计

- **两种管理员凭据**：`POST /admin/auth/login` 换来的会话 token（`adm_` 开头，首选），以及遗留的静态 `ADMIN_TOKEN`（应急通道，常量时间比较、优先尝试）。两者都能审核/删除评论；静态口令没配时相关路由只在有 admin 模块的情况下挂载。
- **会话是行，不是自包含 token**：库里只存 token 的 SHA-256 摘要。登出、停用账号、改密都会让下一次请求立即 401，而不是等 JWT 自己过期。过期是**绝对**的，不滑动——活跃的攻击者不该能把偷来的 token 一直续下去。
- **密码用 PBKDF2-HMAC-SHA256（600k 次）**，摘要自带 `scheme$cost$salt$key`，因此以后提高成本不需要迁移：旧行按自己的参数验证。未知用户名也会对固定哑摘要跑一次同样的派生，且所有失败一律 `401` 且不带 detail——否则响应时间或错误文案就能枚举账号。
- **自我保护**：不能停用或删除当前登录的账号，不能停用最后一个活跃管理员，不能删除最后一个账号（都是 `409` + 一句 detail）。改密会撤销该账号的**所有**会话，包括发起改密的那一个，客户端应回到登录页。
- **审计覆盖面**：登录、登出、账号增删改、改密、会话撤销、评论审核与管理员删除都写 `audit_logs`，带操作者、来源 IP/UA 和动作相关 detail。**失败登录只进服务日志、不落表**：否则任何人可以拿错误口令把审计表刷爆。
- **审计表没有指向账号的外键**：`actor_id` 保留但仍可 join，`actor_name` 是当时的用户名快照，账号删了以后这行仍然读得懂。
- 账号列表、会话列表、审计列表都是 `no-store`：它们要么带凭据，要么描述凭据。

### 创建管理员账号

账号和密码**存在数据库里**：`admin_accounts.username` 与 `admin_accounts.password_hash`。密码是 PBKDF2-HMAC-SHA256 摘要（`pbkdf2-sha256$cost$salt$key`），登录时重新派生再比较。

**开发环境（`APP_ENV=development`）启动时会自动确保默认管理员存在**：

```
账号：admin
密码：astroadmin123
```

登录页会直接显示这组初始凭据，直到密码被修改。它只出现在开发环境：生产不预置该账号、不保存明文、也不在登录页显示。这个账号被删除后，下次以开发环境启动会重新创建。

生产/正式环境请用下面的命令创建，密码不会进环境变量：

首次账号用同一个二进制创建（会先应用迁移，和启动路径一致）：

| 命令 | 作用 |
|---|---|
| `go run ./cmd/server admin create -username owner` | 创建账号，生成 24 位随机密码并只打印一次 |
| `echo '<密码>' \| go run ./cmd/server admin create -username owner -password-stdin` | 自己指定密码（管道，避免进 shell 历史） |
| `go run ./cmd/server admin passwd -username owner` | 重置密码并撤销该账号全部会话 |
| `go run ./cmd/server admin list` | 列出账号、状态与最近登录 |

- 没有 `-password` 参数：命令行参数会留在 shell 历史和 `ps` 输出里。
- 创建/重置都会写审计（操作者记为 `cli`），所以「谁在什么时候造了这个账号」查得到。
- `ADMIN_BOOTSTRAP_USERNAME/PASSWORD` 仍然保留，但它只是**自动化部署**的替代路径（比如容器首次启动）；手动部署请用上面的命令，不必把密码放进环境变量。

### 管理界面（/admin）

服务端渲染的控制台，模板内嵌在二进制里，没有 Node、没有构建步骤、没有外部静态资源：

| 页面 | 作用 |
|---|---|
| `GET /admin` | 首页仪表盘：待审/今日新增/已通过/垃圾四个数字、待审评论、最近活动（只列登录） |
| `GET /admin/login` | 登录表单；同一套账号体系，失败文案不区分账号不存在/密码错 |
| `GET /admin/comments` | 评论管理：待审/已通过/已拒绝/垃圾四个状态，支持搜索与批量操作 |
| `GET /admin/mail` | 邮件管理：SMTP 账号、发件人与收件人、两个通知开关都可编辑（存 `mail_settings`），并可当场测试发信 |
| `GET /admin/audit` | 最近活动：全量记录，可按 `action` 过滤，可勾选删除（首页只显示最近 8 次登录） |

**导航是「首页 / 评论管理 / 邮件管理」，账号入口收在右上角头像菜单里。** 点「账号设置」打开的是当前页上的模态框（原生 `<dialog>`，不依赖任何 JS 库），只有显示名和改密两个表单；没有账号列表、没有「新建管理员」、没有启停和删除按钮。多账号仍然可以通过 JSON API 完成，但页面上不提供——个人站点一个账号足够，每多一个账号就多一份要保护的凭据，也多一次把自己锁在外面的机会。

- **与 JSON API 是两套认证**：页面用 `HttpOnly; SameSite=Strict; Path=/admin` 的 cookie，API 仍只认 `Authorization: Bearer`。这样 API 不会因为浏览器自动带 cookie 而变成 CSRF 面。
- **CSRF 用同步令牌**：令牌放在另一个 HttpOnly cookie 里，服务端渲染进每个表单的隐藏域，POST 时常量时间比较。SameSite 是第一道防线，令牌是第二道——只靠其中任何一个都不够。
- **样式与脚本是独立资源**：`/admin/assets/app.css`、`app.js`、`theme.js`，`//go:embed` 进二进制，URL 带内容哈希、`Cache-Control: immutable`，并提供 ETag/304。页面本身不带内联 `<style>`/`<script>`/`style=`。
- **所有页面 `no-store`**，并带 `CSP: default-src 'none'; style-src 'self'; script-src 'self'`、`X-Frame-Options: DENY`、`Referrer-Policy: no-referrer`。删除确认从内联 `onsubmit` 移到了 `app.js`，所以不需要 `unsafe-inline`。
- **提醒可以临时关掉**：点掉「仍在使用初始密码」后，`app.js` 写一个 `admin_snooze_initial_password` cookie（`Path=/admin; Max-Age=600; SameSite=Strict`），服务端在 10 分钟内不再渲染这条提醒（以及登录页的初始凭据提示），到期后自动恢复。
- **表单是 POST + 303 重定向（PRG）**：刷新不会重复提交；错误用 `?err=<code>` 回到列表页，文案在服务端映射，URL 里不携带任意文本。
- **审核动作由控制台写审计**，账号动作复用 admin service 自己的审计，操作者都取当前会话的账号。
- 生产环境把 `ADMIN_COOKIE_SECURE=true` 打开；本地 `http://localhost` 必须保持关闭，否则浏览器不存 cookie，登录会静默失败。
- **通知统一是弹窗**：页面顶部居中（`position: fixed`）的 toast，全部会自动收起——成功 5 秒、信息 8 秒、警告 10–12 秒、错误 10 秒。鼠标悬停或键盘聚焦时倒计时暂停，`Esc` 关闭最新一条，也可以点 `×`；页面里不再有内嵌横幅。
- **自动收起不会替你静默提醒**：只有手动点 `×` 才会写 10 分钟的贪睡 cookie。倒计时结束只是收起这一条，下一次页面加载仍会重新出现——提醒的目的是被看到，不是被计时器吞掉。
- **初始密码提醒**：`admin_accounts.password_is_initial` 为 true 时，每个页面都会弹一条提醒并带「去修改」链接，直到账号自己把密码改掉。管理员在控制台重置别人的密码会把它重新置为 true——目标用户仍然需要自己选一个密码。API 的账号对象也暴露 `passwordIsInitial`。
- **开发环境的登录页会显示初始凭据**：当恰好一个账号还持有初始密码、且服务运行在开发环境时，登录页顶部直接显示「账号 admin / 初始密码 astroadmin123」。这是刻意的开发便利，登录页本身是未认证页面——**不要把开发环境的控制台暴露到公网**。
- **生产环境什么都不显示**：`APP_ENV=production` 时不预置默认账号、不写 `initial_password` 明文、登录页只给通用提示（账号由 `server admin create` 创建，忘记密码用 `server admin passwd` 重置）。
- **改密后提醒消失**：改密会同时清掉 `initial_password` 明文与 `password_is_initial` 标记，提示和登录页的初始凭据展示一起停止。
- **主题色是 `#37B880`**：`app.css` 顶部定义 `--brand-50…900` 八级色深，`--primary` 取 500，hover/active 取 600/700，浅色底和焦点环用同一色相的 `rgb(55 184 128 / …)`。组件只引用语义变量，改主题是改这一块。
- **外壳是「卡片 + 胶囊导航」**：吸顶半透明导航、圆角 16px 的卡片与面板、四张统计卡、右侧活动流。首页的四个数字（待审 / 今日新增 / 已通过 / 垃圾）来自一次 `GROUP BY status` 加一次按时间计数，没有引入图表库。
- **最近活动只列登录**：右侧活动流显示最近 8 次 `auth.login`（用户名、IP、相对时间），「全部 →」进入按该动作过滤的日志页。审核动作仍在完整日志里，因为「谁在什么时候删了哪条评论」需要能翻历史。
- **没有 CSS 框架**：`app.css` 是一套自带的设计系统，分三层——tokens（颜色/间距/字号/圆角/动效/层级）、base（重置、排版、焦点、滚动条）、components（按钮、输入、复选框、徽章、卡片、表格、分段筛选、批量栏、toast、弹窗、空状态）。类名用 `block__element` + `is-*`，模板里没有框架类。
- **深色模式是换令牌**：`theme.js` 在首屏前按 `prefers-color-scheme` 写 `<html data-theme="dark">`，`app.css` 只重写语义变量，组件规则不重复。
- **动效有档位**：120ms（状态）/200ms（表面）/280ms（浮层），统一 `cubic-bezier(.2,.8,.2,1)`；`prefers-reduced-motion` 下全部压到 1ms。
- **可访问性**：`:focus-visible` 焦点环、跳过导航链接、`aria-current`、toast 的 `aria-live`、表格表头 `scope`、图标 `aria-hidden`。
- **可用性**：所有交互元素有 `:focus-visible` 焦点环；按钮/输入有 hover、active、disabled 状态；表头 `scope`、当前导航 `aria-current`、跳过导航链接、toast `aria-live`；尊重 `prefers-reduced-motion`。页面不加载任何外部字体、图片或脚本，CSP 仍然是 `default-src 'none'`。
- **审核队列可以搜索**：关键词同时匹配作者名、邮箱、正文与文章 slug（`ILIKE`，`%` `_` `\` 已转义），并保留在状态切换、分页和审核动作的跳转里。搜索走的是同一份 WHERE 片段（`commentFilter`），列表和计数不会各说各话。
- **状态标签带数量**：四个状态各显示当前筛选下的条数（一条 `GROUP BY status`），一眼能看出「队列空了」还是「被筛选藏了」。
- 已知取舍：审核队列每页 50 条带「下一页」、删除按钮用原生 `confirm()`。评论正文按纯文本转义展示，不渲染 Markdown。

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
| `ADMIN_TOKEN` | 空 | 遗留的审核 Bearer 口令，≥16 字符；为空时静态口令通道关闭（admin 会话仍可用） |
| `ADMIN_SESSION_TTL` | `168h` | 管理员登录有效期。绝对过期，不滑动；服务端会把它夹在 15 分钟 ~ 30 天之间 |
| `ADMIN_BOOTSTRAP_USERNAME` | 空 | 可选：自动化部署时引导首个账号；手动创建请用 `server admin create`。与下一项必须同时设置 |
| `ADMIN_BOOTSTRAP_PASSWORD` | 空 | 可选：引导账号的密码，≥12 字符；账号表非空时忽略 |
| `ADMIN_COOKIE_SECURE` | `false` | `/admin` 控制台 cookie 是否加 `Secure`。HTTPS 部署必须打开；本地 http 必须关闭 |
| `SPAM_PROVIDER` | `none` | 反垃圾来源：`none` / `akismet` / `llm`。启用后只可能把评论标成垃圾，不会自动发布 |
| `AKISMET_KEY` | 空 | `SPAM_PROVIDER=akismet` 时必填；启动时会 `verify-key` 一次 |
| `AKISMET_SITE_URL` | 空（回落到 `SITE_URL`） | Akismet 站点地址，必须与注册时一致 |
| `LLM_ENDPOINT` | 空 | OpenAI 兼容的 `/chat/completions` 地址；`SPAM_PROVIDER=llm` 时必填 |
| `LLM_MODEL` / `LLM_API_KEY` | 空 | 模型名与密钥；本地模型可以不给密钥。`LLM_TIMEOUT` 默认 `15s` |
| `MAIL_HOST` / `MAIL_PORT` / `MAIL_TLS` | 空 / `587` / `auto` | **只用于首次导入**：邮件设置存在 `mail_settings` 表里、由 `/admin/mail` 编辑；这组环境变量仅在表里还没有那一行时被写入一次。`MAIL_ENABLED` 已废弃（没有总开关了） |
| `MAIL_USERNAME` / `MAIL_PASSWORD` | 空 | 同上，仅首次导入。明文连接下 `PlainAuth` 只对 localhost 生效 |
| `MAIL_FROM` / `MAIL_FROM_NAME` | 空 / `SITE_NAME` | 同上，仅首次导入。发件人邮箱与显示名 |
| `MAIL_ADMIN` | 空 | 同上，仅首次导入。管理员收件邮箱，多个用逗号分隔 |
| `SITE_NAME` / `SITE_URL` | `Astro Liomix` / 空 | 邮件标题与正文里的站点名、文章链接前缀（仍来自环境变量） |

### 批量操作

评论管理与最近活动都有批量入口（`POST /admin/comments/bulk`、`POST /admin/audit/bulk`）：

- 复选框带 `data-bulk-id`，提交时由 `app.js` 收集成本表单里的 hidden `ids`；不用 HTML5 的 `form="..."` 关联（浏览器表现不稳定），也不用嵌套表单（无效 HTML）。
- 每个动作是一个按钮（`name="action"`），点一次就提交；删除按钮带二次确认。评论侧支持 通过 / 拒绝 / 标记垃圾 / 删除（含回复）。
- 一次最多 100 条（`comment.MaxBulkIDs` / `admin.maxAuditDeleteIDs`），与一页的容量一致。
- 批量审核只对**状态真正发生变化**的评论发通知：重复点「通过」不会反复给作者发信。
- 审计只记一条 `comment.bulk.<action>`，detail 里同时记 `requested` 与 `affected`：审计要能回答「点了多少、实际改了多少」。
- 活动记录的删除**本身也写审计**（`audit.delete`，detail 同样是 requested/affected）：把日志删短这件事，日志里得留得下痕迹。它只用于清掉误记的条目，不是保留策略——保留策略见待办。

### 反垃圾

`comment.Moderator` 是唯一的接入点，`cmd/server/spam.go` 把第三方过滤器适配成它：

- **Akismet**：`comment-check`，需要客户端 IP（`X-Forwarded-For` 一律不信任，取 `RemoteAddr`），启动时先 `verify-key`。
- **LLM**：OpenAI 兼容接口，提示词要求返回 `{"spam":bool,"reason":"..."}`；解析器容忍代码块和前后解释，模型没给出 `spam` 字段就当失败。
- 过滤器**只能降级**：判定垃圾 → 存成 `spam`；判定正常 → 仍然是 `pending`。让第三方决定「发布」，等于把站点交给它。
- 过滤器不可用（超时、429、字段缺失）→ 记 warning，评论保持 `pending`，与没有过滤器时完全一致。

### 邮件通知

- **SMTP 配置在后台编辑**：服务器地址、端口、加密方式、用户名、密码/授权码、发件邮箱与名称、管理员收件邮箱（可多个）都存在 `mail_settings` 的单行里，页面保存即生效——`SettingsSender` 每条消息前读一次配置，改端口或换密码不需要重启。
- **环境变量只负责首次导入**：库里还没有那一行时，`MAIL_*` 会被写进去一次；之后页面就是唯一事实来源，避免部署脚本里的旧密码悄悄覆盖后台刚改的值。
- **密码只写不读**：页面永远拿不到已保存的密码，留空即「不修改」；审计只记 `passwordChanged: true`，不记值。
- **邮件模板可在后台粘贴整段 HTML**（`mail_settings.template_admin` / `template_reply`）：留空用内置模板，填了整封 HTML 就换成你的。变量用 `{{siteName}} {{postSlug}} {{postURL}} {{authorName}} {{authorEmail}} {{parentAuthor}} {{body}} {{consoleURL}} {{time}}`，做的是**字符串替换**（不是 `html/template` 解析，避免模板里的 `{{` 误伤），值一律先 HTML 转义；未知变量原样保留，写错能看见。纯文本部分仍用内置文案，给不显示 HTML 的客户端兜底。
- **两个通知开关，默认都开**：新评论提醒管理员（发到收件邮箱列表）、回复提醒被回复者。没有总开关——取消勾选就是关掉那一类邮件。评论**通过审核**那刻才发信，待审和拒收不发。
- **测试发信是同步的**：走已保存的设置真连一次，失败直接把错误显示在页面上，而不是塞进队列报个「已发送」。
- 自己回复自己、或父评论没留邮箱时跳过；每封邮件都带文章链接。
- 发送是**异步队列**（`internal/platform/mail`，容量 256）：入队不阻塞请求，队列满就丢弃并记 warning；进程退出时最多等 5 秒把队列发完。SMTP 有连接/写超时，邮件服务器挂住不会拖住评论接口。

### 数据库

- 只要求**库存在**，表、约束、索引都由迁移建；已应用的迁移记在 `schema_migrations`，重复启动是空操作。
- 迁移按文件名单调执行，每个文件一个事务：语法错误会让整个文件回滚，数据库停在上一个版本而不是半迁移状态。
- 每个迁移事务都带 `SET LOCAL lock_timeout = '5s'`：拿不到表锁就让进程失败退出、由外部重启重试，而不是排在一条慢查询后面把所有请求一起堵死（PostgreSQL 的锁队列是先进先出）。
- 这个执行器在事务里跑 DDL，所以 `CREATE INDEX CONCURRENTLY` 用不了。评论表还小，普通建索引是瞬时的；真要在线给大表加索引，得手工单独执行，别写进迁移文件。
- 多实例同时启动**由咨询锁串行化**（`pg_try_advisory_lock` + 有上限的等待）：每次部署都是几个实例一起起来，光靠 `CREATE TABLE IF NOT EXISTS` 挡不住「两个进程同时给同一张表加约束」——第二个会撞 `duplicate_object`，然后进程退出、被反复重启。拿不到锁时等 30 秒后报「另一个进程正在迁移」，失败比挂住好。整个迁移跑在一条**固定的连接**上，锁和工作在同一个会话里；解锁的 ctx 与调用方解耦，被取消也要把锁还回去（会话级咨询锁会跟着连接留在池里）。
- 删除评论的级联由 `parent_id ... ON DELETE CASCADE` 完成，服务层不遍历子树；`parent_id` 上补了独立索引，否则每次级联都要全表扫一遍（PostgreSQL 只给主键和唯一约束建索引，外键列要自己加）。
- 列表与计数共用一个复合索引 `(post_slug, status, created_at DESC, id DESC)`：过滤、排序、聚合都走它，不需要「先按 slug 取全部再排」的两段式。
- 审核队列是**跨文章**读的（`WHERE status = ? ORDER BY created_at DESC, id DESC`），所以另有一条 `(status, created_at DESC, id DESC)`；没有它，队列查询会因为索引第一列是 slug 而退化成全表扫描加排序。仓储的 slug 过滤因此改成可选：空 slug 表示全部文章。
- 「没有删除凭据」在库里是 `NULL` 而不是空字符串，配一条 `CHECK (delete_token_hash ~ '^[0-9a-f]{64}$')`；领域层仍用空串表示缺席，边界上互转。
- 字段上限在库里也有一份 `CHECK`（slug 200、姓名 80、邮箱 254、网址 200、正文 2000 字符），服务层校验失效或被脚本绕过时数据库仍然拒绝。
- 评论数用一条 `GROUP BY` 聚合回答整批 slug，不靠 `len(List(...))`。
- `timestamptz` 读出后统一转 UTC，接口的时间字符串不随服务器时区变化；Postgres 精度是微秒。
- 连接串进日志前会脱敏（`postgres.Redact`），启动失败的错误里不会带密码。
- 每个连接都带**服务端超时**：`statement_timeout=10s`、`lock_timeout=3s`、`idle_in_transaction_session_timeout=60s`，外加 `application_name=astro-liomix-server`（`pg_stat_activity` 里能认出是谁）。它们作为启动参数随连接串下发，不是连上之后 `SET` 一次——池里的连接一条都没 SET 过。连接串里已经写了的参数优先，运维想自己钉就用 DSN。
- `created_at` / `updated_at` 有 `DEFAULT now()`。服务始终显式给值（时钟可注入才测得了时间相关的用例），默认值是给**别的写入方**用的：修复脚本、导入、`psql` 手敲。没有它，这些写入会卡在 `NOT NULL` 上，然后那个人从自己终端里抄一个时间进来——正是我们不信任的那个钟。
- 表结构的设计理由写在库里（`COMMENT ON`），`\d+ comments` 就能回答「主键为什么是字符串」「这个索引服务哪条查询」，不用去翻文档或问人。
- `post_views` 补了 `created_at`（原来只有 `updated_at`，「这篇文章第一次被读是什么时候」答不了）。存量行按 `updated_at` 回填——那是上界，不是真实首次时间；用 `now()` 回填等于声称所有文章都是今天第一次被读。
- 表设计复核过 [timescale/pg-aiguide](https://github.com/timescale/pg-aiguide) 的 PostgreSQL 规范：没有满足 hypertable 条件的时间序列表（评论、访客、阅读量都是「一行一实体」，不按时间追加），所以没引入 TimescaleDB。
- - 管理员的表分成 `admin_accounts` 与 `admin_sessions`，而不是账号表里放一个「当前 token」列：一个人可以同时用笔记本和手机登录，注销其中一个不该把另一个也踢下线。
- `admin_sessions.token_hash` 是唯一索引，也是每次鉴权的唯一一次查询；库里不存明文 token，所以数据库被拖走也不能拿来当会话用。
- `admin_accounts.username` 有 `CHECK` 钉住小写与字符集，另有唯一索引：服务层的校验被脚本绕过时，数据库仍然拒绝第二个同名账号。`password_hash` 的 `CHECK` 只校验编码形状，不校验内容——内容由 PBKDF2 保证。
- `expires_at` 是绝对时间，配一条 `CHECK (expires_at > created_at)`；启动时按 `expires_at` 清一次过期会话。已撤销的行留到过期才删，因为审计日志还要能点名是哪个会话做的操作。
- `audit_logs` 只追加、不修改：`actor_id` 故意**不建外键**（账号删了，历史还要能读），`actor_name` 存当时的用户名快照，`detail` 用 `jsonb` 以便 `detail->>'status'` 这类查询。三个索引分别服务「全量倒序」「按 action 过滤」「按 actor 过滤」。控制台能删掉勾选的条目（误记清理），每次删除都追加一条 `audit.delete`。
- 审计写入是尽力而为：写失败只记一条 warning，不让「审计表满了」把审核动作变成 500。
- `admin_accounts.initial_password` 是唯一保存明文密码的地方，用途只有一个：开发环境的登录页展示初始凭据。账号自己改密会把它清空，管理员重置会写入新值；**只有开发环境会写它**（`WithPlaintextInitialPassword`），生产始终为 NULL。它是登记在案的安全取舍：在那段时间里，任何能打开登录页或读到库的人都能拿到这个密码。

应用与迁移**应当是两个角色**：迁移要 DDL，应用只要 DML。`configs/roles.sql` 里有幂等的建角色与授权脚本（`psql -v app_password=… -f configs/roles.sql`），需要时执行。目前开发环境仍是 `postgres` 超级用户，属于登记在案的偏离（见下）。

#### 有意保留的偏离

按 `docs/PostgreSQL规范.md` 核对过，下面几处**不打算改**，理由记在这里：

| 规范条目 | 本项目的做法 | 理由 |
|---|---|---|
| 3.3 对外 id 与内部主键分开 | `comments.id` 直接用 32 位随机 hex 当主键 | 它本来就是对外 id，评论没有第二个查询键；加 `bigint` 主键 + `public_id` 要多一列、多一次转换，在这个数据量上换不来任何东西 |
| 3.4 自然键做 `UNIQUE` 不做主键 | `site_visitors.visitor_id`、`post_views.slug` 当主键 | 这两张表的「身份」就是那个值，没有其他属性；主键就是「唯一 + 非空」的强化版 |
| 2.2 id 用 `bigint` | `site_counters.id` 是 `smallint`，恒等于 1 | 单行哨兵，`CHECK (id = 1)` 已经钉死，宽度没有意义 |
| 1.4 每张业务表都要 `created_at` | `site_visitors` 用 `first_seen` | 语义更准（它记的就是「第一次见到这个访客」），改名不会让任何查询更好读 |
| 12.1 应用账号最小权限 | 开发环境仍连 `postgres` | 部署侧决策（生产用哪个角色、密码怎么管）；脚本已备好，见 `configs/roles.sql` |

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

当前覆盖率：`admin` 76.6%、`comment` 90.7%、`count` 93.8%、`audit` 84.2%、`cursor` 100%、`config` 94.6%、`httpkit` 82.7%、`postgres` 77.2%、`idempotency` 71.4%、`id` 80%。

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
- 认证与授权：不透明会话 token + 摘要存储（可撤销），登录失败不区分原因；特权操作写审计，审计表只追加、容忍写入失败，删记录也留痕
- 库表、索引、事务与迁移按 `docs/PostgreSQL规范.md`：`timestamptz` / `text` + `CHECK` / 外键列手工建索引 / 迁移一文件一事务且带 `lock_timeout`

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
- [ ] 限流：发评论、读计数、**管理员登录**都还没有速率限制，公开接口可能被刷（要按端点分级，且计数在多实例下需要共享存储；进程内桶只会在单实例时看起来有效）
- [ ] 幂等键的清理目前只在启动时 `Prune` 一次，长期运行应改成定时任务（`idempotency_keys` 只增不减）
- [ ] 管理员会话清理同样只在启动时跑一次；审计日志 `audit_logs` 还没有保留策略（目前只有控制台里手动勾选删除）
- [ ] 多角色 RBAC：现在刻意是**单角色**——每个管理员都能审核、管账号、读审计，靠「不能动自己 / 不能动最后一个」兜底。需要分级时再加角色与权限点
- [ ] `/readyz` 目前只看进程存活，应加一次数据库 ping
- [ ] 已有三个领域模块，`comment`、`count`、`admin` 按 2.3 评估是否拆子包
