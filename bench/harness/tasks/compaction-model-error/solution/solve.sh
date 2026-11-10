#!/bin/bash
# Write every station code the notes held.
set -euo pipefail

cat >/app/answers.txt <<'EOF'
K01=amber-311
K02=birch-448
K03=cedar-585
K04=delta-722
K05=ember-859
K06=fjord-996
K07=garnet-233
K08=harbor-370
K09=indigo-507
K10=juniper-644
K11=kestrel-781
K12=lagoon-918
EOF
