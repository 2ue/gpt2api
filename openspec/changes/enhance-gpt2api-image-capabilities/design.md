## Context

`gpt2api` 当前图片链路主要集中在以下位置：

- [internal/gateway/images.go](H:\images\gpt2api\internal\gateway\images.go)
- [internal/image/runner.go](H:\images\gpt2api\internal\image\runner.go)
- [internal/scheduler/scheduler.go](H:\images\gpt2api\internal\scheduler\scheduler.go)
- [internal/gateway/images_proxy.go](H:\images\gpt2api\internal\gateway\images_proxy.go)

当前结构的问题并不是“功能缺几个字段”，而是图片系统边界定义不清：

- handler 既做语义解释、又做调度假设、又直接绑定 reverse 执行。
- reverse 实现承载了过多职责，native 和 Responses 无法以稳定边界接入。
- 账号池和任务模型默认假设 provider 只有 reverse 一种。
- 接口字段和 provider 能力没有共享真值表，导致伪兼容。

其它项目里有明确值得吸收的点：

- `sub2api`: 能力分流、同账号重试、跨账号 failover。
- `chatgpt2api`: 轻量 `reference_images` 输入兼容。
- `CLIProxyAPI`: 把协议层与执行层切开，适合用来定义第三 provider 的抽象边界。

本次设计坚持两个原则：

- `gpt2api` 继续作为图片主干，不迁入其它项目的平台层。
- Responses 路线不再只是“提一下 fallback”，而是既能参加统一调度、又能独立直调的正式第三 provider。

## Goals

- 保留 `gpt2api` 现有 reverse 图片优势，尤其是参考图上传、IMG2 命中和 `/p/img/...` 代理能力。
- 修复编辑、图生图、多图、`b64_json`、native options 等语义缺陷。
- 将图片链路重构为：
  `request normalization -> support matrix -> route planning -> provider-aware scheduling -> provider executors -> unified outputs`
- 让图片请求具备同账号重试和跨账号 failover。
- 让 Responses `image_generation` tool 同时具备自动候选和独立直调两种入口。
- 保证新旧任务和旧图片代理 URL 的兼容读取。

## Non-Goals

- 不迁移 `sub2api` 的通用网关、订阅、计费系统。
- 不引入 `CLIProxyAPI` 的整套 provider 框架。
- 不把整个图片系统改造成 Redis Stream 异步 worker。
- 不在本次改造中接入非 OpenAI 体系的图片供应商。
- 不把 `/v1/images/variations` 作为本次交付目标。

## Decisions

### 1. 使用 canonical image request 作为统一入口

新增 `internal/image/request.go`，定义 `CanonicalImageRequest`，统一承载：

- `Operation`: `generate` / `edit`
- `Model`
- `Prompt`
- `N`
- `Size`
- `ResponseFormat`
- `RoutePolicy`
- `BaseImages`
- `ReferenceImages`
- `Mask`
- `Quality`
- `Style`
- `Background`
- `OutputFormat`
- `OutputCompression`
- `Moderation`

这样可以把“接口长什么样”和“语义是什么”分开。

### 2. 引入显式 support matrix

新增 `internal/image/support_matrix.go`，用统一规则判断 provider 对请求形态的支持状态：

- `supported`
- `emulated`
- `rejected`

最少覆盖这些维度：

- prompt-only generation
- generation with `reference_images`
- edit with `image`
- edit with `image + mask`
- `n > 1`
- `response_format=b64_json`
- native-only options present

handler 校验、route planner 和 executor 前置检查共用同一份规则。

### 3. Runner 内部重构为 orchestrator

保留 `image.Runner` 对外入口，但内部变成 orchestrator，并拆成三个执行器：

- `ReverseExecutor`
- `NativeOpenAIExecutor`
- `ResponsesToolExecutor`

执行器职责：

- `ReverseExecutor`
  - 复用现有 ChatGPT reverse 协议链路
  - 保留参考图上传、会话追图、IMG2 命中、图片下载 URL 获取
- `NativeOpenAIExecutor`
  - 调用原生 Images API
  - 正确传递 `mask`、`quality`、`style`、`background`、`output_format`、`output_compression`、`moderation`
  - 支持 `url` / `b64_json`
- `ResponsesToolExecutor`
  - 调用 Responses API 的 `image_generation` tool
  - 作为统一路由候选 provider
  - 作为独立接口的强制 provider

### 4. route policy 明确区分风险偏好

新增图片 route policy：

- `auto`
  - 默认策略
  - 基于请求能力和 provider 能力自动选路
- `safe`
  - 不允许 reverse
  - 只允许 native 和 responses
- `responses`
  - 强制 Responses provider

其中 direct Responses 接口始终强制 `responses`。

### 5. 图片选路规则以“语义正确优先”为准

route planner 的默认规则：

- `edit + mask`:
  - 仅 native
- `edit` without mask:
  - native 优先
  - reverse 作为兼容 fallback
  - responses 仅在兼容矩阵允许时参与
- `generate + reference_images`:
  - reverse 优先
  - native 可通过 edit 语义承接
  - responses 可作为兼容候选
- `generate + prompt-only`:
  - reverse 优先
  - native 与 responses 作为自动候选
- `response_format=b64_json`:
  - reverse 不参与
  - 由 native / responses 承接
- native-only options present:
  - 强制 native

### 6. provider-aware scheduling 独立于 chat 调度

调度层新增 `DispatchSpec`，最少包含：

- `Operation`
- `PreferredProvider`
- `AllowedProviders`
- `PreferredAccountID`
- `ExcludedAccountIDs`
- `NeedMaskSupport`
- `NeedB64JSON`
- `NeedReferenceImages`
- `NeedNativeOptions`

账号租约 `Lease` 扩展为同时承载：

- reverse 所需：
  - `AuthToken`
  - `DeviceID`
  - `SessionID`
  - `ProxyURL`
- native / responses 所需：
  - `APIKey`
  - `APIBaseURL`

同账号重试和跨账号 failover 都由 orchestrator 驱动，不放在 handler。

### 7. 账号模型显式区分 provider kind

`oai_accounts` 扩展以下字段：

- `provider_kind`
- `api_key_enc`
- `api_base_url`
- `image_capabilities_json`
- `same_account_retry_limit`
- `priority`

原则：

- reverse 账号继续保留 AT/RT/ST 逻辑。
- native / responses 账号使用 `api_key` 系列字段。
- 刷新器和 quota probe 只处理 reverse 账号。
- provider eligibility 不再依赖旧的 `account_type` 推断。

### 8. 任务与产物模型统一，但保留 legacy 兼容

`image_tasks` 扩展：

- `operation`
- `provider_kind`
- `route_policy`
- `request_options_json`
- `attempt_count`
- `switch_count`

新增 `image_task_outputs`：

- `task_id`
- `output_index`
- `source_type`
- `source_ref`
- `content_type`
- `revised_prompt`
- `meta_json`

`source_type` 取值：

- `chatgpt_ref`
- `stored_blob`
- `remote_url`

兼容策略：

- 旧 reverse 任务继续使用 `file_ids` / `conversation_id` 读取。
- 新 provider 结果使用 `image_task_outputs`。
- `/p/img/...` 优先走新产物表，缺失时回退到 legacy reverse 路径。

### 9. 本地图片产物存储作为统一落点

新增本地 artifact storage：

- native `b64_json` 输出直接落盘
- native / responses 返回临时 URL 时先下载再落盘
- 响应 `response_format=url` 时继续返回 `/p/img/...`
- 响应 `response_format=b64_json` 时直接从本地 artifact 读回 base64

这样 Responses 路线既能独立返回，也能统一进入任务查询和代理体系。

### 10. Responses 路线的契约和能力矩阵

Responses 路线必须满足两件事：

- 接口契约与现有 `/v1/images/generations`、`/v1/images/edits` 一致
- 真实能力边界由支持矩阵约束，不做静默降级

本次设计中的 Responses 支持矩阵：

| 请求形态 | Responses 支持状态 | 说明 |
| --- | --- | --- |
| `generate + prompt-only` | Supported | 适合显式直调的低风险路线 |
| `generate + reference_images` | Emulated | 通过 input image 内容映射 |
| `edit + image` | Supported | 通过 tool 的 edit 语义映射 |
| `edit + image + mask` | Rejected | 未确认稳定 mask 语义前显式拒绝 |
| `n > 1` | Emulated | 由 orchestrator fan-out |
| `response_format=b64_json` | Supported | 统一产物后回填 base64 |
| native-only options | Rejected | 不做伪兼容 |

### 11. 计费策略保持模型侧统一，但记录 provider 元数据

图片计费继续沿用当前模型定价：

- 以 `models.image_price_per_call * n` 为主
- 不在本次改造中引入 provider 级差异化计价

但执行元数据必须补齐：

- `provider_kind`
- `route_policy`
- `attempt_count`
- `switch_count`

这样后续如果要做 provider 维度计费或成本核算，不需要再翻修任务模型。

### 12. rollout 与回退是正式约束

保留并实现以下控制能力：

- `reverse-only`
- `native-enabled`
- `responses-fallback-enabled`
- `responses-direct-enabled`
- `safe-mode`

同时记录这些观测维度：

- route decision
- provider kind
- same-account retry count
- account switch count
- result source type
- compatibility mode

## Risks

- provider 数量从 1 增长到 3，链路复杂度上升。
- native / responses 结果风格与 reverse 不同，默认路由变化会影响用户感知。
- 新任务模型和代理逻辑如果不保留 legacy 回退，历史任务会直接失效。
- provider 账号模型扩展会触达 admin、DAO、scheduler、refresher 等多处代码。

## Mitigations

- 以 orchestrator / executor / storage 三个边界隔离复杂度。
- 所有 provider 支持关系通过 shared support matrix 管理。
- legacy reverse task 继续通过 `file_ids` 和 `conversation_id` 工作。
- refresh / quota probe 只对 reverse 账号生效，避免影响 native / responses。
- direct Responses route 和 auto routing 复用同一执行器，避免两套图片实现分叉。

## Implementation Shape

建议代码落点：

- `internal/image/request.go`
- `internal/image/support_matrix.go`
- `internal/image/provider.go`
- `internal/image/orchestrator.go`
- `internal/image/executor_native.go`
- `internal/image/executor_responses.go`
- `internal/image/storage.go`
- `internal/image/output.go`
- `internal/upstream/openai/*`
- `sql/migrations/*`

## Acceptance

方案完成后应满足：

- `/v1/images/generations`、`/v1/images/edits` 不再只会走 reverse。
- `/v1/images/responses/generations`、`/v1/images/responses/edits` 可独立使用。
- masked edit 不再被 reverse 静默吞掉。
- `response_format=b64_json` 通过 native / responses 真正返回 base64。
- legacy reverse 历史任务与 `/p/img/...` 不回归。
- OpenSpec 文档本身可以独立指导 schema、路由、provider、代理和兼容迁移实现。
