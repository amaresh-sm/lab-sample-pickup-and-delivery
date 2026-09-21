# Benchmarking Data

Source of truth for scores: scores.json. Token, tool, cost, duration, file, LOC, dependency, and largest-file metrics are taken from each run telemetry/workspace when available; — means the run did not record that metric.

| Run-name (unique Id) | Model Name | Reasoning | score | token | tool calls | Iteration steps | Est cost | Reported cost | Duration | Total Files | Loc | deps | largest file |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|
| openhands-grok-4.6-medium | grok-4.6 | medium | 0.6976 | — | — | — | — | — | — | 41 | 35,730 | 12 | contracts/openapi.contract.json (12570 LOC) |
| openhands-grok-4.6-high-20260920-dev-v2 | grok-4.6 | high | 0.6824 | — | — | — | — | — | 10202.4s | 14 | 5,732 | 4 | contracts/openapi.contract.json (12570 LOC) |
| openhands-1789971385 | claude-opus-5 | medium | 0.6546 | 35,644,216 | 417 | 417 | $561.45 | — | 6237.2s | 2,786 | 43,865 | 13 | contracts/openapi.contract.json (12570 LOC) |
| openhands-grok-4.5-medium-20260916-v2 | grok-4.5 | medium | 0.5750 | 10,982,706 | 90 | 90 | $8.99 | — | 2125.7s | 613 | 41,826 | 15 | contracts/openapi.contract.json (12570 LOC) |
| openhands-grok-4.5-high-20260916-v3 | grok-4.5 | high | 0.5252 | 10,095,082 | 91 | 91 | $4.78 | $4.78 | 1934.0s | 2,013 | 35,773 | 13 | contracts/openapi.contract.json (12570 LOC) |
| openhands-1789967852 | claude-opus-5 | high | 0.5249 | 42,393,320 | 562 | 562 | $668.05 | — | 8072.1s | 40 | 37,499 | 0 | contracts/openapi.contract.json (12570 LOC) |
| openhands-kimi-k3-high-20260920-prod-v3 | kimi-k3 | high | 0.4872 | 25,418,117 | 622 | 622 | $17.41 | $17.41 | 4494.8s | 43 | 38,170 | 9 | contracts/openapi.contract.json (12570 LOC) |
| openhands-glm-5.2-high-20260917-v2 | glm-5.2 | high | 0.4824 | 40,022,736 | 256 | 256 | $11.64 | $10.76 | 2273.9s | 63 | 35,311 | 13 | contracts/openapi.contract.json (12570 LOC) |
| openhands-kimi-k3-high-20260919-v1 | kimi-k3 | high | 0.4775 | 29,268,903 | 724 | 724 | $18.21 | $18.21 | 10361.2s | 44 | 37,877 | 12 | contracts/openapi.contract.json (12570 LOC) |
| openhands-gpt-5.6-sol-medium-20260919-queue-v1 | gpt-5.6-sol | medium | 0.4724 | 2,633,901 | 80 | 80 | $1.09 | — | 978.6s | 559 | 29,877 | 12 | contracts/openapi.contract.json (12570 LOC) |
| openhands-1789739130 | gpt-5.6-sol | high | 0.4720 | 8,843,919 | 254 | 254 | $2.97 | — | 2828.7s | 542 | 30,504 | 10 | contracts/openapi.contract.json (12570 LOC) |
| openhands-1789963426 | claude-sonnet-5 | medium | 0.4322 | 22,438,548 | 317 | 317 | $69.83 | — | 2989.2s | 505 | 34,746 | 12 | contracts/openapi.contract.json (12570 LOC) |
| openhands-gpt-5.6-sol-medium-20260919-v2 | gpt-5.6-sol | medium | 0.4239 | 2,626,424 | 75 | 75 | $1.05 | — | 875.9s | 20 | 28,949 | 12 | contracts/openapi.contract.json (12570 LOC) |
| openhands-1789931908 | grok-4.5 | high | 0.4095 | 10,725,595 | 155 | 155 | $5.56 | $5.56 | 2714.6s | 1,983 | 35,440 | 16 | contracts/openapi.contract.json (12570 LOC) |
| openhands-grok-4.6-medium-20260920-dev-v1 | grok-4.6 | medium | 0.3970 | 57,271,587 | 2,660 | 2,660 | $118.99 | — | 11448.4s | 18 | 34,722 | 14 | contracts/openapi.contract.json (12570 LOC) |
| openhands-kimi-k3-high-20260919-v2 | kimi-k3 | high | 0.3829 | 23,472,726 | 515 | 515 | $13.38 | $13.38 | 5504.2s | 1,987 | 37,409 | 13 | contracts/openapi.contract.json (12570 LOC) |
| openhands-gpt-5.6-sol-high-20260920-prod-v2 | gpt-5.6-sol | high | 0.3745 | 7,104,360 | 193 | 193 | $2.52 | — | 1944.7s | 404 | 30,755 | 12 | contracts/openapi.contract.json (12570 LOC) |
| openhands-1789936188 | claude-sonnet-5 | medium | 0.3735 | 29,826,977 | 363 | 363 | $92.31 | — | 3555.6s | 91 | 34,398 | 10 | contracts/openapi.contract.json (12570 LOC) |
| openhands-kimi-k3-medium-20260920-prod-v1 | kimi-k3 | medium | 0.3705 | 21,999,065 | 657 | 657 | $14.53 | $14.53 | 4132.4s | 43 | 34,087 | 9 | contracts/openapi.contract.json (12570 LOC) |
| openhands-grok-4.5-medium-20260921-prod-v1 | grok-4.5 | medium | 0.3545 | 13,680,192 | 240 | 240 | $7.29 | $7.29 | 3473.6s | 44 | 36,398 | 13 | contracts/openapi.contract.json (12570 LOC) |
| openhands-kimi-k3-medium-20260920-prod-queue-v1 | kimi-k3 | medium | 0.3512 | 10,061,094 | 229 | 229 | $6.03 | $6.03 | 2350.9s | 1,635 | 46,342 | 11 | contracts/openapi.contract.json (12570 LOC) |
| openhands-grok-4.5-medium-20260916-retry | grok-4.5 | medium | 0.3393 | 7,039,169 | 89 | 89 | $6.52 | — | 1611.1s | 523 | 21,588 | 13 | contracts/openapi.contract.json (12570 LOC) |
| openhands-1789940522 | claude-sonnet-5 | medium | 0.3348 | 38,718,964 | 481 | 481 | $119.89 | — | 4916.2s | 639 | 35,723 | 14 | contracts/openapi.contract.json (12570 LOC) |
| openhands-glm-5.2-high-20260920-prod-v1 | glm-5.2 | high | 0.3303 | 8,952,927 | 250 | 250 | $3.19 | $3.19 | 694.5s | 31 | 31,116 | 2 | contracts/openapi.contract.json (12570 LOC) |
| openhands-1789943763 | gpt-5.6-sol | high | 0.3140 | 8,095,812 | 138 | 138 | $2.30 | — | 1605.7s | 544 | 31,763 | 11 | contracts/openapi.contract.json (12570 LOC) |
| openhands-grok-4.6-medium-20260920-dev-queue-v1 | grok-4.6 | medium | 0.3109 | 71,342,800 | 4,532 | 4,532 | $146.96 | — | 12278.4s | 28 | 34,541 | 14 | contracts/openapi.contract.json (12570 LOC) |
| openhands-grok-4.5-high-20260919-v1 | grok-4.5 | high | 0.2792 | 10,667,798 | 188 | 188 | $5.85 | $5.85 | 2583.1s | 51 | 35,921 | 15 | contracts/openapi.contract.json (12570 LOC) |
| openhands-gpt-5.6-terra-high-v2 | gpt-5.6-terra | high | 0.2771 | 2,314,196 | 43 | 43 | $3.15 | — | 594.6s | 11 | 74,006 | 0 | .lab-data/state.json (47021 LOC) |
| codex-1789497895 | gpt-5.6-terra | medium | 0.2762 | — | — | — | — | — | 409.7s | 12 | 973 | 0 | contracts/openapi.contract.json (12570 LOC) |
| openhands-gpt-5.6-sol-high-20260919-v1 | gpt-5.6-sol | high | 0.2694 | 3,008,477 | 114 | 114 | $1.28 | — | 1022.0s | 562 | 29,916 | 9 | contracts/openapi.contract.json (12570 LOC) |
| openhands-glm-5.2-medium-20260919-v3 | glm-5.2 | medium | 0.2659 | 48,401,399 | 1,038 | 1,038 | $16.83 | $16.83 | 3250.8s | 1,020 | 36,617 | 11 | contracts/openapi.contract.json (12570 LOC) |
| openhands-kimi-k3-medium-20260919-v2 | kimi-k3 | medium | 0.2632 | 15,140,046 | 395 | 395 | $8.43 | $8.43 | 4576.0s | 28 | 33,840 | 9 | contracts/openapi.contract.json (12570 LOC) |
| openhands-glm-5.2-high-20260919-v2 | glm-5.2 | high | 0.2501 | — | — | — | — | — | — | 35 | 34,997 | 5 | contracts/openapi.contract.json (12570 LOC) |
| openhands-gpt-5.6-luna-high | gpt-5.6-luna | high | 0.2450 | 3,835,905 | 65 | 65 | $5.16 | — | 596.1s | 19 | 50,903 | 5 | data/state.json (22500 LOC) |
| openhands-gpt-5.6-sol-medium-20260920-prod-v1 | gpt-5.6-sol | medium | 0.2316 | 3,565,087 | 91 | 91 | $1.33 | — | 1058.3s | 26 | 27,487 | 12 | contracts/openapi.contract.json (12570 LOC) |
| openhands-gpt-5.6-terra-high | gpt-5.6-terra | high | 0.2237 | 2,064,199 | 45 | 45 | $2.81 | — | 315.8s | 11 | 26,395 | 0 | contracts/openapi.contract.json (12570 LOC) |
| openhands-1789710691 | gpt-5.6-luna | high | 0.2191 | 3,205,163 | 58 | 58 | $4.39 | — | 638.6s | 15 | 41,389 | 6 | data/state.json (13099 LOC) |
| openhands-1789709196 | gpt-5.6-terra | high | 0.2133 | 2,830,709 | 64 | 64 | $3.87 | — | 408.9s | 14 | 159,322 | 0 | data/state.json (132240 LOC) |
| openhands-1789710694 | gpt-5.6-luna | high | 0.1872 | 3,159,027 | 77 | 77 | $4.37 | — | 645.2s | 16 | 28,779 | 5 | contracts/openapi.contract.json (12570 LOC) |
| openhands-gemini-3.7-flash-high-20260918-v3 | gemini-3.7-flash | high | 0.1803 | 24,188,594 | 479 | 479 | $3.42 | — | 2236.1s | 56 | 67,913 | 10 | openapi_full_dump.json (12570 LOC) |
| repaired-openhands-1789968768 | claude-sonnet-5 | high | 0.1696 | — | — | — | — | — | 6425.0s | 26 | 3,813 | 5 | contracts/openapi.contract.json (12570 LOC) |
| openhands-gpt-5.6-luna-medium-20260920-dev-v1 | gpt-5.6-luna | medium | 0.1681 | 1,009,994 | 33 | 33 | $0.36 | — | 178.0s | 11 | 78,821 | 0 | data/state.json (52560 LOC) |
| openhands-1789932080 | glm-5.2 | medium | 0.1627 | 12,856,227 | 266 | 266 | $4.66 | $4.66 | 1329.9s | 20 | 30,542 | 3 | contracts/openapi.contract.json (12570 LOC) |
| openhands-gemini-3.7-flash-high-20260918-v4 | gemini-3.7-flash | high | 0.1478 | 25,518,173 | 479 | 479 | $3.69 | — | 2511.2s | 61 | 42,185 | 16 | contracts/openapi.contract.json (12570 LOC) |
| openhands-gemini-3.7-flash-high-20260917-v2 | gemini-3.7-flash | high | 0.1448 | 36,597,987 | 273 | 273 | $3.93 | — | 1752.8s | 58 | 42,088 | 18 | contracts/openapi.contract.json (12570 LOC) |
| openhands-1789716816 | gemini-3.7-flash | medium | 0.1416 | 23,127,630 | 483 | 483 | $3.33 | — | 2684.7s | 40 | 39,527 | 17 | contracts/openapi.contract.json (12570 LOC) |
| openhands-glm-5.2-medium-20260920-prod-v1 | glm-5.2 | medium | 0.1386 | 13,557,764 | 291 | 291 | $4.99 | $4.99 | 1804.9s | 43 | 32,461 | 4 | contracts/openapi.contract.json (12570 LOC) |
| openhands-minimax-m3-high-20260920-prod-v1 | minimax-m3 | high | 0.1316 | 11,245,138 | 311 | 311 | $1.88 | $0.94 | 1182.5s | 35 | 31,857 | 5 | contracts/openapi.contract.json (12570 LOC) |
| openhands-gpt-5.6-terra-medium | gpt-5.6-terra | medium | 0.1276 | 1,156,753 | 30 | 30 | $1.60 | — | 215.0s | 11 | 26,091 | 0 | contracts/openapi.contract.json (12570 LOC) |
| openhands-deepseek-v4-pro-high-20260920-prod-v1 | deepseek-v4-pro | high | 0.1110 | 30,093,390 | 828 | 828 | $4.65 | $14.82 | 9024.8s | 44 | 37,555 | 16 | contracts/openapi.contract.json (12570 LOC) |
| openhands-1789817282 | minimax-m3 | high | 0.1049 | 8,661,928 | 225 | 225 | $1.50 | $0.75 | 1568.9s | 23 | 31,490 | 5 | contracts/openapi.contract.json (12570 LOC) |
| openhands-1789926104 | deepseek-v4-pro | high | 0.1028 | 19,713,439 | 477 | 477 | $2.03 | $7.03 | 4648.5s | 12 | 29,903 | 0 | contracts/openapi.contract.json (12570 LOC) |
| openhands-gemini-3.7-flash-medium-20260918-v1 | gemini-3.7-flash | medium | 0.1014 | — | — | — | — | — | 1786.0s | 1,032 | 13,075 | 10 | contracts/openapi.contract.json (12570 LOC) |
| openhands-minimax-m3-high-20260919-v1 | minimax-m3 | high | 0.0991 | — | — | — | — | — | — | 48 | 36,444 | 8 | contracts/openapi.contract.json (12570 LOC) |
| openhands-1789720533 | gemini-3.7-flash | medium | 0.0953 | 14,640,260 | 289 | 289 | $2.14 | — | 1554.0s | 48 | 38,582 | 19 | contracts/openapi.contract.json (12570 LOC) |
| openhands-minimax-m3-medium-20260920-prod-v2 | minimax-m3 | medium | 0.0890 | 19,408,310 | 470 | 470 | $3.27 | $1.64 | 2868.6s | 35 | 34,616 | 12 | contracts/openapi.contract.json (12570 LOC) |
| openhands-deepseek-v4-pro-medium-20260920-prod-v2 | deepseek-v4-pro | medium | 0.0871 | 6,413,246 | 198 | 198 | $1.69 | $5.07 | 1994.2s | 27 | 31,753 | 12 | contracts/openapi.contract.json (12570 LOC) |
| openhands-minimax-m3-medium-20260920-prod-v3 | minimax-m3 | medium | 0.0858 | 10,459,997 | 288 | 288 | $1.71 | $0.85 | 1310.6s | 28 | 30,254 | 7 | contracts/openapi.contract.json (12570 LOC) |
| openhands-deepseek-v4-pro-medium-20260920-prod-v1 | deepseek-v4-pro | medium | 0.0831 | 13,888,013 | 286 | 286 | $2.40 | $7.60 | 3174.0s | 48 | 34,733 | 12 | contracts/openapi.contract.json (12570 LOC) |
| openhands-gpt-5.6-luna-medium-20260920-dev-v2 | gpt-5.6-luna | medium | 0.0773 | 765,512 | 30 | 30 | $0.29 | — | 155.4s | 11 | 25,852 | 0 | contracts/openapi.contract.json (12570 LOC) |
| openhands-deepseek-v4-pro-high-20260921-prod-v2 | deepseek-v4-pro | high | 0.0707 | — | — | — | — | — | 5095.0s | 29 | 6,098 | 14 | contracts/openapi.contract.json (12570 LOC) |
| openhands-1789709204 | gpt-5.6-luna | medium | 0.0665 | 1,441,124 | 35 | 35 | $1.95 | — | 218.7s | 10 | 26,149 | 0 | contracts/openapi.contract.json (12570 LOC) |
| openhands-minimax-m3-high-20260919-v2 | minimax-m3 | high | 0.0590 | 18,738,582 | 576 | 576 | $3.10 | $1.55 | 2124.9s | 69 | 34,717 | 17 | contracts/openapi.contract.json (12570 LOC) |
| openhands-1789710696 | gpt-5.6-terra | medium | 0.0375 | 1,171,783 | 37 | 37 | $1.64 | — | 221.1s | 11 | 25,669 | 0 | contracts/openapi.contract.json (12570 LOC) |
| openhands-minimax-m3-medium-20260920-prod-v7 | minimax-m3 | medium | 0.0300 | 8,516,451 | 227 | 227 | $1.35 | $0.68 | 1021.7s | 27 | 38,231 | 3 | contracts/openapi.contract.json (12570 LOC) |
| openhands-deepseek-v4-pro-medium-20260920-prod-v3 | deepseek-v4-pro | medium | 0.0300 | 3,222,449 | 78 | 78 | $0.75 | $1.93 | 2390.7s | 1,386 | 28,866 | 4 | contracts/openapi.contract.json (12570 LOC) |

