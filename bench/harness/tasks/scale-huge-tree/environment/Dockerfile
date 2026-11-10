FROM golang:1.25-bookworm@sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437

WORKDIR /app
COPY workspace/ ./
RUN python3 -c "import os; [os.makedirs('/app/tree/d%03d' % d, exist_ok=True) or [open('/app/tree/d%03d/f%03d.txt' % (d, f), 'w').write('record %d routine entry\nstatus ok\nchecksum %08x\n' % (d * 200 + f, (d * 200 + f) * 2654435761 % 4294967296)) for f in range(200)] for d in range(250)]"
RUN printf 'record needle\nORCHID-LEDGER-7 value=48213\nstatus ok\n' > /app/tree/d173/f088.txt \
 && printf 'record decoy\nORCHID-LEDGER-6 value=11111\nstatus ok\n' > /app/tree/d017/f003.txt \
 && printf 'record decoy\nORCHID-LEDGER-5 value=22222\nstatus ok\n' > /app/tree/d201/f150.txt
