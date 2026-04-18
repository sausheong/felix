---
name: imagemagick
description: Inspect, convert, resize, crop, compress, and transform image files using ImageMagick.
tags: [imagemagick, image, convert, resize, crop, compress, thumbnail, png, jpg, webp]
---

# ImageMagick

## Purpose

Use ImageMagick to inspect, convert, resize, crop, compress, and transform image files. This skill is useful for format conversion, dimension changes, optimization, annotations, compositing, and batch image processing.

## When to use this skill

Use ImageMagick when you need to:

- Convert between image formats such as PNG, JPG, WEBP, TIFF, and PDF
- Resize, crop, rotate, or flip images
- Compress or optimize images for file size
- Change quality, color depth, or metadata
- Add borders, text, watermarks, or simple overlays
- Create thumbnails
- Combine or split images
- Inspect image dimensions, format, and properties
- Perform batch operations over many files

Do not use ImageMagick when:

- You need complex layer-based editing like Photoshop
- You require precise manual retouching
- The task is better handled by a dedicated vector editor
- The task is really document editing rather than image manipulation

## Environment notes

ImageMagick is pre-installed and available as `magick` at `/usr/bin/magick`. The `identify` and `montage` subcommands are also available. PDF rasterization may be restricted by the local ImageMagick security policy.

## Core principles

1. Inspect the image before transforming it.
2. Always write output to a new file unless in-place overwrite is explicitly intended.
3. Prefer explicit dimensions, formats, and quality settings.
4. Preserve aspect ratio unless the task specifically requires distortion.
5. Avoid repeated lossy recompression.
6. For modern ImageMagick, prefer `magick` over older separate commands like `convert`.
7. Batch carefully and test on one file first.

## Basic command patterns

### Check installation

```bash
magick -version
```

### Inspect image metadata

```bash
magick identify input.jpg
```

More detailed:

```bash
magick identify -verbose input.jpg
```

### Convert image format

```bash
magick input.png output.jpg
```

### Create a copy in another format

```bash
magick input.webp output.png
```

## Common operations

### Resize image while preserving aspect ratio

```bash
magick input.jpg -resize 1200x1200 output.jpg
```

This fits the image within the box.

### Resize to exact width

```bash
magick input.jpg -resize 1200x output.jpg
```

### Resize to exact height

```bash
magick input.jpg -resize x800 output.jpg
```

### Force exact size

```bash
magick input.jpg -resize 1200x800! output.jpg
```

Use only when distortion is acceptable.

### Crop image

```bash
magick input.jpg -crop 800x600+100+50 output.jpg
```

### Auto-orient based on EXIF

```bash
magick input.jpg -auto-orient output.jpg
```

### Rotate image

```bash
magick input.jpg -rotate 90 output.jpg
```

### Flip or flop image

```bash
magick input.jpg -flip output.jpg
magick input.jpg -flop output.jpg
```

### Create thumbnail

```bash
magick input.jpg -thumbnail 300x300 output.jpg
```

### Compress JPEG

```bash
magick input.jpg -quality 82 output.jpg
```

### Convert to WEBP

```bash
magick input.jpg -quality 80 output.webp
```

### Strip metadata

```bash
magick input.jpg -strip output.jpg
```

### Add border

```bash
magick input.jpg -bordercolor white -border 20 output.jpg
```

### Add text annotation

```bash
magick input.jpg -gravity south -pointsize 24 -annotate +0+20 "Sample Text" output.jpg
```

### Add watermark overlay

```bash
magick input.jpg watermark.png -gravity southeast -geometry +20+20 -composite output.jpg
```

### Convert to grayscale

```bash
magick input.jpg -colorspace Gray output.jpg
```

### Blur image

```bash
magick input.jpg -blur 0x4 output.jpg
```

### Sharpen image

```bash
magick input.jpg -sharpen 0x1 output.jpg
```

## Combining and splitting

### Combine images horizontally

```bash
magick img1.jpg img2.jpg +append output.jpg
```

### Combine images vertically

```bash
magick img1.jpg img2.jpg -append output.jpg
```

### Create contact sheet

```bash
magick montage *.jpg -thumbnail 200x200 -tile 4x -geometry +10+10 contact-sheet.jpg
```

### Extract a page from a PDF

```bash
magick input.pdf[0] output.png
```

### Split animated GIF into frames

```bash
magick input.gif frame_%03d.png
```

### Create animated GIF

```bash
magick -delay 10 -loop 0 frame_*.png output.gif
```

## Batch processing

### Resize all JPG files in a folder

```bash
mkdir -p resized
for f in *.jpg; do
  magick "$f" -resize 1600x1600 "resized/$f"
done
```

### Convert all PNG files to WEBP

```bash
mkdir -p webp
for f in *.png; do
  base="${f%.png}"
  magick "$f" -quality 80 "webp/$base.webp"
done
```

## Common useful options

### Quality

```
-quality 80
```

Common for JPG and WEBP output.

### Remove metadata

```
-strip
```

### Set background for formats that do not support transparency

```bash
magick input.png -background white -alpha remove -alpha off output.jpg
```

### Control output density for PDF or vector rendering

```bash
magick -density 300 input.pdf[0] output.png
```

Usually applied before the input when rasterizing.

### Set output format explicitly

```bash
magick input.png jpg:output.jpg
```

## Workflow

1. Inspect the input image with `identify`.
2. Decide whether the transformation is lossless or lossy.
3. Choose explicit output filename and format.
4. Test on one image first.
5. Run the transformation.
6. Verify output dimensions, quality, and file size.
7. Apply in batch only after confirming the result.

## Examples

### Resize and optimize for web

```bash
magick input.jpg -auto-orient -resize 1600x1600 -strip -quality 82 output.jpg
```

### Convert PNG to WEBP

```bash
magick input.png -quality 80 output.webp
```

### Crop to square avatar

```bash
magick input.jpg -gravity center -crop 800x800+0+0 +repage output.jpg
```

### Add simple watermark

```bash
magick input.jpg watermark.png -gravity southeast -geometry +24+24 -composite output.jpg
```

## Troubleshooting

- **ImageMagick not found**: Run `magick -version` to verify. ImageMagick should be pre-installed.
- **Command uses `convert` but fails**: Use `magick` instead. The `convert` command is deprecated in modern ImageMagick.
- **Output looks blurry**: Avoid repeated resizing. Use a larger target size. Check whether the source image is already small.
- **File size is too large**: Reduce `-quality`, strip metadata with `-strip`, convert to WEBP, or resize to smaller dimensions.
- **Transparent PNG turns black in JPG**: Set a background explicitly with `-background white -alpha remove -alpha off`.
- **PDF conversion blocked by security policy**: Some ImageMagick installs restrict PDF reading via policy settings. Use another tool for PDF rasterization if needed.

## Quick reference

- Need image conversion or transformation: use ImageMagick
- Need batch processing: use ImageMagick with shell loops
- Need simple overlays, crops, or thumbnails: use ImageMagick
- Need advanced manual editing: use an image editor instead
