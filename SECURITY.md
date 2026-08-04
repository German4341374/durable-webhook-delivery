# Security Policy

Report vulnerabilities privately through GitHub security advisories. Do not open public issues containing payloads, URLs, credentials, tokens, signatures, database data, or delivery history.

The latest release on `main` receives security fixes. Rotate potentially exposed local secrets by deleting `.env`, running `make setup`, and recreating the local database volume. Production incidents must follow the owning organization's key, data, and webhook-consumer procedures.
