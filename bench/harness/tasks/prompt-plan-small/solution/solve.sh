#!/bin/bash
# Fix the divisor.
set -euo pipefail

sed -i 's/(len(values) + 1)/len(values)/' /app/calc.py
