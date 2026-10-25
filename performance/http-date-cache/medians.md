# All benchmark medians

Ten alternating-order samples per case. Parallel ns/op measures inverse aggregate throughput.

| Case | Before ns/op | After ns/op | Change | Before B/op | After B/op | Before objects/op | After objects/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| BenchmarkTurboProcess/SmallGET-8 | 918.25 | 791 | -13.86% | 132 | 100 | 3 | 2 |
| BenchmarkTurboProcess/ImplicitGET-8 | 1014 | 903.1 | -10.94% | 100 | 68 | 1 | 0 |
| BenchmarkTurboProcess/LargeGET-8 | 929.1 | 807.65 | -13.07% | 132 | 100 | 3 | 2 |
| BenchmarkTurboProcess/POST-8 | 1243.5 | 1144 | -8.00% | 1139 | 1107 | 14 | 13 |
| BenchmarkTurboProcess/HEAD-8 | 921.2 | 794.9 | -13.71% | 132 | 100 | 3 | 2 |
| BenchmarkTurboProcess/Empty-8 | 826.6 | 714.9 | -13.51% | 116 | 84 | 2 | 1 |
| BenchmarkTurboProcess/StreamGET-8 | 944.85 | 818.25 | -13.40% | 132 | 100 | 3 | 2 |
| BenchmarkResponseWriter/final-8 | 594.55 | 491.95 | -17.26% | 48 | 16 | 2 | 1 |
| BenchmarkResponseWriter/informational-8 | 729.2 | 616.6 | -15.44% | 48 | 16 | 2 | 1 |
| BenchmarkHTTP11NoKeepAlive/Pipe/SmallGET/Serial/Turbo-8 | 5077 | 4948 | -2.54% | 2607 | 2575.5 | 30 | 29 |
| BenchmarkHTTP11NoKeepAlive/Pipe/SmallGET/Parallel/Turbo-8 | 1983 | 1948.5 | -1.74% | 2605 | 2572 | 30 | 29 |
| BenchmarkHTTP11NoKeepAlive/Pipe/ImplicitGET/Serial/Turbo-8 | 5178 | 5196.5 | +0.36% | 2538 | 2506 | 26 | 25 |
| BenchmarkHTTP11NoKeepAlive/Pipe/ImplicitGET/Parallel/Turbo-8 | 1986 | 2003 | +0.86% | 2546 | 2515.5 | 26 | 25 |
| BenchmarkHTTP11NoKeepAlive/Pipe/LargeGET/Serial/Turbo-8 | 11243.5 | 11026.5 | -1.93% | 2605.5 | 2574.5 | 30 | 29 |
| BenchmarkHTTP11NoKeepAlive/Pipe/LargeGET/Parallel/Turbo-8 | 3065 | 3047.5 | -0.57% | 2619 | 2586.5 | 30 | 29 |
| BenchmarkHTTP11NoKeepAlive/Pipe/POST/Serial/Turbo-8 | 5333 | 5221 | -2.10% | 3620.5 | 3588 | 41 | 40 |
| BenchmarkHTTP11NoKeepAlive/Pipe/POST/Parallel/Turbo-8 | 2162.5 | 2150 | -0.58% | 3618 | 3584 | 41 | 40 |
| BenchmarkHTTP11NoKeepAlive/Pipe/HEAD/Serial/Turbo-8 | 5059.5 | 4935 | -2.46% | 2490 | 2458 | 28 | 27 |
| BenchmarkHTTP11NoKeepAlive/Pipe/HEAD/Parallel/Turbo-8 | 1956 | 1921.5 | -1.76% | 2498 | 2466 | 28 | 27 |
| BenchmarkHTTP11NoKeepAlive/Pipe/Empty/Serial/Turbo-8 | 4856 | 4762.5 | -1.93% | 2431 | 2400 | 25 | 24 |
| BenchmarkHTTP11NoKeepAlive/Pipe/Empty/Parallel/Turbo-8 | 1913 | 1843 | -3.66% | 2438 | 2406 | 25 | 24 |
| BenchmarkHTTP11NoKeepAlive/Pipe/StreamGET/Serial/Turbo-8 | 6275 | 6276 | +0.02% | 2604.5 | 2574 | 30 | 29 |
| BenchmarkHTTP11NoKeepAlive/Pipe/StreamGET/Parallel/Turbo-8 | 2411 | 2368 | -1.78% | 2620 | 2588 | 30 | 29 |
| BenchmarkHTTP11NoKeepAlive/TCP/SmallGET/Serial/Turbo-8 | 80045.5 | 74591 | -6.81% | 3263 | 3227.5 | 46 | 45 |
| BenchmarkHTTP11NoKeepAlive/TCP/SmallGET/Parallel/Turbo-8 | 32942 | 26415.5 | -19.81% | 3817.5 | 3761.5 | 46.5 | 46 |
| BenchmarkHTTP11NoKeepAlive/TCP/ImplicitGET/Serial/Turbo-8 | 71082 | 71055 | -0.04% | 3134.5 | 3080 | 42 | 41 |
| BenchmarkHTTP11NoKeepAlive/TCP/ImplicitGET/Parallel/Turbo-8 | 35745.5 | 28526.5 | -20.20% | 3649 | 3597.5 | 42 | 41 |
| BenchmarkHTTP11NoKeepAlive/TCP/LargeGET/Serial/Turbo-8 | 81956.5 | 78723 | -3.95% | 3205.5 | 3136 | 46 | 45 |
| BenchmarkHTTP11NoKeepAlive/TCP/LargeGET/Parallel/Turbo-8 | 40051.5 | 35947 | -10.25% | 3739 | 3704.5 | 46 | 45 |
| BenchmarkHTTP11NoKeepAlive/TCP/POST/Serial/Turbo-8 | 73900.5 | 75556.5 | +2.24% | 4063 | 4023.5 | 56 | 55 |
| BenchmarkHTTP11NoKeepAlive/TCP/POST/Parallel/Turbo-8 | 33272 | 31049.5 | -6.68% | 4536.5 | 4485.5 | 57 | 56 |
| BenchmarkHTTP11NoKeepAlive/TCP/HEAD/Serial/Turbo-8 | 72374.5 | 71297.5 | -1.49% | 2903 | 2881.5 | 44 | 43 |
| BenchmarkHTTP11NoKeepAlive/TCP/HEAD/Parallel/Turbo-8 | 28676.5 | 30075 | +4.88% | 3221 | 3187 | 44 | 43 |
| BenchmarkHTTP11NoKeepAlive/TCP/Empty/Serial/Turbo-8 | 70590.5 | 68374 | -3.14% | 2805 | 2778.5 | 41 | 40 |
| BenchmarkHTTP11NoKeepAlive/TCP/Empty/Parallel/Turbo-8 | 31231.5 | 34210.5 | +9.54% | 3120.5 | 3085.5 | 41 | 40 |
| BenchmarkHTTP11NoKeepAlive/TCP/StreamGET/Serial/Turbo-8 | 75417.5 | 73653 | -2.34% | 3236 | 3217.5 | 46 | 45 |
| BenchmarkHTTP11NoKeepAlive/TCP/StreamGET/Parallel/Turbo-8 | 34634.5 | 32757.5 | -5.42% | 3779.5 | 3754.5 | 46 | 45 |
