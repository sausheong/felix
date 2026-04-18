---
name: ffmpeg
description: Process, convert, compress, trim, and manipulate audio and video files using FFmpeg.
tags: [ffmpeg, video, audio, convert, compress, trim, media, mp4, mp3, wav, thumbnail]
---

# FFmpeg

## Purpose

Use FFmpeg to process, convert, compress, trim, and manipulate audio and video files. This skill is useful for format conversion, resizing, bitrate control, extracting streams, and basic media transformations.

## When to use this skill

Use FFmpeg when you need to:

- Convert between media formats (e.g. MP4, MKV, MP3, WAV)
- Compress or reduce file size
- Resize or scale video resolution
- Trim or cut segments of audio or video
- Extract audio from video
- Merge or concatenate media files
- Change codecs, bitrates, or frame rates
- Capture frames or generate thumbnails
- Inspect media metadata

Do not use FFmpeg when:

- You need complex video editing with timelines, transitions, or effects
- You require a GUI-based editing workflow
- The task involves high-level creative editing rather than transformation

## Environment notes

FFmpeg and ffprobe are pre-installed and available at `/usr/bin/ffmpeg` and `/usr/bin/ffprobe`.

## Core principles

1. Prefer copying streams (`-c copy`) when no re-encoding is needed.
2. Re-encode only when format, codec, or size must change.
3. Use explicit codecs for predictable output.
4. Control quality via CRF (video) and bitrate (audio).
5. Avoid unnecessary recompression to preserve quality.
6. Always write output to a new file.
7. Inspect inputs before processing when uncertain.

## Basic command patterns

### Check FFmpeg installation

```bash
ffmpeg -version
```

### Inspect media file

```bash
ffmpeg -i input.mp4
```

For more structured output:

```bash
ffprobe input.mp4
```

### Convert video format

```bash
ffmpeg -i input.mkv -c:v libx264 -c:a aac output.mp4
```

### Convert audio format

```bash
ffmpeg -i input.wav -c:a mp3 output.mp3
```

### Extract audio from video

```bash
ffmpeg -i input.mp4 -vn -c:a copy output.aac
```

Or re-encode:

```bash
ffmpeg -i input.mp4 -vn -c:a mp3 output.mp3
```

### Copy streams without re-encoding

```bash
ffmpeg -i input.mp4 -c copy output.mkv
```

### Trim video (without re-encoding)

```bash
ffmpeg -ss 00:00:10 -to 00:00:30 -i input.mp4 -c copy output.mp4
```

### Trim video (accurate, with re-encoding)

```bash
ffmpeg -i input.mp4 -ss 00:00:10 -to 00:00:30 -c:v libx264 -c:a aac output.mp4
```

### Resize video

```bash
ffmpeg -i input.mp4 -vf scale=1280:720 -c:v libx264 -c:a aac output.mp4
```

### Reduce file size using CRF

```bash
ffmpeg -i input.mp4 -c:v libx264 -crf 28 -preset medium -c:a aac output.mp4
```

Lower CRF means higher quality:
- 18 to 23: high quality
- 24 to 30: smaller size

### Change frame rate

```bash
ffmpeg -i input.mp4 -r 30 output.mp4
```

### Create thumbnail

```bash
ffmpeg -i input.mp4 -ss 00:00:05 -vframes 1 thumbnail.jpg
```

### Extract frames

```bash
ffmpeg -i input.mp4 frame_%04d.png
```

### Concatenate files (same format)

Create a file list:

```
file 'part1.mp4'
file 'part2.mp4'
```

Then:

```bash
ffmpeg -f concat -safe 0 -i list.txt -c copy output.mp4
```

## Common useful options

### Video codecs

- `libx264` for H.264
- `libx265` for H.265 (smaller size, slower)
- `copy` to avoid re-encoding

### Audio codecs

- `aac` for most video containers
- `mp3` for compatibility
- `copy` to avoid re-encoding

### Presets

Control encoding speed versus compression:

```
-preset ultrafast | superfast | veryfast | faster | fast | medium | slow | slower | veryslow
```

Slower presets give better compression at the cost of time.

### Bitrate control

```
-b:v 1000k
-b:a 128k
```

### Overwrite output without prompt

```
-y
```

### Suppress logs

```
-loglevel error
```

## Workflow

1. Inspect input file with `ffmpeg -i` or `ffprobe`.
2. Determine if re-encoding is required.
3. Choose appropriate codecs and quality settings.
4. Write output to a new file.
5. Run command.
6. Verify output exists and matches expectations.
7. Iterate with adjusted CRF, bitrate, or scaling if needed.

## Examples

### Compress video for sharing

```bash
ffmpeg -i input.mp4 -c:v libx264 -crf 26 -preset fast -c:a aac output.mp4
```

### Extract audio for podcast

```bash
ffmpeg -i input.mp4 -vn -c:a mp3 -b:a 192k output.mp3
```

### Resize for mobile

```bash
ffmpeg -i input.mp4 -vf scale=720:-2 -c:v libx264 -crf 23 -c:a aac output.mp4
```

### Quick trim without quality loss

```bash
ffmpeg -ss 00:01:00 -to 00:02:00 -i input.mp4 -c copy clip.mp4
```

## Troubleshooting

- **Output file too large**: Increase CRF (e.g. 28 to 32), use libx265, or reduce resolution.
- **Poor quality output**: Lower CRF (e.g. 18 to 23), use slower preset, avoid repeated re-encoding.
- **Audio out of sync**: Re-encode instead of copying streams.
- **Unsupported format**: Check available codecs with `ffmpeg -codecs`. Convert to H.264 + AAC.
- **Trim is inaccurate**: Place `-ss` after `-i` for accuracy (requires re-encoding).

## Quick reference

- Need format conversion or compression: use FFmpeg
- Need fast trim without quality loss: use `-c copy`
- Need smaller file: increase CRF or use H.265
- Need compatibility: use H.264 + AAC in MP4
- Need editing with effects: use a video editor instead
