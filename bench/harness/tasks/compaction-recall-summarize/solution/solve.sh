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
K13=marble-155
K14=nectar-292
K15=onyx-429
K16=pewter-566
K17=quartz-703
K18=russet-840
K19=saffron-977
K20=tundra-214
K21=umber-351
K22=violet-488
K23=willow-625
K24=xenon-762
K25=yarrow-899
K26=zephyr-136
K27=basalt-273
K28=cobalt-410
K29=dune-547
K30=falcon-684
EOF
