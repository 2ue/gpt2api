## ADDED Requirements

### Requirement: Image tasks SHALL persist provider-agnostic execution metadata
Each image task SHALL record the operation type, selected provider kind, attempt count, switch count, and normalized request options needed to understand how the request was executed.

#### Scenario: Reverse task records provider metadata
- **WHEN** a reverse image request is created and executed
- **THEN** the task record includes the operation type, provider kind `chatgpt_reverse`, and final attempt or switch counts

#### Scenario: Native task records provider metadata
- **WHEN** a native image request is created and executed
- **THEN** the task record includes the operation type, provider kind `openai_native`, and final attempt or switch counts

### Requirement: Image outputs SHALL be stored as typed artifacts
The image gateway SHALL persist image outputs as typed artifacts instead of assuming every output is a reverse file reference. Artifact source types SHALL include reverse references and stored binary outputs at minimum.

#### Scenario: Reverse outputs are stored as reverse artifacts
- **WHEN** reverse execution produces image references
- **THEN** the gateway stores those outputs as reverse artifact entries that preserve the upstream reference needed for proxy resolution

#### Scenario: Native outputs are stored as stored artifacts
- **WHEN** native execution returns `b64_json` or downloadable image URLs
- **THEN** the gateway stores those outputs as binary-backed artifacts instead of forcing them into reverse-style file ID fields

### Requirement: Image proxy SHALL resolve output artifacts by source type
The `/p/img/:task_id/:idx` proxy SHALL resolve image bytes according to the stored artifact type for that output while preserving compatibility with legacy reverse tasks.

#### Scenario: Reverse artifact is proxied through reverse downloader
- **WHEN** a proxy request targets an output whose artifact type is a reverse reference
- **THEN** the proxy resolves the current upstream download URL using the task conversation and account lease data before returning bytes

#### Scenario: Stored artifact is proxied directly
- **WHEN** a proxy request targets an output whose artifact type is a stored binary artifact
- **THEN** the proxy returns bytes from the configured image storage backend without requiring reverse account download logic

#### Scenario: Legacy reverse task remains readable
- **WHEN** a proxy request targets an older task that predates artifact storage but still has legacy reverse `file_ids`
- **THEN** the proxy falls back to the legacy reverse resolution path instead of failing
