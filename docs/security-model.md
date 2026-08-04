# Security model

## Trust boundaries

The public boundary is webhook ingestion. Management endpoints, PostgreSQL, worker health, and metrics are internal interfaces. Place the API behind TLS and network controls; do not expose management endpoints solely on the strength of a static token.

## Secret storage

Channel secrets use AES-256-GCM with a random nonce and channel ID as additional authenticated data. The master key exists only in process environment. Database compromise alone does not disclose channel secrets, but a combined database/runtime compromise does. Logs and API responses never include plaintext secrets, ciphertext, nonces, HMAC values, cookies, authorization values, or admin tokens.

## SSRF and DNS rebinding

Production mode accepts HTTPS only. Validation rejects credentials and any hostname resolving to loopback, private, link-local, multicast, or unspecified addresses. The custom dialer resolves again immediately before connecting, validates every returned address, and connects directly to one validated IP. TLS uses the original hostname for certificate verification. Redirects and environment proxy settings are disabled.

This mitigates, but cannot remove, all SSRF risk. Production deployments should add a network egress allowlist or proxy, outbound firewall, DNS policy, target allowlist, and monitoring. IPv4 and IPv6 are both checked.

## Payloads

Requests are limited to 1 MiB before HMAC verification and persistence. Payloads are stored verbatim for retry, so database encryption, retention, backup protection, and access review are required. The service does not log bodies.

## Key rotation

This compact version has no online master-key rotation. Back up the key in an approved secret manager. A production rotation would store a key version beside each secret, decrypt with the old key, re-encrypt in a transaction, and retain the old key until verification completes.
