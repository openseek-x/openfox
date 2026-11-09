# Error contract

Every JSON API error uses this shape:

```json
{
  "error": {
    "code": "RESOURCE_NOT_FOUND",
    "message": "resource is not available",
    "request_id": "request-id"
  }
}
```

`message` is safe for the caller and must not contain SQL, credentials,
tenant-private names, or another organization's existence. `request_id` is
opaque and is also returned in `X-Request-ID`.

Status semantics:

| Status | Use                                                                  |
| ------ | -------------------------------------------------------------------- |
| 400    | malformed input or invalid field value                               |
| 401    | missing or invalid authentication                                    |
| 403    | authenticated but not allowed to perform a known action              |
| 404    | missing resource or a resource hidden by tenant/object authorization |
| 409    | version, idempotency, state, or capacity conflict                    |
| 422    | well-formed input that violates a domain rule                        |
| 429    | bounded queue or rate limit rejection                                |
| 503    | dependency unavailable or service not ready                          |

New codes must be stable ASCII identifiers and must document whether retrying
is safe. Unknown internal failures map to a generic 500 response and are
logged with the request ID without logging secrets.
