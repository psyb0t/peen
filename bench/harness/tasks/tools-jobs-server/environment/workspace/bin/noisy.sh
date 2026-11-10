#!/bin/sh
i=1
while [ "${i}" -le 6000 ]; do
	echo "noise line ${i}"
	i=$((i + 1))
done
sleep 4
echo "DONE code=7391"
