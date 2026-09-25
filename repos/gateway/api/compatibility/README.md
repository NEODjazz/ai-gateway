# Reviewed API contract changes

The compatibility gate compares this branch with API version 0.1.416 at
`c32f5652691ba11d8f8e63894e7fcb1be67f8c70`. Its threshold remains
`WARN`. The adjacent files contain exact, endpoint-specific findings accepted
for the 0.1.474 update. New findings still fail CI.

- A2A result schemas inherit optional media-resolution data on parts. Existing
  result shapes are retained.
- Vector-store responses may return static chunking only when the caller selected
  static chunking; existing auto responses retain their shape.
- New native tool variants shift `oneOf` positions in the request schema. The
  previously accepted variants remain present and covered by request tests.
- Response enums gained provider capabilities and execution states. Consumers
  must handle unfamiliar values; the contract lists those now emitted.
- `prompt_cache_key` and `user` gained explicit length bounds to prevent
  unbounded request identities. Callers exceeding those bounds receive a
  validation error.

Remove these accepted findings once the 0.1.474 contract is the comparison
baseline. Do not use this list for unrelated API changes.
