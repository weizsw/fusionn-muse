# Media Resolution Design

## Goal

Fusionn-Muse should process torrents whose real media is present but does not use the current hyphenated filename code pattern. It should also prepare ordered multipart videos and playable disc-image downloads into one normal media file before the existing subtitle pipeline runs.

Success criteria:

- A folder containing `ssni00083hhb.mp4` and a folder or torrent name containing `SSNI-083` is accepted and queued as `SSNI-083.mp4`.
- Ordered split videos such as `pppd176A.FHD.wmv`, `pppd176B.FHD.wmv`, `pppd176C.FHD.wmv` are assembled in order into one normalized media file.
- Ordered split videos such as `soe00967hhb1.wmv`, `soe00967hhb2.wmv` are assembled in numeric order into one normalized media file.
- Playable `.iso` and `.nrg` downloads are extracted without privileged mounts, resolved to their main media content, and prepared as one normalized `.mkv` when possible.
- Normal single video files continue through the current hardlink/copy and processing path without unnecessary conversion.

## Scope

In scope:

- Code extraction from hyphenated names (`SSNI-083`) and compact names (`ssni00083hhb`).
- Code fallback priority: video filename, parent folder name, torrent name.
- Folder media resolution before job creation.
- Multipart video detection and assembly.
- `.iso` and `.nrg` extraction plus media selection.
- Lossless remux/concat first, transcode only as a fallback.

Out of scope:

- Carrying multiple source files through the queue and processor.
- Mounting disc images inside Docker.
- Full DVD menu handling.
- User-configurable matching rules.
- Re-encoding normal single video files.

## Architecture

Add a media resolver layer between `handler.TorrentComplete` and job creation.

The resolver returns a resolved media input with:

- source paths: one or more original files
- display code: normalized code such as `SSNI-083`
- output filename: normalized media name such as `SSNI-083.mp4` or `SSNI-083.mkv`
- preparation mode: direct, multipart concat, or image extraction

The existing queue and processor should continue to receive a single `SourcePath` and `FileName`. If preparation produces a new file, the job source path points at that prepared file. This avoids broad changes to queue state, retry behavior, subtitle naming, and final scraping movement.

## Code Detection

Use one code parser for all detection sources.

Accepted patterns:

- Hyphenated: `([A-Z]{2,5})-(\d{3,5})`
- Compact: `([A-Z]{2,5})0*(\d{3,5})` followed by optional release noise such as letters or digits

Compact matches must reject known technical tokens as prefixes, such as `HD`, `FHD`, `UHD`, `SD`, `AVC`, `HEVC`, `XVID`, `X264`, and `X265`. This prevents names containing tokens like `FHD1080` from being treated as media codes.

Normalization:

- Uppercase the prefix.
- Preserve the numeric value with three digits minimum.
- Remove extra zero padding beyond three digits.

Examples:

- `SSNI-083` -> `SSNI-083`
- `ssni00083hhb.mp4` -> `SSNI-083`
- `pppd176A.FHD.wmv` -> `PPPD-176`
- `soe00967hhb1.wmv` -> `SOE-967`

Fallback priority:

1. matched video filename
2. matched parent folder name
3. matched torrent name

When multiple sources disagree, prefer the earlier priority and log the chosen source.

## Source Selection

For a direct file path:

- If it is a supported video, resolve it directly.
- If it is `.iso` or `.nrg`, resolve it through image extraction.
- Otherwise reject it.

For a folder path:

1. Walk recursively for supported video files and supported image files.
2. Ignore files at or below the existing minimum video size unless they are image files.
3. If one video has a code, choose it.
4. If multiple videos with the same code form an ordered set, choose the ordered set.
5. If no video filename has a code, look for code in the parent folder, then torrent name.
6. With a fallback code, choose an ordered video set if present; otherwise choose the largest video.
7. If there is an image file and no better normal video candidate, extract and resolve the image.

## Multipart Assembly

Detect ordered sets only when files share the same inferred base code and extension family.

Supported order markers:

- trailing letter markers: `A`, `B`, `C`
- trailing numeric markers: `1`, `2`, `3`
- common part markers: `part1`, `cd1`, `disc1`

Assembly behavior:

- Sort by detected marker.
- Require at least two parts and no duplicate order marker.
- Prefer `ffmpeg -f concat -safe 0 -i parts.txt -c copy output.mkv`.
- If copy concat fails, retry with a conservative transcode to MP4.

The prepared output should be named from the normalized code, not the first part name.

## ISO and NRG Preparation

Disc images are handled without loop mounts.

Extraction strategy:

- `.iso`: extract with `bsdtar` or `7z`.
- `.nrg`: extract with `7z` when possible; if needed, convert to ISO with `nrg2iso`, then extract.

Docker runtime dependencies should include the minimum tools needed for this path. Prefer tools available through Debian packages in the current Python slim image.

After extraction:

- DVD layout: find `VIDEO_TS/VTS_*_*.VOB`, group by title set, choose the largest title chain, assemble ordered VOB parts.
- Blu-ray layout: find `BDMV/STREAM/*.m2ts`, choose the largest stream first.
- Plain extracted media: run normal source selection over the extracted folder.

Prepared output:

- Prefer `.mkv` with stream copy because it handles DVD/Blu-ray codecs better than MP4.
- Fall back to MP4 transcode only if remux or concat fails.

## Error Handling

Resolver errors should be explicit and logged:

- no media candidates found
- no code found in filename, folder, or torrent name
- ambiguous multipart ordering
- image extraction failed
- media preparation failed

If resolution fails for a torrent folder, return the current successful webhook response with no queued jobs, matching existing "no valid video files found" behavior.

If preparation fails after a job has started, move prepared/intermediate outputs to failed only when they are in managed data folders. Do not move or delete original torrent files.

## Testing

Add focused Go tests for:

- hyphenated and compact code extraction
- fallback priority between file, folder, and torrent name
- single fallback video selection
- multipart A/B/C ordering
- multipart 1/2/3 ordering
- ambiguity rejection for duplicate or missing part markers
- direct `.wmv` support remains accepted

Add integration-style tests where practical using temporary directories and small dummy files for resolver selection. FFmpeg/image extraction tests can use command seams or small fixture files if available; otherwise test command construction and keep heavy media fixtures out of the repo.

## Decisions

- Use `.mkv` as the preferred prepared output for multipart and image-based sources.
- Keep normal single video files in their original extension.
- Add `.iso` and `.nrg` as resolvable media sources, but not as normal video extensions.
