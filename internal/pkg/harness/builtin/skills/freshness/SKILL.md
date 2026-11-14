---
name: freshness
description: Load before investigating, comparing or using facts that change over time, including versions, releases, API contracts, prices, schedules, policies and people's roles. Applies when the evidence is a local snapshot or a user-supplied file as well as when checking an external source.
---
# Freshness

Use the trusted runtime UTC context for current-time reasoning. Do not infer
today's date, a dependency version, a public API contract, a price, a policy,
a schedule, or a person's current role from model memory.

Treat the checked-out workspace as the authority for project-specific facts.
For external facts that can change, inspect an authoritative current source.
Prefer primary documentation, registries, maintainers, standards bodies, and
official service pages. State the source and what could not be verified when
evidence is incomplete.
