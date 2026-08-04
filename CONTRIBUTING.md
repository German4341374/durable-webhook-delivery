# Contributing

Use Conventional Commits and focused branches. Run gofmt, `go vet`, unit/integration tests, race detection on Linux, both builds, and controlled scenarios before requesting review.

Never commit `.env`, webhook payloads, target credentials, channel secrets, master keys, admin tokens, production URLs, database dumps, or delivery history. Changes to leases, outbox boundaries, retry policy, SSRF checks, encryption, or idempotency require explicit tests and documentation.
