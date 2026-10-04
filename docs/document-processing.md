# Document processing

Model deployments have an optional `document_processing` field: `native` or
`docling`. Omission keeps the existing policy on update; deployments created
without the field retain native behavior. This is an additive management API
change. It does not change the model or adapter's native `file_input` capability.

Docling conversion is an opt-in gateway operation. Deployment processing policy,
provider credentials and guardrail policy must remain bound throughout a request.
Converted content must pass authorization, security checks and token accounting
before inference. A failed or partial conversion must never fall back to passing
an opaque document to a model.
