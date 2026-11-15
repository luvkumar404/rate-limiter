| Algorithm | Offered /s | Whole-run completed /s | Whole-run valid /s | p50 ms | p95 ms | p99 ms | Allowed | Blocked | Invalid | Dropped |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| sliding_window |  | 151.8 | 151.8 | 2.15 | 5.51 | 8.57 | 100 | 900 | 0.00% | 0 |
| fixed_window | 100 | 76.4 | 76.4 | 2.20 | 6.05 | 11.38 | 2000 | 0 | 0.00% | 0 |
| sliding_window | 100 | 76.2 | 76.2 | 1.90 | 5.69 | 10.35 | 2001 | 0 | 0.00% | 0 |
| token_bucket | 100 | 76.2 | 76.2 | 1.98 | 5.98 | 8.64 | 2001 | 0 | 0.00% | 0 |
