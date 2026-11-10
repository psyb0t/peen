FROM golang:1.25-bookworm@sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437

WORKDIR /app
COPY workspace/ ./
RUN python3 -c "open('/app/data/big.log', 'w').write(''.join('2026-03-%02d %02d:%02d:%02d node-%02d event id=%06d status=ok latency=%dms bytes=%d\n' % (n % 28 + 1, n % 24, n % 60, (n * 7) % 60, n % 40, n, n % 97, n * 13 % 5000) if n != 77431 else '2026-03-05 12:00:00 node-07 ALERT code=RX-48213 raised for id=077431\n' for n in range(120000)))"
RUN python3 -c "open('/app/data/manual.txt', 'w').write(''.join('manual section %06d: the quick brown fox jumps over the lazy dog again and again, nothing to see here\n' % n for n in range(21000)) + 'END harbor\n')"
