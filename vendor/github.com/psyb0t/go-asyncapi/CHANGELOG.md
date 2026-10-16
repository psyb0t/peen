# Changelog

All notable changes per release. Versions follow [semver](https://semver.org).

## v0.1.0, 2026-10-15

- Added `Load`, which reads an AsyncAPI 3 document in YAML or JSON and returns its servers, channels, operations, messages and component schemas in document order, with local and relative-file `$ref`s followed and message and operation traits merged.
- Added `Resolver` and `Location` for dereferencing `$ref`s inside schemas, and `Object`, an ordered mapping for reading schema trees.
