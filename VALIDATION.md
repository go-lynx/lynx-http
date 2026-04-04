# Validation

## Automated Baseline

Current workspace baseline:

```bash
go test ./...
go vet ./...
```

## What This Covers

- The module passes unit, integration, stress-gated, and benchmark-build test compilation in the current workspace.
- Documentation and `conf/example_config.yml` have been reconciled with the current runtime so reserved fields are no longer described as fully active behavior.

## Remaining Production Checks

- Verify TLS certificate provider wiring in a real Lynx application, including rotation and managed restart behavior.
- Run traffic and shutdown drills against a live deployment to validate timeout tuning and concurrency limits under load.
- If you need CORS, automatic security headers, or richer graceful-shutdown semantics, add explicit middleware/proxy coverage because those schema fields are not fully wired today.
