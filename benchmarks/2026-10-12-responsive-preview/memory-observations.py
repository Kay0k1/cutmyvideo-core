import hashlib, json, pathlib, resource, subprocess, sys, time
source = pathlib.Path(sys.argv[1])
output = pathlib.Path(sys.argv[2])
mode = sys.argv[3]
filter_chain = "scale=852:480,setsar=1,fps=24" if mode == "baseline" else "fps=24,scale=852:480,setsar=1"
args = ["/usr/local/bin/ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-filter_threads", "1", "-protocol_whitelist", "file", "-format_whitelist", "mov,matroska,webm,mp3,wav,flac,ogg,aac,avi,mpegts", "-threads", "2", "-max_pixels", "33554432", "-max_streams", "32", "-ss", "0.000", "-i", str(source), "-t", "30.000", "-map", "0:0", "-map", "0:1", "-vf", filter_chain, "-c:v", "libx264", "-preset", "ultrafast", "-crf", "28", "-maxrate", "1400k", "-bufsize", "2800k", "-pix_fmt", "yuv420p", "-threads", "2", "-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", "-map_metadata", "-1", "-map_chapters", "-1", "-fs", "16777216", str(output)]
started=time.perf_counter()
subprocess.run(args, check=True)
usage=resource.getrusage(resource.RUSAGE_CHILDREN)
print(json.dumps({"mode":mode,"wall_ms":(time.perf_counter()-started)*1000,"peak_rss_kib":usage.ru_maxrss,"sha256":hashlib.sha256(output.read_bytes()).hexdigest(),"filter":filter_chain,"args":args}))
output.unlink()
