#!/usr/bin/env python3
"""Generate original, tiny HDR signals; CI only reads the committed fixtures.

PQ inverse EOTF and HLG OETF/reference 1000-nit OOTF follow ITU-R BT.2100-3
Tables 4/5. These are neutral absolute-luminance bars, not tagged SDR imagery.
"""
import hashlib
import json
import math
import struct
import subprocess
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent
NITS = [0, 5, 20, 80, 203, 400, 1000]


def pq(nits):
    value = (nits / 10000) ** (2610 / 16384)
    return ((3424 / 4096 + 2413 / 4096 * 32 * value) /
            (1 + 2392 / 4096 * 32 * value)) ** (2523 / 4096 * 128)


def hlg_scene(value):
    a = 0.17883277
    b, c = 1 - 4 * a, 0.5 - a * math.log(4 * a)
    return math.sqrt(3 * value) if value <= 1 / 12 else a * math.log(12 * value - b) + c


def hlg(rgb):
    displayed_luma = sum(coefficient * channel for coefficient, channel in zip((.2627, .6780, .0593), rgb))
    if not displayed_luma:
        return (0, 0, 0)
    scene_luma = (displayed_luma / 1000) ** (1 / 1.2)
    gain = 1000 * scene_luma ** .2
    return tuple(hlg_scene(channel / gain) for channel in rgb)


def yuv(rgb):
    r, g, b = rgb
    y = .2627 * r + .6780 * g + .0593 * b
    return (round(64 + 876 * y), round(512 + 896 * (b - y) / (2 * (1 - .0593))),
            round(512 + 896 * (r - y) / (2 * (1 - .2627))))


def main():
    manifest = {"format": 1, "origin": "Original synthetic signal, generated for cutmyvideo-core; repository license applies.",
                "signal_reference": "https://www.itu.int/dms_pubrec/itu-r/rec/bt/R-REC-BT.2100-3-202502-I!!PDF-E.pdf",
                "neutral_bar_luminance_nits": NITS, "hlg_reference_peak_nits": 1000,
                "hlg_system_gamma": 1.2, "color_bar_display_rgb_nits": [100, 10, 5],
                "width": 128, "height": 80, "duration_seconds": 2,
                "generator_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
                "ffmpeg_version": subprocess.check_output(["ffmpeg", "-version"]).decode().splitlines()[0],
                "fixtures": []}
    for transfer, name in (("smpte2084", "pq-10bit.mp4"), ("arib-std-b67", "hlg-10bit.mp4")):
        signals = [(n, n, n) for n in NITS] + [(100, 10, 5)]
        colors = [yuv(tuple(pq(v) for v in rgb) if transfer == "smpte2084" else hlg(rgb)) for rgb in signals]
        raw = b"".join(struct.pack("<H", colors[x // (16 if plane == 0 else 8)][plane])
                       for plane in range(3) for _ in range(80 if plane == 0 else 40)
                       for x in range(128 if plane == 0 else 64))
        with tempfile.TemporaryDirectory() as temporary:
            input_file = Path(temporary) / "signal.yuv"
            input_file.write_bytes(raw)
            command = ["ffmpeg", "-v", "error", "-nostdin", "-y", "-stream_loop", "-1", "-f", "rawvideo",
                       "-pixel_format", "yuv420p10le", "-video_size", "128x80", "-framerate", "5",
                       "-color_primaries", "bt2020", "-color_trc", transfer, "-colorspace", "bt2020nc", "-color_range", "tv",
                       "-i", str(input_file),
                       "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "2", "-map", "0:v:0",
                       "-map", "1:a:0", "-c:v", "libx264", "-preset", "ultrafast", "-qp", "0", "-threads", "1",
                       "-g", "5", "-bf", "0", "-vf", "setsar=1", "-pix_fmt", "yuv420p10le", "-x264-params",
                       f"colorprim=bt2020:transfer={transfer}:colormatrix=bt2020nc", "-color_primaries", "bt2020",
                       "-color_trc", transfer, "-colorspace", "bt2020nc", "-color_range", "tv",
                       "-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", str(ROOT / name)]
            subprocess.run(command, check=True)
        fixture = ROOT / name
        manifest["fixtures"].append({"name": name, "transfer": transfer,
                                    "sha256": hashlib.sha256(fixture.read_bytes()).hexdigest(), "bytes": fixture.stat().st_size,
                                    "luma10": [color[0] for color in colors], "yuv10": colors,
                                    "generation_command": ["signal.yuv" if item == str(input_file) else name if item == str(fixture) else item
                                                           for item in command]})
    (ROOT / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")


if __name__ == "__main__":
    main()
