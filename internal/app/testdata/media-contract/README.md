# Original HDR contract fixtures

The two MP4 files contain original synthetic 128 × 80, two-second H.264 10-bit 4:2:0 / AAC signals. There is no third-party footage. The repository license applies to these fixtures and generator.

Seven neutral bars represent 0, 5, 20, 80, 203, 400 and 1000 cd/m². An eighth bar is a colored display-linear RGB sample (100, 10, 5 cd/m²). `generate.py` calculates PQ inverse EOTF or HLG OETF with reference 1000-nit OOTF, converts nonlinear RGB into BT.2020 nonconstant-luminance limited-range 10-bit YCbCr, then losslessly encodes the tiny frames. Formula references (BT.2100-3, approved February 2025, Table 4 on printed pages 4–5 and Table 5 on pages 5–7): [ITU-R BT.2100-3, Tables 4 and 5](https://www.itu.int/dms_pubrec/itu-r/rec/bt/R-REC-BT.2100-3-202502-I!!PDF-E.pdf).

`manifest.json` records committed byte hashes, expected decoded 10-bit sample values, generation commands, generator hash and actual encoder version. Tests verify input hashes, bit depth, transfer/primaries/matrix/range and decoded physical signal values before assessing outputs. CI decodes the committed fixtures; it does not need a 10-bit encoder to regenerate them. Running `python3 generate.py` regenerates files with the installed FFmpeg; encoder version changes can change compressed bytes and must be reviewed rather than silently accepting new hashes.

The small signals exercise explicit PQ/HLG transfer and color conversion. They do not prove support for dynamic HDR metadata, Dolby Vision or all mastering-display/content-light configurations.

`mono-silence-vfr.mp4` is a separate original 32 × 24 signal retained byte-for-byte from the initial VFR test. `mono-silence-vfr.json` records its SHA, input clock/frame palette and original generation procedure. A 1000 Hz mono pulse occupies 0.600–0.700 s; the rest is silence. On the operator FFmpeg build, encoding this valid input as mono AAC 192k hung; the mandatory test checks that the current operation completes within its own eight-second context and preserves frames/PCM timing. This is a functional deadline regression, not a general eight-second processing SLA. Its manifest is independent of the two HDR fixtures.

## Generated SDR corner inputs

The tests also generate four original nonlinear RGB patches: red `(240,20,20)`, green `(20,240,20)`, blue `(20,20,240)` and white `(240,240,240)`. Their RGB-to-YCbCr conversion explicitly uses BT.709 and limited range before setting geometry and encoding. Tags alone are insufficient: an earlier fixture physically used BT.601 despite its BT.709 labels on FFmpeg 6.1. The input and preview had identical colors; that was a fixture error, not evidence of a preview color regression.

Before any product operation, tests check the input's color tags and decode one native YCbCr frame. Expected patch samples are calculated independently from [ITU-R BT.709-6 (June 2015), sections 3.2–3.4](https://www.itu.int/dms_pubrec/itu-r/rec/bt/R-REC-BT.709-6-201506-I!!PDF-E.pdf): luma coefficients `0.2126/0.7152/0.0722`, chroma divisors `1.8556/1.5748`, and 8-bit limited-range scales `16+219Y`, `128+224Cb/Cr`. The four-value YCbCr tolerance covers integer conversion and H.264 quantization at flat patch interiors; it rejects the mislabeled BT.601 input. The existing decoded RGB corner tolerance remains **35**, both for inputs and outputs.
