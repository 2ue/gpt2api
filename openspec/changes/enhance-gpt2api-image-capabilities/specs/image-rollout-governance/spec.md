## ADDED Requirements

### Requirement: Image route support SHALL be explicit per request shape
The image gateway SHALL maintain an explicit support matrix for image request shapes and provider kinds so that unsupported combinations are rejected or rerouted intentionally instead of being silently degraded.

#### Scenario: Unsupported request shape is rejected explicitly
- **WHEN** a client submits an image request whose semantic shape is unsupported by the selected provider policy
- **THEN** the gateway returns an explicit validation or route error instead of accepting the request and silently dropping unsupported behavior

#### Scenario: Supported request shape is routed intentionally
- **WHEN** a client submits an image request whose semantic shape is supported by more than one provider kind
- **THEN** the gateway uses the configured route policy to choose a provider instead of relying on incidental handler behavior

### Requirement: New image routing SHALL be controllable by rollout flags
The image gateway SHALL provide configuration that can force reverse-only behavior, enable native image execution selectively, and disable optional fallback providers during rollout or incident response.

#### Scenario: Reverse-only rollback is enabled
- **WHEN** operators enable a reverse-only rollout mode
- **THEN** compatible image requests are routed only through reverse execution and native image execution is bypassed

#### Scenario: Native execution can be enabled selectively
- **WHEN** operators enable native image execution for a subset of models or providers
- **THEN** only those configured image routes use native execution while other routes keep their existing behavior

### Requirement: Image observability SHALL expose routing and failover outcomes
The image system SHALL record provider kind, route decision, attempt count, switch count, result type, and compatibility mode for image executions so that rollout and regressions can be analyzed.

#### Scenario: Successful request records route metadata
- **WHEN** an image request completes successfully
- **THEN** the system records which provider handled the request, how many attempts were used, and whether provider switching occurred

#### Scenario: Failed request records failover metadata
- **WHEN** an image request fails after retry or provider switching
- **THEN** the system records the final failure code, provider route, retry count, and switch count for troubleshooting

### Requirement: New image storage and proxy behavior SHALL preserve legacy task access
The new image artifact and proxy model SHALL remain backward-compatible with legacy reverse-only tasks and signed image proxy URLs during migration.

#### Scenario: Legacy reverse task remains queryable
- **WHEN** a client queries an image task created before unified artifact storage was introduced
- **THEN** the task response continues to resolve its legacy reverse outputs correctly

#### Scenario: Legacy reverse proxy path remains valid
- **WHEN** a client accesses a proxy URL for a legacy reverse image task
- **THEN** the proxy resolves the image through the legacy reverse path rather than failing because the task predates artifact storage
