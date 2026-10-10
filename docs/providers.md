# Recorded video providers

The catalogue contains 15 recognized platforms. Recognition selects a page adapter and normalizes a pasted link; **it does not mean every video is accessible**. Import verifies a single finite recording, real provider identity/duration, available media formats and network limits. Unknown public HTTPS pages may still use the guarded generic yt-dlp extractor. Direct file URLs remain supported separately.

The runtime pins [yt-dlp 2026.08.19](https://github.com/yt-dlp/yt-dlp/releases/tag/2026.08.19). Its [extractor list](https://github.com/yt-dlp/yt-dlp/blob/2026.08.19/supportedsites.md) is a discovery reference, not a successful-import claim. Closed accounts, memberships, passwords, DRM, regional restrictions, anti-bot challenges and expired links can prevent import. No account cookies or authentication-bypass configuration is offered.

Extractor JSON responses are bounded to 8 MiB. The fixed `pre_process` metadata step clears unused subtitle and automatic-caption URL matrices before `--dump-single-json`; videos with many translated caption variants can otherwise exceed that limit. Recording identity, duration, all stream formats/headers and playlist or multi-video envelopes remain intact. The same inspection runs during import and worker processing. Offline regressions use the pinned real extractor with generated oversized fixtures and no platform requests.

## Catalogue and accepted page forms

Ordinary HTTP and schemeless pastes on recognized hosts are upgraded to HTTPS. URL credentials and nonstandard ports are refused. Genuine hostnames are explicitly recognized; a name containing a platform domain is not sufficient. Unsupported channels/collections are rejected before extraction. Tracking/player offsets are removed; access-essential page parameters are preserved. Exports use their separately supplied time ranges.

| Provider slug | Page forms and aliases | Validation evidence |
|---|---|---|
| `youtube` | `watch?v=ID`, `youtu.be/ID`, `/shorts/ID`, recorded `/live/ID`, `/embed/ID`; mobile/music/nocookie | Real metadata and MP4/MP3 exports; normalized variants tested |
| `twitch` | `/videos/ID`, older channel `/v/ID` or `/video/ID`, player `?video=ID`; `clips.twitch.tv/SLUG`, channel `/clip/SLUG`, clips embed `?clip=SLUG`; www/m/go | Real VOD selective HLS export and progressive clip export |
| `rutube` | `/video/ID`, `/shorts/ID`, `/embed/ID`, `/play/embed/ID`, recorded `/live/video/ID`; numeric embeds | Real selective HLS export; Shorts normalize to the same 32-character video ID |
| `tiktok` | `/@user/video/ID`, `/v/ID.html`, `/t/CODE`, vm/vt short links | Extractor/URL validation; tested public sample was unavailable in the test environment |
| `instagram` | `/reel/ID`, `/reels/ID`, `/p/ID`, `/tv/ID`, optional username; `/share/reel/ID` redirects | Extractor/URL validation only; shared redirects depend on platform response |
| `vimeo` | `/ID`, `/ID/HASH`, player `/video/ID`, channel/group single-video links | Extractor/URL validation; tested public sample was unavailable in the test environment |
| `dailymotion` | `/video/ID`, `/embed/video/ID`, `dai.ly/ID` | Extractor/URL validation only |
| `vk` | `/videoOWNER_ID`, `/clipOWNER_ID`, `video_ext.php?oid=…&id=…`, `?z=video…`; vk.com/vkvideo.ru | Extractor/URL validation only |
| `facebook` | `/user/videos/ID`, `/watch/?v=ID`, `/video.php?v=ID`, `/reel/ID`, `/share/v/CODE`, `/share/r/CODE`, fb.watch | Extractor/URL validation only |
| `x` | `/user/status/ID`, `/i/status/ID`; x.com/twitter.com | Extractor/URL validation only |
| `reddit` | `/r/sub/comments/ID/title`, redd.it/v.redd.it links; www/old/np | Extractor/URL validation only; separate HLS/DASH renditions may prevent MP4 |
| `ok` | `/video/ID`, `/videoembed/ID`; ok.ru | Extractor/URL validation only |
| `bilibili` | `/video/BVID`, `/video/avID`, b23.tv short links | Extractor/URL validation only; multi-part collections are refused |
| `streamable` | `/ID`, `/e/ID`, `/s/ID/HASH` | Real metadata inspection of a public progressive video |
| `rumble` | `/vID-title.html`, `/embed/vID` | Extractor/URL validation only |

YouTube canonicalization accepts only a valid11-character video ID and discards playlist/time/tracking parameters. For unknown direct HTTPS URLs the raw query is preserved, including its original ordering and signature. Unlisted page access hashes such as Rutube `p`, Vimeo `h` and VK `access_key`/`list` are preserved; this does not grant access to a password or account-restricted video.

## Recordings, clips and live broadcasts

Twitch channel URLs and player `?channel=…` URLs return `live_not_supported`; use a VOD or clip link. Metadata marked live/upcoming/in-progress is rejected even if it includes an elapsed duration. HLS media requires an finite completed `EXT-X-ENDLIST` playlist; its measured complete duration becomes the source timeline. VOD manifests still growing during a broadcast are refused.

Supported media paths:

- Progressive public HTTPS MP4/WebM, or compatible separate progressive video/audio streams, through guarded range reads.
- Finite unencrypted combined HLS video/audio, or audio-only HLS for MP3. Selected segments and a preceding decoder-context segment are staged. MPEG-TS and fMP4 init maps are inspected/remuxed locally. The full long recording is not downloaded as a fallback.
- Complete uploaded/direct local media within source and processing limits.

The HLS implementation bounds manifest size 2 MiB, nesting depth 2, variants 100, segments 100000, each segment duration 300 seconds and total source duration 30 days. Manifest duration must match provider metadata within 1 second. Every redirect, variant, init map and selected segment uses the public-IP-pinned HTTPS client. A shared transfer budget covers the whole export job, including its several ranges. The worker reserves staging/output storage and removes temporary media after every fragment.

Encrypted keys, HLS byte ranges, low-latency parts, gap segments, changing init maps or discontinuities inside a selected fragment, and separate HLS video/audio renditions are explicitly unsupported. Separate rendition clocks cannot be safely assumed identical; the engine refuses to silently discard original A/V delay. A master requiring an external audio rendition is also refused. DASH fragments are not supported. FFmpeg never opens these remote playlists; it only reads guarded progressive relay URLs or trusted staged media bytes.

## Preview contract

Uploaded/direct sources use `preview_kind:native` and an owner-protected original-file endpoint. YouTube uses `preview_kind:youtube` with a validated embed ID. Other platform sources use `preview_kind:window`: clients request `/sources/{id}/preview?start_ms=…` for a bounded 30-second H.264/AAC MP4 interval. HLS requests download only the intersecting segments plus decoder context. The same endpoint provides a fallback for unavailable YouTube embeds or staged codecs unsupported by the browser. Upstream access restrictions still apply. Positions are aligned to 30-second windows. Completed MP4 windows are reused from a private, owner-checked cache (512 MiB / 64 entries globally, 128 MiB / 16 entries per owner; 30-minute idle expiry). Only the final MP4 is retained and its actual size remains charged to the storage ledger. Source deletion removes its cached windows. Repeated concurrent requests for the same window share one render; at most two windows render globally, and optional `priority=background` prefetch uses only an otherwise idle renderer. Staged inputs are deleted before publication; abandoned requests expire through maintenance.

`thumbnail_url` is a same-origin owner-protected endpoint. The server stages at most 2 MiB of a public JPEG/PNG and checks its dimensions; unsupported/unavailable images become null. Signed CDN image/media URLs are never returned as visible preview links. Media and thumbnails require the session cookie and expire under the source retention policy.

## Reproducible evidence

On 2026-10-03, a guarded metadata/media diagnostic in the development environment verified:

| Public recording | Selected original interval | Observed result |
|---|---|---|
| [Rutube sample](https://rutube.ru/video/3eac3b4561676c17df9132a9a1e62e3e/) | 40–43 s | 3.000 s MP4; 3 of 21 media segments; about 1.2 MB transferred |
| [Twitch VOD](https://www.twitch.tv/videos/2833641956) | 10000–10003 s | 3.003 s MP4 from a recording lasting 32268 seconds;3 of3224 segments; about 11.8 MB transferred |
| [Twitch clip](https://clips.twitch.tv/FaintLightGullWholeWheat) | 10–13 s | 3.003 s 720p MP4; progressive source with unspecified codec metadata |
| [Streamable sample](https://streamable.com/moo) | Metadata only | Actual 12 s duration and a progressive MP4 format |

These observations are specific links, timestamps and an environment, not availability guarantees. A Vimeo sample and a TikTok sample returned `platform_unavailable`; that observation does not prove an authentication requirement. External smoke checks are opt-in and kept outside repository tests. CI uses synthetic media, PostgreSQL and deterministic security/timeline tests; it does not depend on public videos remaining online.
