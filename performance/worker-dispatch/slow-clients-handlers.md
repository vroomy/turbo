# Slow-client and slow-handler observations

One instrumented 512-request run per case, 128 closed-loop clients, requested delay 1 ms. Max queue is sampled every millisecond and is a lower bound.

| Transport | Scenario | Workers | Queue | req/s | p50 ms | p95 ms | p99 ms | Max queue |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Pipe | slow-client | 1 | 0 | 1851 | 68.24 | 71.20 | 71.51 | 0 |
| Pipe | slow-client | 1 | 8 | 9090 | 13.86 | 14.24 | 14.34 | 8 |
| Pipe | slow-client | 1 | 1024 | 41110 | 1.08 | 7.97 | 8.72 | 127 |
| Pipe | slow-client | 8 | 0 | 8479 | 14.90 | 15.96 | 15.98 | 0 |
| Pipe | slow-client | 8 | 8 | 15634 | 7.67 | 8.39 | 8.45 | 8 |
| Pipe | slow-client | 8 | 1024 | 97949 | 1.04 | 1.63 | 1.71 | 120 |
| Pipe | slow-client | 32 | 0 | 29639 | 4.15 | 4.43 | 4.47 | 0 |
| Pipe | slow-client | 32 | 8 | 36842 | 3.15 | 3.94 | 4.19 | 8 |
| Pipe | slow-client | 32 | 1024 | 89147 | 1.10 | 1.96 | 2.16 | 96 |
| Pipe | slow-handler | 1 | 0 | 903 | 140.69 | 149.69 | 149.96 | 0 |
| Pipe | slow-handler | 1 | 8 | 930 | 136.43 | 139.77 | 139.99 | 8 |
| Pipe | slow-handler | 1 | 1024 | 908 | 139.62 | 142.84 | 143.03 | 127 |
| Pipe | slow-handler | 8 | 0 | 7396 | 17.09 | 17.69 | 17.78 | 0 |
| Pipe | slow-handler | 8 | 8 | 7376 | 17.15 | 17.76 | 17.82 | 8 |
| Pipe | slow-handler | 8 | 1024 | 7448 | 16.94 | 17.56 | 17.66 | 120 |
| Pipe | slow-handler | 32 | 0 | 29266 | 4.18 | 4.51 | 4.57 | 0 |
| Pipe | slow-handler | 32 | 8 | 29143 | 4.17 | 4.52 | 4.58 | 8 |
| Pipe | slow-handler | 32 | 1024 | 29964 | 4.12 | 4.29 | 4.42 | 96 |
| TCP | slow-client | 1 | 0 | 16975 | 7.53 | 8.75 | 8.82 | 0 |
| TCP | slow-client | 1 | 8 | 20116 | 6.43 | 8.03 | 8.07 | 8 |
| TCP | slow-client | 1 | 1024 | 24729 | 4.52 | 7.03 | 7.24 | 101 |
| TCP | slow-client | 8 | 0 | 34096 | 3.26 | 4.52 | 4.97 | 0 |
| TCP | slow-client | 8 | 8 | 34275 | 3.22 | 4.91 | 5.30 | 8 |
| TCP | slow-client | 8 | 1024 | 29207 | 3.69 | 5.67 | 5.94 | 77 |
| TCP | slow-client | 32 | 0 | 34447 | 3.21 | 4.37 | 5.14 | 0 |
| TCP | slow-client | 32 | 8 | 27656 | 3.88 | 6.34 | 6.91 | 8 |
| TCP | slow-client | 32 | 1024 | 29505 | 3.39 | 6.01 | 6.14 | 50 |
| TCP | slow-handler | 1 | 0 | 914 | 138.53 | 143.31 | 143.54 | 0 |
| TCP | slow-handler | 1 | 8 | 889 | 138.86 | 150.28 | 153.01 | 8 |
| TCP | slow-handler | 1 | 1024 | 894 | 140.65 | 147.85 | 170.07 | 127 |
| TCP | slow-handler | 8 | 0 | 6787 | 17.21 | 18.02 | 19.90 | 0 |
| TCP | slow-handler | 8 | 8 | 6968 | 17.12 | 17.48 | 18.67 | 8 |
| TCP | slow-handler | 8 | 1024 | 6716 | 17.06 | 18.11 | 20.52 | 120 |
| TCP | slow-handler | 32 | 0 | 16030 | 7.24 | 9.19 | 9.89 | 0 |
| TCP | slow-handler | 32 | 8 | 16951 | 6.84 | 8.53 | 9.03 | 8 |
| TCP | slow-handler | 32 | 1024 | 17609 | 6.78 | 8.31 | 9.10 | 71 |
