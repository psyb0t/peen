#!/bin/bash
# Write each record's serial.
set -euo pipefail

cat >/app/serials.txt <<'EOF'
rec01.txt=SER-3FF45714
rec02.txt=SER-C344666C
rec03.txt=SER-77CCE03B
rec04.txt=SER-1EDE1D11
rec05.txt=SER-F83BD0D6
rec06.txt=SER-8331FA41
rec07.txt=SER-16A2B719
rec08.txt=SER-57BFFD5B
rec09.txt=SER-F04EB949
rec10.txt=SER-A86CA0AE
rec11.txt=SER-CF40300B
rec12.txt=SER-67628B42
rec13.txt=SER-5F770AAA
rec14.txt=SER-CE4248A7
rec15.txt=SER-56A9D059
rec16.txt=SER-9FD30DD8
rec17.txt=SER-CA6FB0D6
rec18.txt=SER-2C264221
rec19.txt=SER-F7467E8E
rec20.txt=SER-6D52E677
rec21.txt=SER-701DEA21
rec22.txt=SER-A91E5BD8
rec23.txt=SER-E75973E5
rec24.txt=SER-11406D45
EOF
