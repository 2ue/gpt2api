## Why

`gpt2api` 当前的图片链路在目录内仍然最深，尤其是 ChatGPT reverse 生图、图生图命中、图片代理下载和任务查询已经成型；但它仍把几乎所有图片请求压到同一条 reverse Runner 上，导致几个核心问题长期共存：

- 生成图、改图、图生图三类语义没有被显式建模，handler 和 Runner 只能靠隐式分支解释请求。
- `mask` 被当作普通参考图降级处理，编辑接口在语义上并不正确。
- `n`、`b64_json`、`quality/style/background/output_*` 等字段收进来了，但并没有按 provider 真实能力执行。
- 调度仍是 reverse-only，缺少按 provider 能力、同账号重试、跨账号 failover 的图片专用策略。
- 任务和代理模型默认假设结果一定来自 reverse `file_ids`，无法稳定承载 native 和 Responses 产物。

本次变更不是把其它项目整体迁入 `gpt2api`，而是围绕 `gpt2api` 的图片主干吸收其他实现中最有价值的部分：

- 吸收 `sub2api` 的能力分流、同账号重试、跨账号 failover。
- 吸收 `chatgpt2api` 的轻量参考图输入兼容。
- 吸收 `CLIProxyAPI` 的协议层与执行层边界抽象，并把 Responses `image_generation` tool 做成正式第三 provider。

## What Changes

- 引入 canonical image request，统一表达 `generate`、`edit`、`image-to-image` 请求语义。
- 将图片执行重构为 orchestrator + provider executors，保留 reverse 为核心执行器，同时补齐 native 和 Responses 两条通道。
- 将图片路由改为 capability-aware route planning，根据请求能力和 route policy 在 `reverse`、`native`、`responses_tool` 三种 provider 之间选路。
- 新增 Responses 独立图片接口：
  - `/v1/images/responses/generations`
  - `/v1/images/responses/edits`
- 将 Responses `image_generation` tool 定义为双角色 provider：
  - 统一图片路由中的自动候选与兜底 provider
  - 可显式直调的独立图片路线
- 为图片调度补齐 provider-aware candidate 过滤、同账号重试、跨账号 failover、优先级与能力筛选。
- 为图片 provider 账号补齐独立元数据：
  - `provider_kind`
  - `api_key`
  - `api_base_url`
  - capability flags
  - `same_account_retry_limit`
  - `priority`
- 为图片任务补齐 provider / operation / route / attempts / switches / request_options 等元数据，并新增统一产物表承载 native 和 Responses 结果。
- 统一 `/p/img/:task_id/:idx` 代理逻辑，使 legacy reverse 任务与新 provider 任务都可稳定读取。
- 为图片链路补齐显式支持矩阵、回退开关、路由观测字段和兼容迁移约束。

## Capabilities

### New Capabilities

- `image-operation-compatibility`
- `image-execution-routing`
- `image-provider-scheduling`
- `image-provider-account-management`
- `image-output-unification`
- `image-responses-route`
- `image-rollout-governance`

### Modified Capabilities

- None.

## Impact

- Affected code:
  - `internal/gateway/images.go`
  - `internal/gateway/images_proxy.go`
  - `internal/image/*`
  - `internal/scheduler/*`
  - `internal/account/*`
  - `internal/server/router.go`
  - `cmd/server/main.go`
  - `internal/upstream/*`
- Affected data model:
  - `oai_accounts`
  - `image_tasks`
  - new `image_task_outputs`
- Affected API behavior:
  - `/v1/images/generations`
  - `/v1/images/edits`
  - `/v1/images/responses/generations`
  - `/v1/images/responses/edits`
  - `/v1/images/tasks/:id`
  - `/p/img/:task_id/:idx`
- External systems:
  - ChatGPT reverse image path
  - OpenAI Images API
  - OpenAI Responses API with `image_generation` tool
