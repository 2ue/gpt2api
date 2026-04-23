## 1. Canonical Request And Support Matrix

- [ ] 1.1 Add a canonical image request model for generate and edit semantics.
- [ ] 1.2 Refactor `/v1/images/generations` and `/v1/images/edits` parsing to build canonical requests before execution.
- [ ] 1.3 Separate `mask` handling from generic reference image ingestion.
- [ ] 1.4 Add a shared support matrix with `supported`, `emulated`, and `rejected` states per provider kind.
- [ ] 1.5 Make `n`, `response_format`, `reference_images`, and native-only image options real routing requirements.

## 2. Routing And Executors

- [ ] 2.1 Convert `image.Runner` into an orchestrator behind the existing entry point.
- [ ] 2.2 Preserve the current ChatGPT reverse flow as `ReverseExecutor`.
- [ ] 2.3 Add `NativeOpenAIExecutor` for native image generations and edits.
- [ ] 2.4 Add `ResponsesToolExecutor` for Responses `image_generation` tool execution.
- [ ] 2.5 Add image route policies `auto`, `safe`, and `responses`.
- [ ] 2.6 Add direct Responses image routes with the same request and response contract as the standard image endpoints.
- [ ] 2.7 Ensure route planning prefers semantic correctness over handler legacy behavior.

## 3. Provider-Aware Scheduling

- [ ] 3.1 Add `DispatchSpec` for image scheduling with provider and capability filtering.
- [ ] 3.2 Extend image leases so reverse and API-key providers can share the same scheduler boundary.
- [ ] 3.3 Add same-account retry inside the orchestrator for retryable image failures.
- [ ] 3.4 Add cross-account failover with exclusion tracking and bounded switch counts.
- [ ] 3.5 Record final attempt and switch counts on image tasks.

## 4. Provider Account Model

- [ ] 4.1 Extend `oai_accounts` with `provider_kind`, `api_key_enc`, `api_base_url`, capability flags, retry limit, and priority.
- [ ] 4.2 Update account DAO, service, and admin handler to create and edit reverse, native, and Responses image accounts safely.
- [ ] 4.3 Keep refresh and quota probe paths scoped to reverse accounts only.
- [ ] 4.4 Ensure provider eligibility is determined by explicit provider metadata instead of legacy `account_type`.

## 5. Task And Output Unification

- [ ] 5.1 Extend `image_tasks` with operation, provider, route policy, request options, attempt count, and switch count.
- [ ] 5.2 Add `image_task_outputs` for unified output storage.
- [ ] 5.3 Add local artifact storage for native and Responses outputs.
- [ ] 5.4 Update task query and image proxy logic to read unified outputs first and legacy reverse fields second.
- [ ] 5.5 Preserve legacy reverse task and proxy compatibility.

## 6. Endpoint Behavior

- [ ] 6.1 Route masked edits to native execution instead of reverse degradation.
- [ ] 6.2 Preserve `reference_images` as a first-class reverse image-to-image input while allowing alternate compatible providers.
- [ ] 6.3 Return real `b64_json` payloads on supported providers.
- [ ] 6.4 Support direct Responses generation and edit execution under the same request contract.
- [ ] 6.5 Reject unsupported Responses request shapes explicitly instead of silently dropping fields.

## 7. Verification

- [ ] 7.1 Add schema migration SQL for account and image task changes.
- [ ] 7.2 Add route selection and support-matrix tests.
- [ ] 7.3 Add compatibility checks for legacy reverse tasks.
- [ ] 7.4 Run `openspec validate enhance-gpt2api-image-capabilities`.
- [ ] 7.5 Run targeted Go tests and build verification for the touched packages.
