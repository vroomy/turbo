# Sustained TCP observations

One preflight connection is included in dial/accept/close counters, and excluded from attempts/completions. Failed runs are diagnostic evidence; their elapsed times include outstanding 10 s deadlines and failure capture. They are not throughput samples.

| Round/server/run | Attempts | Completed | Errors | Dialed | Accepted | Closed | Close calls | Seconds | Max queue | Stopped goroutines |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| round1-Turbo/bounded | 10000 | 10000 | 0 | 10001 | 10001 | 10001 | 10001 | 0.257 | 0 | 2 |
| round1-Turbo/3s | 104856 | 104856 | 0 | 104857 | 104857 | 104857 | 104857 | 3.000 | 0 | 2 |
| round1-Turbo/10s | 99739 | 99731 | 8 | 99740 | 99740 | 99740 | 99740 | 13.605 | 0 | 2 |
| round1-Stdlib/bounded | 10000 | 10000 | 0 | 10001 | 10001 | 10001 | 10001 | 0.273 | 0 | 2 |
| round1-Stdlib/3s | 96646 | 96646 | 0 | 96647 | 96647 | 96647 | 96647 | 3.000 | 0 | 2 |
| round1-Stdlib/10s | 73914 | 73906 | 8 | 73915 | 73915 | 73915 | 73915 | 12.908 | 0 | 2 |
| round2-Stdlib/bounded | 10000 | 10000 | 0 | 10001 | 10001 | 10001 | 10001 | 0.257 | 0 | 2 |
| round2-Stdlib/3s | 38760 | 38752 | 8 | 38761 | 38761 | 38761 | 38761 | 11.722 | 0 | 2 |
| round2-Stdlib/10s | 259335 | 259327 | 8 | 259336 | 259336 | 259336 | 259336 | 18.543 | 0 | 2 |
| round2-Turbo/bounded | 10000 | 10000 | 0 | 10001 | 10001 | 10001 | 10001 | 0.257 | 0 | 2 |
| round2-Turbo/3s | 102575 | 102575 | 0 | 102576 | 102576 | 102576 | 102576 | 3.000 | 0 | 2 |
| round2-Turbo/10s | 270148 | 270140 | 8 | 270149 | 270149 | 270149 | 270149 | 19.115 | 0 | 2 |
| round3-Turbo/bounded | 10000 | 10000 | 0 | 10001 | 10001 | 10001 | 10001 | 0.476 | 0 | 2 |
| round3-Turbo/3s | 87092 | 87084 | 8 | 87093 | 87093 | 87093 | 87093 | 13.078 | 0 | 2 |
| round3-Turbo/10s | 65223 | 65215 | 8 | 65224 | 65224 | 65224 | 65224 | 12.362 | 0 | 2 |
| round3-Stdlib/bounded | 10000 | 10000 | 0 | 10001 | 10001 | 10001 | 10001 | 0.282 | 0 | 2 |
| round3-Stdlib/3s | 87803 | 87795 | 8 | 87804 | 87804 | 87804 | 87804 | 13.260 | 0 | 2 |
| round3-Stdlib/10s | 187766 | 187758 | 8 | 187767 | 187767 | 187767 | 187767 | 16.297 | 0 | 2 |
| cooldown/Turbo/3s | 61439 | 61439 | 0 | 61440 | 61440 | 61440 | 61440 | 3.000 | 0 | 2 |
| cooldown/Turbo/10s | 235995 | 235995 | 0 | 235996 | 235996 | 235996 | 235996 | 10.000 | 0 | 2 |
| cooldown/Stdlib/3s | 111450 | 111450 | 0 | 111451 | 111451 | 111451 | 111451 | 3.000 | 0 | 2 |
| cooldown/Stdlib/10s | 302046 | 302046 | 0 | 302047 | 302047 | 302047 | 302047 | 10.000 | 0 | 2 |
