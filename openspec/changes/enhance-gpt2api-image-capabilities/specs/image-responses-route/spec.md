## ADDED Requirements

### Requirement: Responses image route SHALL act as both fallback and direct provider
The image gateway SHALL treat the Responses `image_generation` tool route as a third image provider that can participate in automatic routing and can also be selected explicitly as a direct route.

#### Scenario: Automatic routing may choose Responses provider
- **WHEN** a canonical image request is classified as Responses-compatible and route policy allows fallback providers
- **THEN** the route planner may select the Responses provider as part of automatic image routing

#### Scenario: Direct route forces Responses provider
- **WHEN** a client or internal route policy explicitly selects the Responses image route
- **THEN** the gateway executes the request only through the Responses provider instead of reverse or native providers

### Requirement: Direct Responses endpoints SHALL preserve the existing image API contract
The gateway SHALL expose direct Responses image endpoints that accept the same request fields and return the same response shape as the standard image generation and image edit endpoints.

#### Scenario: Direct Responses generation endpoint matches standard contract
- **WHEN** a client submits a valid request to the direct Responses generation endpoint
- **THEN** the endpoint accepts the same request fields as `/v1/images/generations` and returns the same response structure used by the standard image generation API

#### Scenario: Direct Responses edit endpoint matches standard contract
- **WHEN** a client submits a valid request to the direct Responses edit endpoint
- **THEN** the endpoint accepts the same request fields as `/v1/images/edits` and returns the same response structure used by the standard image edit API

### Requirement: Responses route SHALL obey an explicit support matrix
The gateway SHALL classify each image request shape for the Responses provider as supported, emulated, or rejected. Rejected request shapes SHALL fail explicitly rather than being silently degraded.

#### Scenario: Supported prompt-only generation executes normally
- **WHEN** a prompt-only generation request is routed to the Responses provider
- **THEN** the gateway executes the request through the Responses route and returns normalized image results

#### Scenario: Emulated multi-image request is orchestrated explicitly
- **WHEN** a request with `n > 1` is routed to the Responses provider and the route is marked emulated for multi-image behavior
- **THEN** the orchestrator expands the request according to Responses route policy instead of pretending the upstream natively supports that exact behavior

#### Scenario: Rejected masked edit fails explicitly
- **WHEN** a masked edit request is forced onto the Responses provider before stable mask semantics are declared supported
- **THEN** the gateway returns an explicit unsupported-route or unsupported-capability error instead of accepting the request and dropping the mask behavior

### Requirement: Responses route selection SHALL support explicit policy modes
The gateway SHALL support image route policies that distinguish automatic routing, reverse-avoiding safe routing, and Responses-forced routing.

#### Scenario: Safe policy excludes reverse
- **WHEN** a request is executed under a safe image route policy
- **THEN** reverse providers are excluded from candidate selection while native and Responses providers may still participate if compatible

#### Scenario: Responses policy excludes non-Responses providers
- **WHEN** a request is executed under a Responses-forced route policy
- **THEN** only Responses-capable providers are eligible for selection
