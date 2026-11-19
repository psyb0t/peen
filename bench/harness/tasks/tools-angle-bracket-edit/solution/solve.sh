#!/bin/bash
# Rename the class on its declaration line only.
set -euo pipefail

sed -i '1s/^export class Cache<T> {$/export class Store<T> {/' /app/src/cache.ts
