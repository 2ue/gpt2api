## ADDED Requirements

### Requirement: The image gateway SHALL route requests by capability, not by endpoint alone
The image gateway SHALL select an execution provider for each image request based on canonical request capability, model route policy, and account support. Route selection SHALL distinguish between reverse-first requests, native-required requests, and fallback-eligible requests.

#### Scenario: Prompt-only generation prefers reverse
- **WHEN** a prompt-only generation request is compatible with both reverse and native providers
- **THEN** the route planner prefers the configured reverse-first path for that model unless policy overrides it

#### Scenario: Native-required request bypasses reverse
- **WHEN** a request contains a mask or native-only image options
- **THEN** the route planner selects only providers that advertise native image capability

#### Scenario: Reference image generation remains reverse-first
- **WHEN** a generation request includes `reference_images`
- **THEN** the route planner keeps the request on a reverse-first path unless an explicit model policy says otherwise

### Requirement: Image execution SHALL be delegated through provider-specific executors
The image gateway SHALL execute image requests through provider-specific executors behind a common orchestration boundary. Reverse execution SHALL reuse the existing ChatGPT reverse image flow, while native execution SHALL call the OpenAI image endpoints without reverse-specific assumptions.

#### Scenario: Reverse execution uses reverse protocol
- **WHEN** the route planner selects a reverse provider
- **THEN** the orchestrator delegates execution to a reverse executor that performs chat requirements, proof-of-work handling, reference uploads, conversation streaming, polling, and image download URL resolution

#### Scenario: Native execution uses native image endpoints
- **WHEN** the route planner selects a native provider
- **THEN** the orchestrator delegates execution to a native executor that calls the native image endpoint and parses native image outputs

### Requirement: Route policy SHALL be configurable per image request and runtime policy
The image gateway SHALL allow each image request and runtime rollout policy to constrain which providers are allowed and which provider is preferred for compatible requests.

#### Scenario: Request policy prefers safe providers
- **WHEN** an image request is executed under a safe route policy
- **THEN** route selection prefers native for requests that both native and reverse could satisfy

#### Scenario: Runtime policy forbids a provider
- **WHEN** runtime rollout policy disables a specific provider kind
- **THEN** route selection SHALL NOT dispatch the request to that provider even if accounts exist
