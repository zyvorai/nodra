# Contributing

Thanks for improving Nodra.

1. Fork the repository and create a focused branch.
2. Run `make fmt`, `make vet`, and `make race`.
3. For API/UI changes, also run `make smoke` (and `make demo-client` against a lab if you touch auth or console tabs).
4. Add tests for behavior changes, especially durability and authentication boundaries.
5. Keep the edge runtime dependency-light. New dependencies need a clear maintenance/security reason.
6. Update docs when you change operator-facing behavior (`README.md`, `docs/API.md`, `docs/openapi.yaml`, `docs/DEMO.md` as relevant).
7. Open a pull request describing the failure mode or use case, the change, and how it was tested.

By submitting a contribution, you agree that it is licensed under Apache-2.0.

## Design principles

- never acknowledge durable acceptance before persistence succeeds
- never silently delete a queued event on transport failure
- do not weaken authentication to simplify demos
- avoid “HA” claims until state semantics actually support it
- prefer explicit bounded resource behavior over unbounded in-memory queues
- list JSON endpoints return arrays, never `null`
