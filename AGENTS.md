# Repository Guidance

- Use English for source, configuration, documentation, and commits.
- Preserve the event/outbox transaction boundary and at-least-once semantics.
- Never log or return secrets, signatures, tokens, cookies, or payload bodies.
- Treat all outbound URLs as hostile and keep DNS/IP checks at dial time.
- Add migrations instead of editing an applied migration.
- Do not claim a controlled scenario passed unless it ran successfully.
