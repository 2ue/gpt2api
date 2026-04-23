## ADDED Requirements

### Requirement: Image scheduling SHALL be provider-aware
The image scheduler SHALL dispatch leases using provider capability, request capability, account health, cooldown state, and exclusion state. Reverse-only accounts SHALL NOT be selected for native-required requests, and native-only accounts SHALL NOT be selected for reverse-required requests.

#### Scenario: Native-required request skips reverse-only account
- **WHEN** a native-required image request is dispatched
- **THEN** the scheduler excludes reverse-only accounts from the candidate set

#### Scenario: Reverse image-to-image request skips native-only account
- **WHEN** a reverse-first image-to-image request with reference images is dispatched
- **THEN** the scheduler excludes native-only accounts from the candidate set

### Requirement: Reverse image leases SHALL preserve stable reverse identifiers
When the scheduler grants a reverse image lease, it SHALL reuse a stable `oai_device_id` and `oai_session_id` for that account unless those identifiers are missing and need to be initialized.

#### Scenario: Existing reverse account reuses stable identifiers
- **WHEN** the scheduler dispatches a reverse lease for an account that already has `oai_device_id` and `oai_session_id`
- **THEN** the lease uses those persisted identifiers instead of generating new ones

#### Scenario: Missing reverse identifiers are initialized once
- **WHEN** the scheduler dispatches a reverse lease for an account missing a device or session identifier
- **THEN** the scheduler generates and persists the missing identifiers before continuing

### Requirement: Retryable image failures SHALL retry on the same account before switching
The image orchestrator SHALL retry retryable image failures on the same account before excluding that account and switching to another account. Same-account retry count SHALL be bounded by account or system policy.

#### Scenario: Retryable failure triggers same-account retry
- **WHEN** execution returns a retryable image failure on an account with remaining same-account retries
- **THEN** the orchestrator retries the request on the same account before switching providers or accounts

#### Scenario: Retry budget exhaustion triggers account switch
- **WHEN** a retryable image failure continues after the configured same-account retry limit is reached
- **THEN** the orchestrator excludes the failing account and dispatches a new account if one is available

### Requirement: Non-retryable image failures SHALL not loop on the same account
The image orchestrator SHALL treat non-retryable provider failures as immediate switch-or-fail conditions and SHALL NOT repeatedly retry them on the same account.

#### Scenario: Non-retryable error switches accounts
- **WHEN** execution returns a non-retryable image provider error and another eligible account exists
- **THEN** the orchestrator excludes the current account and retries on another account

#### Scenario: No eligible account returns scheduling failure
- **WHEN** execution cannot find another eligible account after exclusions are applied
- **THEN** the gateway returns a scheduling or unavailable error instead of looping indefinitely
