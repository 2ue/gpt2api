## ADDED Requirements

### Requirement: Image provider accounts SHALL declare explicit provider kind and image capabilities
Every image-capable account SHALL explicitly declare its provider kind and the image capabilities it supports. The system SHALL distinguish at minimum between reverse image accounts and native image accounts instead of inferring execution behavior only from legacy account type labels.

#### Scenario: Reverse account declares reverse capability
- **WHEN** an operator creates or updates a reverse image account
- **THEN** the account record stores a provider kind identifying it as reverse-capable and marks reverse image execution as supported

#### Scenario: Native account declares native capability
- **WHEN** an operator creates or updates a native image account
- **THEN** the account record stores a provider kind identifying it as native-capable and marks native image execution as supported

#### Scenario: Responses account declares responses capability
- **WHEN** an operator creates or updates a Responses-tool image account
- **THEN** the account record stores a provider kind identifying it as responses-capable and marks Responses image execution as supported

### Requirement: Provider-specific credentials SHALL be validated before scheduling
The system SHALL validate that each image provider account has the credentials required by its provider kind before that account becomes eligible for image scheduling.

#### Scenario: Reverse account missing reverse credentials is ineligible
- **WHEN** a reverse image account is missing required reverse credentials or stable reverse identifiers cannot be established
- **THEN** the account SHALL NOT be considered dispatchable for reverse image requests

#### Scenario: Native account missing API credentials is ineligible
- **WHEN** a native image account is missing its native API key or required base URL configuration
- **THEN** the account SHALL NOT be considered dispatchable for native image requests

#### Scenario: Responses account missing provider credentials is ineligible
- **WHEN** a Responses-tool image account is missing the credentials or base URL needed to execute Responses tool calls
- **THEN** the account SHALL NOT be considered dispatchable for Responses image requests

### Requirement: Scheduler eligibility SHALL honor declared image capabilities
Image scheduling SHALL only consider accounts whose declared image capabilities satisfy the canonical request capability chosen by the route planner.

#### Scenario: Native-required request only sees native-capable accounts
- **WHEN** a canonical image request requires native image capability
- **THEN** the scheduler candidate set contains only accounts that declare native image capability

#### Scenario: Reverse-first request only sees reverse-capable accounts
- **WHEN** a canonical image request requires or prefers reverse image execution
- **THEN** the scheduler candidate set contains only accounts that declare reverse image capability unless a route policy explicitly permits fallback

#### Scenario: Responses-forced request only sees responses-capable accounts
- **WHEN** a canonical image request is routed with a forced Responses policy
- **THEN** the scheduler candidate set contains only accounts that declare Responses image capability
