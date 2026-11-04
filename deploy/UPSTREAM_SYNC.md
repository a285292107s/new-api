# 上游同步策略（UPSTREAM SYNC）

本文件规定本 fork 如何跟进上游 `QuantumNous/new-api`。它和 `deploy/PINS.md`（digest ↔ commit 配对）、
根目录 `MODIFICATIONS.md`（AGPL 改动清单）一起构成发布审计链。

| 项 | 值 |
| --- | --- |
| 最后全量同步点 | `ba5f7367`（2026-11-03，`upstream/main` tip） |
| 对应生产镜像 | `sha256:12e7b5ede2533cb32b453fd492ceae31fa187d3110c475bf0a332e26298062e3` |
| 同步起点 | `ba5f7367..upstream/main`（当前增量为 0） |
| 上一上游基线 | `e835b1b`（`v1.0.0-rc.47`，2026-10-24） |

## 1. 原则：分层同步，而不是整体挑选

上游近 4 个月 634 个提交（≈158/月），不可能也不应该全跟。按代码性质分三层，各用各的规则：

| 层 | 范围 | 上游量（4 个月） | 规则 | 节奏 |
| --- | --- | --- | --- | --- |
| 协议层 | `relay/`、`relaykit/`、`tokenkit/`、`pkg/`、`constant/` | 171（relay+relaykit） | **不挑，整块跟**。我们对这块零改动（相对上游 diff 为空） | 每季度跟一个上游 tag |
| 安全层 | `common/`、`middleware/`、`service/`、依赖与 Go 版本 | ≈116 + `go.mod` 22 次 | **必跟，不挑**。涉及 API key、登录态、额度扣减 | 每周扫，发现即 backport |
| 控制面 | `model/`、`controller/`、`router/`、`setting/`、嵌入式 `web/` 管理台 | 101 + 144 + 43 + 42 + 310 | **按需挑**。产品决策权在本项目 | 每月集成一次 |

一句话：**用户连得上的协议和安全跟着上游走，用户看得见的产品和页面由本项目决定。**

`web/` 占上游提交近一半（310/634），管理台只是内部工具，**默认只取安全修复，不跟功能改版**，这一条就砍掉一半同步工作量。

## 2. 节奏

| 频率 | 动作 | 耗时 |
| --- | --- | --- |
| 每周 | 看上游 `v1.0.0-rc.*` Release notes + GitHub 安全公告，标出必须 backport 的条目；不做集成 | 15 分钟 |
| 每月 | 按第 4 节跑一次完整集成（cherry-pick + 验收 + 发版） | 半天～1 天 |
| 每季度 | 在 `sync/rehearsal` 分支做一次全量合并演练，防止 merge-base 腐烂 | 半天 |

上游发版约每周一个 rc（`rc.44` 10-12 → `rc.47` 10-24），所以「每月看一次」等于一次面对 4 个版本、约 158 个提交，必须先用 Release notes 过滤，不要逐条读。

## 3. 硬规则

1. **依赖链**：单个提交改动 >20 个文件、或动 `model` 表结构、或改 `router` 核心路由的，要么把整串拿进来，要么整串不拿；不允许只取中间一段。
2. **schema**：任何带 `AutoMigrate`/表结构变化的提交，必须在「全新空库」和「生产库 dump 恢复」两种环境下各启动一遍，并连续启动两次验证幂等。
3. **钱路径**：碰 `model/quota*`、`service/quota*`、计费、日志扣费的提交，必须真的发一次 `/v1/chat/completions` 验证扣费、倍率、日志三者一致。
4. **前端**：`web/`、`electron/` 的功能提交默认跳过；只取安全与构建修复，且合并后必须 `cd web && bun run build` 通过。
5. **协议层**：不做逐 commit cherry-pick，按 release tag 整目录同步 `relay/`、`relaykit/`、`tokenkit/` 及其依赖的 `pkg/`、`constant/`，让编译器指出还缺哪些 `model` 类型。
6. **安全层**：不存在「不合适」的安全修复，只有「先 backport 还是等下次集成」的区别；等待上限一个月。

## 4. 月度 SOP

```powershell
git fetch upstream --tags
git checkout -b sync/2026-11 upstream/main   # 以最新上游为底，再把自有补丁放上去
```

1. **列候选**（排除前端、Electron、文档）：

   ```powershell
   git log --oneline --no-merges upstream/main --since="1 month ago" -- . ':!web' ':!electron' ':!docs' ':!*.md'
   ```

2. **分拣成四类**：`安全` / `修复` / `功能` / `跳过`。对照 Release notes，不看提交标题猜内容。
3. **按主题 cherry-pick**，一律带 `-x`（保留上游 sha，台账可回查）：`git cherry-pick -x <sha>`。按 `relaykit` / 计费 / 安全 分组，每组单独验证。
4. **过验收门**（第 5 节），任何一组不过就回退该组，不要带着问题继续下一组。
5. **合并回生产分支**，PR 描述里贴台账行；CI 出镜像，更新 `deploy/PINS.md`。
6. **更新 `MODIFICATIONS.md`**（如引入的行为变化影响用户可见语义）。
7. **记台账**（第 6 节）。

## 5. 验收门（每批必过）

```powershell
go build ./...
go vet ./...
go test ./model/ ./controller/ ./relay/... ./middleware/ ./service/...
```

若改到 `relaykit/` 或 `tokenkit/`，在对应模块内再跑：`GOWORK=off go build ./...`。

staging 冒烟清单（缺一不可）：

| 项 | 通过标准 |
| --- | --- |
| 登录 / 刷新 | 用户中心登录成功，refresh cookie 正常轮换 |
| 一次真实中转 | `/v1/chat/completions` 200，余额扣减与日志金额一致 |
| 令牌 CRUD | 建令牌、复制 key、删除均正常 |
| 日志 | 用户中心日志页能增量刷新，金额/模型正确 |
| 兑换 | 兑换码入账，余额与流水一致 |
| 管理台 | `loli.mydeepseek.top` 以 `role >= 10` 账号登录，用户中心内「用户管理」页可读；无独立 admin 子域 |
| 数据库 | 空库启动 + 生产 dump 恢复启动，均无迁移报错 |

## 6. 台账

追加在下方表格，一行一个处置决定。`处置` 只能是 `并入` / `暂缓` / `放弃`。

| 日期 | 上游范围 | 层 | 上游 commit | 类型 | 处置 | 理由 | 验证 | 发版 digest |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 2026-11-03 | 全量至 `ba5f7367` | 全部 | `40a658af`（merge） | 基线 | 并入 | 生产线合并上游 `ba5f7367` | health=healthy，`/api/status`、`/api/pricing`、`/` 200 | `12e7b5ed…` |
| 2026-11 | `ba5f7367..` | 协议 | — | — | 待办 | 下个窗口从零开始，按季度整块跟 | — | — |

## 7. 指标与退出条件

每月记录三个数字，写进台账表尾：

- **落后上游天数** = 今天 − 上游最新提交日期，目标 ≤30 天。
- **自有 diff** = `git diff --shortstat upstream/main...HEAD` 的文件数与行数，当前 32 文件 / +2211 −22。
- **每次集成的冲突文件数**。

触发下面任一条件，说明「挑选式同步」已到极限，改用「上游 release tag + patch series」（仓库只放 `patches/*.patch`，CI 拉 tag 后 apply）：

- 连续两个季度全量合并演练都合不动；
- 上游对 `relaykit` 做协议层大版本重写；
- 自有 diff 涨到 5000 行以上，或落后上游超过两个月。

## 8. 首次演练：2026-10-03 ～ 2026-11-03

该窗口共有 **138 个**非前端/非文档提交（全量 634 个）。这篇区间的分拣结果就是第 4 节的样板：

| commit | 标题（截断） | 分类 | 处置 |
| --- | --- | --- | --- |
| `7d8b9126` | upgrade Go to 1.26.10 for CVE-2026-111943 | 安全 | 必跟 |
| `3150dfea` | bump go-jose/v4 4.1.4 → 4.1.6 | 依赖安全 | 必跟 |
| `9b473a3f` | 2FA 锁定过期后重置失败计数 | 安全 | 必跟 |
| `30748c65` | 隐藏访问日志中的查询凭据 | 安全 | 必跟 |
| `9823ebc7` | 按 prompt_tokens_details 计费 cache_creation | 钱路径 | 必跟 |
| `ea343c4a` | 预留/任务写入失败时仍对流计费 | 钱路径 | 必跟 |
| `55e2ae54` | 每次重试重新定价并先预留 | 钱路径 | 必跟 |
| `0165fef9` | relaykit：保留 Gemini 文本与工具调用共存 | 协议 | 整块跟 |
| `52c17303` | Gemini Argon 字段与 reasoning 映射 | 协议 | 整块跟 |
| `94b52166` | Vertex 每渠道 v1beta1 端点开关 | 兼容 | 按需（用户用到才要） |
| `6cf0d90b` | DeepSeek V4.1 Flash / Search / Fetch | 用户可见 | 按需（产品决定） |
| `4013729f` | 敏感词过滤默认关闭并清空词表 | 行为变更 | 谨慎（先确认本站策略） |
| `128089fa` | 前端与 AI 客户端错误消息 i18n | 管理台 | 跳过 |
| Electron 依赖 bump（`7fa815ed` 等） | — | 桌面端 | 跳过 |
| Kling/Veo/Vidu 插件（`ee343c4a` 等） | — | 插件 | 跳过（未启用） |

## 9. 附：分拣命令速查

```powershell
# 只列安全相关
git log --oneline --no-merges upstream/main --since="1 month ago" -i --grep="security\|cve\|vuln\|auth\|2fa\|credential\|xss\|csrf"

# 只看钱路径
git log --oneline --no-merges upstream/main --since="1 month ago" -- model/quota*.go service/quota*.go service/*billing*.go relay/helper/price.go

# 只看协议层
git log --oneline --no-merges upstream/main --since="1 month ago" -- relay/ relaykit/ tokenkit/

# 只看某个 tag 之后
git log --oneline --no-merges v1.0.0-rc.47..upstream/main -- . ':!web' ':!electron' ':!docs'

# 当前落后多少
git rev-list --count HEAD..upstream/main
git diff --shortstat upstream/main...HEAD
```
