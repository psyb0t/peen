#!/bin/sh
awk 'BEGIN {
	for (i = 1; i <= 125000; i++) {
		printf "record %07d payload abcdefghijklmnopqrstuvwxyz0123456789 abcdefghij0123456789ABCDEFGHIJ\n", i
	}
	print "SUMMARY total=48213"
	for (i = 125001; i <= 250000; i++) {
		printf "record %07d payload abcdefghijklmnopqrstuvwxyz0123456789 abcdefghij0123456789ABCDEFGHIJ\n", i
	}
}'
