Baseline `a9c75df83f965bf1d56e2263ca585ab77aeaf305`; candidate source catalog `3503f2bd511ce8fee94b174085fd0a151c7b787ed7e910e1561d32a3a7708e07`. Primary corpus: 567 observations / 72 case comparisons / 4542 independently decoded files. Including completed separate controls: 627 media operations / 4792 files. Pure-function benchmark rows are counted separately.

Cells show baseline → candidate cohort medians, except mixed C0 → C1. Ratio is the median of matching-pair baseline/candidate ratios; brackets show the full pair range and W/N is candidate wins. >1 favors candidate. No overall speedup combines these different methods. RSS/workspace/I/O are sampled lower bounds in MiB; I/O shows physical read/write. CPU is waited whole-helper seconds. FFmpeg / FFprobe columns count subprocess launches, including their counting shells in this primary corpus.

Selected frozen local/embedding cases

| Case | Paired ratio [range]; W/N | Wall s | CPU s | RSS LB MiB | Workspace LB MiB | I/O LB MiB R/W | FFmpeg / FFprobe |
| --- | --- | --- | --- | --- | --- | --- | --- |
| embedded-accurate-mp4-01 | 1.22× [1.19, 1.29]; 5/5 | 0.308 → 0.247 | 0.318 → 0.256 | 71.7 → 69.8 | 0.019 → 0.019 | 0 → 0 / 0.043 → 0.0586 | 1 → 1 / 3 → 2 |
| embedded-accurate-mp4-32 | 0.959× [0.925, 0.989]; 0/5 | 0.806 → 0.834 | 1.08 → 1.12 | 75.1 → 75 | 0.593 → 0.593 | 0 → 0 / 1.12 → 1.12 | 1 → 1 / 2 → 2 |
| embedded-copy-mkv-01 | 1.21× [1.09, 1.28]; 5/5 | 0.347 → 0.287 | 0.348 → 0.289 | 60.8 → 61.1 | 0.0151 → 0.0151 | 0 → 0 / 0.0508 → 0.0352 | 1 → 1 / 4 → 3 |
| embedded-copy-mkv-32 | 1.25× [1.19, 1.27]; 5/5 | 13.6 → 10.8 | 13.9 → 11.1 | 66.8 → 67.2 | 0.978 → 0.962 | 0 → 0 / 2.27 → 1.65 | 97 → 64 / 98 → 92 |
| local-accurate-mp4-separate-32 | 1.04× [1.03, 1.1]; 5/5 | 5.65 → 5.45 | 6 → 5.78 | 78.2 → 78.1 | 0.617 → 0.617 | 0 → 0 / 0.977 → 0.988 | 32 → 31 / 33 → 32 |
| local-accurate-mp4-joined-32 | 0.989× [0.95, 1.01]; 1/5 | 0.812 → 0.825 | 1.09 → 1.1 | 77.6 → 79 | 0.593 → 0.593 | 0 → 0 / 1.12 → 1.2 | 1 → 1 / 2 → 2 |
| local-copy-mp4-joined-32 | 1.05× [1.03, 1.08]; 5/5 | 13.9 → 13.2 | 14.2 → 13.5 | 72.1 → 71.9 | 1.04 → 1.02 | 0 → 0 / 2.3 → 2.27 | 97 → 94 / 98 → 92 |
| local-copy-mkv-joined-32 | 1.24× [1.21, 1.28]; 5/5 | 13.8 → 11.1 | 14.1 → 11.4 | 71.6 → 73.1 | 0.978 → 0.962 | 0 → 0 / 2.3 → 1.62 | 97 → 64 / 98 → 92 |
| complex-accurate-mp4-separate-20 | 1.24× [1.18, 1.25]; 5/5 | 9.55 → 7.76 | 18.6 → 15 | 131 → 131 | 36.9 → 35 | 0 → 0 / 71.5 → 62.9 | 20 → 16 / 21 → 17 |
| complex-accurate-mp4-joined-20 | 0.975× [0.959, 1.02]; 2/5 | 6.37 → 6.55 | 15.7 → 16.1 | 141 → 141 | 37.5 → 37.5 | 0 → 0 / 74.9 → 75 | 1 → 1 / 2 → 2 |

All 8 HLS cases: measured phase and outer invocation

| Case | Phase ratio [range]; W/N | Phase s | Outer ratio [range]; W/N | Outer s | Phase KiB / requests | Total KiB / requests |
| --- | --- | --- | --- | --- | --- | --- |
| hls-accurate-mp4-separate-cold | 0.987× [0.954, 1.02]; 1/3 | 3.76 → 3.74 | 0.987× [0.954, 1.02]; 1/3 | 3.78 → 3.76 | 352 → 152 / 25 → 11 | 352 → 153 / 26 → 12 |
| hls-accurate-mp4-separate-warm | 1.01× [0.999, 1.05]; 2/3 | 3.73 → 3.71 | 1.02× [1.01, 1.03]; 3/3 | 5.6 → 5.51 | 380 → 0 / 28 → 0 | 760 → 95.6 / 57 → 8 |
| hls-accurate-mp4-separate-straggler | 1.06× [1.03, 1.1]; 5/5 | 4.27 → 3.99 | 1.06× [1.03, 1.1]; 5/5 | 4.29 → 4.01 | 352 → 152 / 25 → 11 | 352 → 153 / 26 → 12 |
| hls-accurate-mp4-separate-overlap | 0.988× [0.964, 1.05]; 2/5 | 3.72 → 3.71 | 0.988× [0.964, 1.05]; 2/5 | 3.74 → 3.73 | 380 → 95.1 / 28 → 7 | 380 → 95.6 / 29 → 8 |
| hls-copy-mkv-joined-cold | 1.07× [1.05, 1.07]; 3/3 | 6.93 → 6.47 | 1.07× [1.05, 1.07]; 3/3 | 6.96 → 6.49 | 352 → 152 / 25 → 11 | 352 → 153 / 26 → 12 |
| hls-copy-mkv-joined-warm | 1.09× [1.08, 1.11]; 3/3 | 7.05 → 6.54 | 1.07× [1.06, 1.08]; 3/3 | 8.83 → 8.35 | 380 → 0 / 28 → 0 | 760 → 95.6 / 57 → 8 |
| hls-copy-mkv-joined-straggler | 1.12× [1.1, 1.15]; 5/5 | 7.62 → 6.83 | 1.12× [1.1, 1.15]; 5/5 | 7.65 → 6.85 | 352 → 152 / 25 → 11 | 352 → 153 / 26 → 12 |
| hls-copy-mkv-joined-overlap | 1.1× [1.07, 1.12]; 5/5 | 7.11 → 6.46 | 1.1× [1.07, 1.12]; 5/5 | 7.13 → 6.48 | 380 → 95.1 / 28 → 7 | 380 → 95.6 / 29 → 8 |

Warm phase excludes prefill; the outer invocation and total traffic include it. Transport is a fixed fixture, not a public-network measurement.

HLS resource medians

| Case | CPU s | RSS LB MiB | Workspace LB MiB | I/O LB MiB R/W | FFmpeg / FFprobe |
| --- | --- | --- | --- | --- | --- |
| hls-accurate-mp4-separate-cold | 3.77 → 3.76 | 79.8 → 80.7 | 0.388 → 0.537 | 0 → 0 / 1.26 → 1.27 | 16 → 16 / 32 → 32 |
| hls-accurate-mp4-separate-warm | 5.5 → 5.59 | 81.9 → 82.3 | 0.647 → 0.74 | 0 → 0 / 2.46 → 2.52 | 24 → 24 / 48 → 48 |
| hls-accurate-mp4-separate-straggler | 3.78 → 3.76 | 80 → 80.7 | 0.388 → 0.537 | 0 → 0 / 1.32 → 1.34 | 16 → 16 / 32 → 32 |
| hls-accurate-mp4-separate-overlap | 3.73 → 3.78 | 80 → 81.2 | 0.401 → 0.494 | 0 → 0 / 1.34 → 1.37 | 16 → 16 / 32 → 32 |
| hls-copy-mkv-joined-cold | 6.92 → 6.46 | 70.7 → 71.3 | 0.5 → 0.649 | 0 → 0 / 1.7 → 1.46 | 33 → 26 / 65 → 65 |
| hls-copy-mkv-joined-warm | 8.7 → 8.39 | 72.8 → 72.7 | 0.777 → 0.87 | 0 → 0 / 2.8 → 2.62 | 41 → 34 / 81 → 81 |
| hls-copy-mkv-joined-straggler | 7.12 → 6.57 | 71.9 → 72.4 | 0.5 → 0.649 | 0 → 0 / 1.72 → 1.39 | 33 → 26 / 65 → 65 |
| hls-copy-mkv-joined-overlap | 7.1 → 6.5 | 71.6 → 71.4 | 0.53 → 0.623 | 0 → 0 / 1.73 → 1.54 | 33 → 26 / 65 → 65 |

All 6 server P0 cases: actual HTTP burst through all downloaded bytes

| Case | Burst ratio [range]; W/N | Burst s | Whole-helper CPU s | RSS LB MiB | Workspace LB MiB | I/O LB MiB R/W | FFmpeg / FFprobe |
| --- | --- | --- | --- | --- | --- | --- | --- |
| server-accurate-mp4-1 | 1.06× [0.937, 1.14]; 3/5 | 4.12 → 3.9 | 8.64 → 8.09 | 382 → 382 | 61.2 → 61.2 | 0 → 0 / 135 → 135 | 8 → 8 / 13 → 11 |
| server-accurate-mp4-4 | 1.03× [0.982, 1.07]; 4/5 | 15.3 → 14.8 | 32.4 → 31.7 | 436 → 436 | 245 → 245 | 0.00781 → 0.0156 / 541 → 488 | 32 → 32 / 52 → 40 |
| server-copy-mp4-1 | 1.01× [0.977, 1.2]; 3/5 | 1.58 → 1.58 | 2.88 → 2.8 | 254 → 254 | 50.7 → 50.7 | 0 → 0 / 105 → 138 | 8 → 8 / 21 → 19 |
| server-copy-mp4-4 | 1.12× [1.06, 1.19]; 5/5 | 4.82 → 4.3 | 10.8 → 9.84 | 310 → 309 | 203 → 203 | 0 → 0 / 417 → 384 | 32 → 32 / 84 → 72 |
| server-mp3-1 | 0.995× [0.984, 1.02]; 2/5 | 1.06 → 1.06 | 2.14 → 1.97 | 232 → 253 | 34.5 → 34.5 | 0 → 0 / 69.2 → 103 | 8 → 8 / 13 → 11 |
| server-mp3-4 | 1.09× [1.08, 1.29]; 5/5 | 3.22 → 2.95 | 7.31 → 6.69 | 308 → 308 | 138 → 138 | 0.00781 → 0 / 276 → 277 | 32 → 32 / 52 → 40 |

Server role and PostgreSQL resource medians

| Case | API CPU s | Worker CPU s | API memory LB MiB | Worker memory LB MiB | PG interval CPU s | PG endpoint memory after MiB |
| --- | --- | --- | --- | --- | --- | --- |
| server-accurate-mp4-1 | 0.254 → 0.253 | 7.85 → 7.28 | 53.2 → 48.7 | 172 → 172 | 0.319 → 0.33 | 179 → 179 |
| server-accurate-mp4-4 | 0.961 → 1 | 30.1 → 29.1 | 158 → 158 | 216 → 216 | 0.599 → 0.593 | 174 → 173 |
| server-copy-mp4-1 | 0.24 → 0.244 | 2.23 → 2.15 | 49.8 → 49.7 | 42.1 → 38.9 | 0.301 → 0.315 | 182 → 183 |
| server-copy-mp4-4 | 0.921 → 0.932 | 9.1 → 8.14 | 158 → 158 | 72.7 → 66.8 | 0.503 → 0.504 | 180 → 181 |
| server-mp3-1 | 0.231 → 0.235 | 1.55 → 1.36 | 49.8 → 49.1 | 32.3 → 33.5 | 0.304 → 0.326 | 187 → 188 |
| server-mp3-4 | 0.874 → 0.833 | 5.79 → 5.14 | 154 → 157 | 41.4 → 38.2 | 0.467 → 0.496 | 200 → 201 |

Server burst has 250 ms completion polling. Role groups are separate API/worker processes. PG values cover helper endpoints including setup; endpoint memory and container-lifetime peaks are not per-run peaks. Kernel peaks stay null when unavailable.

Explicitly retained MP3 joined cases

| Case | Paired ratio [range]; W/N | Wall s | CPU s | RSS LB MiB | Workspace LB MiB | I/O LB MiB R/W | FFmpeg / FFprobe |
| --- | --- | --- | --- | --- | --- | --- | --- |
| local-mp3-joined-02 | 0.919× [0.85, 1.13]; 1/3 | 0.216 → 0.225 | 0.215 → 0.224 | 64.8 → 65.6 | 0.0525 → 0.0525 | 0 → 0 / 0.0781 → 0.0781 | 1 → 1 / 2 → 2 |
| local-mp3-joined-32 | 0.951× [0.928, 1.14]; 2/5 | 0.481 → 0.481 | 0.475 → 0.462 | 68.2 → 67.3 | 0.814 → 0.814 | 0 → 0 / 1.59 → 1.59 | 1 → 1 / 2 → 2 |

A paired median below 1 is a measured regression; these cases remain in the report alongside gains.

Supplemental controlled mixed pipeline

| Comparison | Paired ratio [range]; W/N | Burst s | CPU s | RSS LB MiB | Workspace LB MiB | I/O LB MiB R/W | FFmpeg / FFprobe |
| --- | --- | --- | --- | --- | --- | --- | --- |
| candidate P0 → candidate P1 | 1.06× [1.06, 1.13]; 5/5 | 8.64 → 8.13 | 21.5 → 20.9 | 371 → 316 | 149 → 149 | 0 → 0 / 190 → 190 | 20 → 20 / 38 → 38 |

Three baseline references remain unpaired in JSON. This is one process with separate Stores, fixed 250 ms transport and 500 ms polling. P1 is opt-in; the earlier native/tool timeout remains unresolved despite later successful runs.

Pure functions: 20 rows, two benchmarks, five pairs each

| Benchmark | Paired ratio [range]; W/N | ns/op B → C | B/op B → C | allocs/op B → C |
| --- | --- | --- | --- | --- |
| PCM32MiB | 4.92× [4.65, 5.11]; 5/5 | 6.25e+07 → 1.32e+07 | 1.64e+04 → 1.64e+04 | 1 → 1 |
| SubtitleRebase10000Cues32Cuts | 4.12× [3.87, 4.48]; 5/5 | 3.09e+05 → 7.65e+04 | 2.24e+04 → 1.04e+05 | 421 → 422 |

Separate 60-operation unwrapped counting-shell control

| Case | Unwrapped paired ratio [range]; W/N | Wall s B → C | CPU s B → C | Primary wrapped ratio |
| --- | --- | --- | --- | --- |
| embedded-accurate-mp4-01 | 1.34× [1.23, 1.48]; 5/5 | 0.307 → 0.237 | 0.309 → 0.242 | 1.22× [1.19, 1.29]; 5/5 |
| embedded-accurate-mp4-32 | 1.1× [0.955, 1.13]; 4/5 | 0.835 → 0.789 | 1.11 → 1.05 | 0.959× [0.925, 0.989]; 0/5 |
| embedded-copy-mkv-01 | 1.31× [1.21, 1.47]; 5/5 | 0.361 → 0.28 | 0.362 → 0.28 | 1.21× [1.09, 1.28]; 5/5 |
| embedded-copy-mkv-32 | 1.24× [1.23, 1.25]; 5/5 | 13.6 → 11 | 13.9 → 11.2 | 1.25× [1.19, 1.27]; 5/5 |
| local-copy-mp4-joined-32 | 1.05× [1.03, 1.06]; 5/5 | 13.5 → 12.9 | 13.8 → 13.3 | 1.05× [1.03, 1.08]; 5/5 |
| complex-accurate-mp4-separate-20 | 1.24× [1.2, 1.29]; 5/5 | 9.76 → 7.7 | 19 → 15 | 1.24× [1.18, 1.25]; 5/5 |

Unwrapped counts are unobserved. The later control does not isolate per-launch cost from OS-cache/order effects and does not measure mixed Go-wrapper overhead. Full raw witnesses, unrounded metrics and fingerprints remain outside Git. No competitor or production-SLA claim.
