## ADDED Requirements

### Requirement: Image requests SHALL be normalized into canonical operations
The image gateway SHALL normalize `/v1/images/generations` and `/v1/images/edits` inputs into a canonical image request model before route selection or execution. The canonical model SHALL preserve operation type, prompt, image inputs, reference images, mask, output count, response format, and native image options.

#### Scenario: Prompt-only generation is normalized
- **WHEN** a client submits `/v1/images/generations` with only `prompt`, `model`, `size`, and `n`
- **THEN** the gateway creates a canonical request with operation `generate` and preserves those fields for routing and execution

#### Scenario: Reference image generation is normalized
- **WHEN** a client submits `/v1/images/generations` with `reference_images`
- **THEN** the gateway preserves those reference images as image-to-image inputs instead of collapsing them into plain text or dropping them

### Requirement: Edit requests SHALL preserve native edit semantics
The image gateway SHALL treat `mask` as a first-class edit input and SHALL NOT silently downgrade `mask` into a generic reference image. Requests that require native edit semantics SHALL be marked native-required before execution.

#### Scenario: Masked edit requires native semantics
- **WHEN** a client submits `/v1/images/edits` with one or more source images and a `mask`
- **THEN** the canonical request marks the operation as `edit` with `mask` present and the route planner treats the request as native-required

#### Scenario: Edit without mask remains a valid edit request
- **WHEN** a client submits `/v1/images/edits` without a `mask`
- **THEN** the canonical request remains an `edit` operation and may still be routed to a provider that supports edit semantics without forcing a mask

### Requirement: Image request compatibility SHALL reflect true provider semantics
The image gateway SHALL expose only those image behaviors that the selected provider can honor. Native-only fields such as `background`, `quality`, `style`, `output_format`, `output_compression`, and `moderation` SHALL NOT be silently accepted on a route that cannot implement them.

#### Scenario: Native-only options force native compatibility
- **WHEN** a client submits a generation or edit request with native-only image options
- **THEN** the request is classified as requiring native image capability before execution begins

#### Scenario: Unsupported options are not silently ignored
- **WHEN** route selection resolves to a provider that cannot honor a supplied image option
- **THEN** the gateway either selects a provider that supports the option or returns a validation or route error instead of pretending the option was applied

### Requirement: Image result cardinality and format SHALL be real, not nominal
The image gateway SHALL treat `n` and `response_format` as execution requirements. A successful request SHALL return the requested number of images unless bounded by an explicit server-side limit, and `b64_json` SHALL return image payloads rather than proxy URLs.

#### Scenario: Multiple images are actually produced
- **WHEN** a client submits an image request with `n > 1`
- **THEN** the gateway returns that many output images or an explicit bounded-limit error instead of only using `n` for billing

#### Scenario: Base64 response format returns base64 payloads
- **WHEN** a client submits an image request with `response_format=b64_json`
- **THEN** the gateway returns image data as base64 JSON output instead of forcing URL-only semantics
