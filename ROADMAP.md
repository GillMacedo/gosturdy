# Roadmap

## Next
- `pgxpool` and `sqlx` pool settings
- Response bodies that are never closed, where the parser can be sure
- Retries with no backoff in `net/http` clients

## Later
- Kubernetes manifests checked next to the code, such as the grace period against the shutdown timeout
- Optional type checking for fewer false findings
