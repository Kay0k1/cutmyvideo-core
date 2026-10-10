#!/usr/bin/env python3
"""Require an actual bounded zscale/tonemap conversion before native CI tests."""
import subprocess


def main():
    # One tiny generated PQ frame exercises both filters and the linked zimg
    # conversion path. Filter-name listings alone cannot establish usability.
    source = ("color=black:s=16x16:r=1,format=yuv420p10le,"
              "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc:range=limited")
    filters = ("zscale=t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,"
               "setparams=colorspace=bt709,tonemap=mobius:param=0.3:desat=2:peak=100,"
               "zscale=t=bt709:m=bt709:matrixin=gbr:r=limited,format=yuv420p")
    try:
        result = subprocess.run(
            ["ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-max_alloc", "268435456",
             "-filter_threads", "1", "-threads", "1", "-f", "lavfi", "-i", source,
             "-frames:v", "1", "-vf", filters, "-f", "null", "-"],
            stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
            timeout=15, check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise SystemExit("Required HDR conversion preflight failed: " + str(error))
    if result.returncode:
        detail = result.stderr[:4096].decode("utf-8", errors="replace").strip()
        raise SystemExit("Required HDR conversion preflight failed; install FFmpeg with zscale/libzimg and tonemap.\n" + detail)
    print("Required zscale/libzimg and tonemap conversion passed.")


if __name__ == "__main__":
    main()
