# Input attribution and exact derivative recipe

**Pycnonotus goiavier**, Joaquim Baeta (Spontaneous Symmetry), [CC BY4.0](https://creativecommons.org/licenses/by/4.0/). [File page](https://commons.wikimedia.org/wiki/File:Pycnonotus_goiavier.webm); [reviewed revision 1030336221](https://commons.wikimedia.org/w/index.php?title=File:Pycnonotus_goiavier.webm&oldid=1030336221); [original file](https://upload.wikimedia.org/wikipedia/commons/2/20/Pycnonotus_goiavier.webm).

Original 21,473,668 B, SHA256 `c2ba89f5bfa1a7c69c78b04eff8de0c6fce3736e335499f76ee7dbe2006ea7b8`. Derivative 18,495,443 B, SHA256 `dbcce2e3a87a05e104be167e3075c7c98cd23da867f8b858ac3edb724580364d`.

Changes: first 20 seconds; labeled SMPTE170m converted to BT709, CFR30/H264; original sound replaced by pulsed 440 Hz left / 880 Hz right stereo tones; 256×64 RGB witness strip added without upscaling. Code/report licensing does not replace the footage’s CC BY attribution obligation.

Actual FFmpeg argv with only paths normalized (a reproduction recipe, not an additional generation performed for this report):

```sh
/absolute/tools/ffmpeg -hide_banner -v error -nostdin -n -threads 2 -filter_complex_threads 2 -i /absolute/original/Pycnonotus_goiavier.webm -f lavfi -i 'aevalsrc=exprs='"'"'.2*sin(2*PI*440*t)*if(between(mod(t,1),.2,.7),1,.05)|.2*sin(2*PI*880*t)*if(between(mod(t,1),.2,.7),1,.05)'"'"':s=48000:d=20' -filter_complex '[0:v]colorspace=all=bt709:iall=smpte170m,fps=30,format=gbrp,geq=r='"'"'if(lt(Y,64)*lt(X,256),if(lt(X,64),240,if(lt(X,128),20,if(lt(X,192),20,240))),r(X,Y))'"'"':g='"'"'if(lt(Y,64)*lt(X,256),if(lt(X,64),20,if(lt(X,128),240,if(lt(X,192),20,240))),g(X,Y))'"'"':b='"'"'if(lt(Y,64)*lt(X,256),if(lt(X,64),20,if(lt(X,128),20,if(lt(X,192),240,240))),b(X,Y))'"'"',scale=in_range=full:out_range=limited:out_color_matrix=bt709,format=yuv420p[video]' -map '[video]' -map 1:a:0 -c:v libx264 -preset fast -crf 18 -threads:v 2 -pix_fmt yuv420p -r 30 -x264-params keyint=30:min-keyint=30:scenecut=0:colorprim=bt709:transfer=bt709:colormatrix=bt709:fullrange=off -t 20 -c:a aac -b:a 192k -ac 2 -ar 48000 -color_primaries bt709 -color_trc bt709 -colorspace bt709 -color_range tv -movflags +faststart /absolute/fixtures/main.mp4
```

Original actual argv receipt SHA256 `c516c78afb08b6a55ad852b90357952d0fa8d1b8ccbb7c757460e46c9e6a6731`; independent fixture-verification script SHA256 `54e6f6bebe274ee75adc37334c656ceefd820e2e5848017f0b3f18f8df9fe21d`; complete 20-second audio-span receipt SHA256 `ea569097946623aa50a2f8cdd3fc00cf98ab4de75e1bfdb4c2bfbfd6ad15f9f5`. Tool and input hashes are in INPUTS. Complete input proof covers all 20 per-second stereo pulse windows.

This benchmark input dataset redistributes the exact derivative fixture, full decoded tone PCM and retained input/generation proofs with this attribution. The original WebM is linked above and is not included. The proof tools are historical generation/verification identities; no FFmpeg executable or runtime image is redistributed. No regeneration was performed for this dataset.
