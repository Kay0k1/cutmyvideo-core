# Matched media contract verification

This directory preserves one complete, matched functional verification of all seven required media-contract test families. Both runs use the same final test helper, the same seven fixture/provenance files, Go 1.27.2 and FFmpeg N-127083-g65a3870462-20261001. The tests inspect input metadata independently and check native 8-bit YCbCr input patches, decoded RGB frames, 10-bit YUV, PCM audio and presentation timestamps. They exercise accurate fast/compact, compatible copy and preview.

The baseline runtime is immutable commit `b59e1d22adc3f20533bf3b54243bd6c28e6c6b48`, with only the final new tests/fixtures copied into its isolated checkout. The after run uses the frozen production source from commit `6fe8c1b9696b8c6e59014043f31bed844152aded`, with the corrected new test helper. Metadata records the baseline Git head as `b59e1d2`, the after head as `6fe8c1b`, and `source_dirty: true` for both; the source digests identify the actual runtime contents. Runtime, helper and fixtures remained unchanged during each run. No production or private customer data was used.

| Family | Baseline result | After result |
|---|---|---|
| Rotation/display geometry | Rotations already work; copy 720p incorrectly accepts display height 800 when coded height is 450 | PASS: 90/180/270° colored corners, normalized geometry and display-height cap |
| Sample aspect ratio | Accurate remains 720×576/SAR16:15; preview is 600×480/SAR16:15 | PASS: accurate 768×576 and preview 640×480 with square pixels; copy preserves original SAR |
| Default audio selection | First 440 Hz track is chosen instead of the default second 880 Hz track | PASS: actual decoded tone confirms default selection and first-track fallback when no default exists |
| Attached picture/audio-only | PASS already | PASS: attached cover is not playable video; MP3 contains the selected audio |
| Copy quality/selected video | Unselected 722-pixel video blocks copy of selected 96-pixel video | PASS: selected stream and decoded corner identities are correct |
| PQ/HLG color | Accurate/preview produce 8-bit output still tagged HDR/BT.2020 | PASS: declared SDR BT.709/TV, neutral/color patches and distinct highlights; HDR copy preserves 10-bit pixels/tags; MP3 remains independent |
| VFR/mono-silence | Accurate fast/compact round 207→200 ms; the original nearly silent mono input exhausts the separate eight-second export context | PASS: all nine fine-grid PTS, frame identities and PCM pulse alignment; preview has its declared 24 fps cadence; original mono-silence export completes within its context |

After: **7/7 PASS with `-race`, no skips**. Baseline: six families fail, while the attached-picture/audio-only control passes. The final helper SHA-256 is `edeb4a86e63ea71a883b707a8bcd75e4fd5de5549e0d5e9b1f480b5ed7b27329`. Earlier iterations and partial follow-ups are retained outside the repository; none is substituted for this full final pair.

The SDR corner fixtures explicitly convert nonlinear full-range RGB to BT.709 limited-range YCbCr. Before any product operation, a decoded native YCbCr frame is checked against independent coefficients from [ITU-R BT.709-6 (June 2015), sections 3.2–3.4](https://www.itu.int/dms_pubrec/itu-r/rec/bt/R-REC-BT.709-6-201506-I!!PDF-E.pdf): luma `0.2126/0.7152/0.0722`, chroma divisors `1.8556/1.5748`, and 8-bit coding `16+219Y`, `128+224Cb/Cr`. Flat patch samples allow four code values for integer conversion and H.264 quantization. The decoded RGB input and output tolerance remains **35**. This check rejects physical BT.601 content carrying BT.709 labels; setting tags alone cannot pass it.

Actual FFmpeg 6.1 diagnostics found that the previous implicit conversion produced green input RGB `(3,205,13)` with the same preview color, despite BT.709 tags. The explicit matrix produced input/preview RGB `(18,237,17)` and matched independent YCbCr values within one code value. That was an input fixture defect, not a preview rendering defect. This directory contains a fresh complete pair using the corrected helper on both runtimes. The [earlier immutable6fe payload](https://github.com/Kay0k1/cutmyvideo-core/tree/6fe8c1b9696b8c6e59014043f31bed844152aded/verification/2026-10-10-media-contract) and its receipts are retained; its raw runs are not substituted into this pair. The correction changes tests/provenance only; the production-source digests below remain unchanged.

Copy bounds use the independently observed first selected playable video PTS, rounded to milliseconds, rather than assuming that a zero-origin request implies a zero-origin container. A FFmpeg 5.1-generated MKV can legitimately place that first video frame at **21 ms** because of AAC encoder delay. The oracle requires that exact input-derived start; accurate still requires start zero. The existing 100 ms end tolerance was not widened. The matched runs here use the operator build above; native/Bookworm evidence is separate.

The original 25 Hz mono-silence fixture is retained byte-for-byte with its own SHA/provenance; the test separately generates the finer VFR input. It has a 1000 Hz pulse from 0.600–0.700 s and silence elsewhere. The old mono AAC 192k operation reaches an independent **eight-second deadline** on the operator build; the current mono AAC 128k operation completes and preserves decoded media. This is a functional cancellation/completion regression, **not an encoding speed benchmark or a general eight-second processing SLA**. Package elapsed times include the intentional baseline timeout and must not be advertised as a speedup.

`final-matched-manifest.json` records artifact hashes, tools, exact helper/fixture identities and each family's status. `*-run.json` additionally contains source path/hash lists and the command/environment provenance. `runtime_source_sha256` follows the established sorted `cmd/internal/pkg` `.go/.sql/.json` protocol excluding `*_test.go`; it includes fixture JSON. `production_only_sha256` also excludes `testdata` and isolates production source:

| Identity | Baseline | After |
|---|---|---|
| Production-only SHA-256 | `a69e36776391de545a3b9e11402c1e28a86287fa802deedb3625f7d05bcbe7b5` | `817e475ec50cc932613ff8142eb49c66ce57145c2bced526227186cd565b7e9a` |
| Runtime protocol SHA-256 | `b2e63149d468b7a35cde3e930f19fa6438adf801462947aaf8f72a3e8cbde0b0` | `311473c2757c8436c238cde2d1a6df5eca09197bc5fe1d0f91415a68ea1d6280` |

The two `.jsonl.gz` files contain the exact original JSONL streams. Metadata's uncompressed `.jsonl` names are logical identifiers; the uncompressed raw files are not duplicated here. Their recorded raw SHA-256 values can be verified after decompression. Exact empty stderr files are included. Gzip headers have fixed timestamps and no local filename.

Physical PQ/HLG fixtures use the transfer functions in [ITU-R BT.2100-3 (February 2025), Tables 4–5](https://www.itu.int/dms_pubrec/itu-r/rec/bt/R-REC-BT.2100-3-202502-I!!PDF-E.pdf); their tags, exact 10-bit samples and SHA values are checked before output assertions. This evidence does not certify Dolby Vision, dynamic HDR, every mastering-metadata variant, all platforms, or overall 1.0 readiness. Required native, service, recovery and full-suite checks are recorded separately.

Raw diagnostics were retired from the working tree on 2026-10-13. The [original files](https://github.com/Kay0k1/cutmyvideo-core/tree/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/verification/2026-10-10-media-contract) remain in Git history; future runs follow [artifact retention](../../docs/artifacts.md).
