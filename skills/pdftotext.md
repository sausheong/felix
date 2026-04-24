---
name: pdftotext
description: Extract plain text from PDF files using pdftotext (Poppler). Useful for reading, searching, or piping PDF content into other tools.
tags: [pdftotext, pdf, text, extract, poppler, convert, ocr-not, document]
---

# pdftotext

## Purpose

Use `pdftotext` (from the Poppler suite) to extract plain text from PDF files. This skill is for the common case of getting readable text out of a PDF so it can be summarized, searched, indexed, or fed to another tool.

## When to use this skill

Use pdftotext when you need to:

- Read the contents of a PDF that the user references
- Extract a specific page range from a PDF
- Pipe PDF text into grep, wc, summarization, or another text tool
- Preserve approximate page layout (columns, tables) for downstream parsing
- Quickly check what a PDF contains before deciding next steps

Do not use pdftotext when:

- The PDF is a scanned image with no embedded text layer (use OCR instead, e.g. `tesseract`)
- You need to extract images, tables-as-data, or form-field values (use a PDF library)
- You need to preserve fonts, styling, or pixel-perfect layout (use Pandoc + HTML, or render with another tool)
- You need to modify or rewrite the PDF (pdftotext is read-only)

## Environment notes

`pdftotext` is part of the Poppler utilities and ships alongside `pdfinfo`, `pdfimages`, `pdftoppm`, etc. Path varies by platform — common locations are `/usr/bin/pdftotext`, `/usr/local/bin/pdftotext`, and `/opt/homebrew/bin/pdftotext`.

### Check if installed

```bash
command -v pdftotext && pdftotext -v 2>&1 | head -1
```

### Install if missing

If `pdftotext` is not found, install the Poppler utilities before running any other command in this skill:

- **macOS (Homebrew)**: `brew install poppler`
- **Linux (Debian/Ubuntu)**: `sudo apt-get update && sudo apt-get install -y poppler-utils`
- **Linux (Fedora/RHEL)**: `sudo dnf install -y poppler-utils`
- **Linux (Arch)**: `sudo pacman -S --noconfirm poppler`
- **Windows (winget)**: `winget install --id oschwartz10612.Poppler -e` (or download from https://github.com/oschwartz10612/poppler-windows/releases)
- **Windows (scoop)**: `scoop install poppler`

After installing, re-check with `pdftotext -v` before continuing. If installation fails, ask the user how they'd like to proceed.

**Always quote PDF paths in bash** — many user PDFs live under `~/Downloads` or `~/Documents` and contain spaces (e.g. `"My Report.pdf"`).

## Core principles

1. Inspect first with `pdfinfo` if you don't know the page count or whether the PDF has a text layer.
2. Default to `-layout` mode when the PDF has columns or tables; default to plain mode for prose.
3. Write extracted text to a new `.txt` file; never overwrite the source PDF.
4. If the output is empty or near-empty, the PDF is probably image-only — say so and suggest OCR.
5. Use `-f` and `-l` to extract specific pages rather than dumping a 500-page book.

## Basic command patterns

### Detect installed version

```bash
pdftotext -v
```

### Extract all text to a file

```bash
pdftotext "input.pdf" "output.txt"
```

### Extract to stdout (for piping)

```bash
pdftotext "input.pdf" -
```

### Preserve layout (columns, tables)

```bash
pdftotext -layout "input.pdf" "output.txt"
```

### Extract specific pages

```bash
# Pages 5 through 10
pdftotext -f 5 -l 10 "input.pdf" "output.txt"

# Just page 1
pdftotext -f 1 -l 1 "input.pdf" "output.txt"
```

### Inspect a PDF before extracting

```bash
pdfinfo "input.pdf"
```

This prints title, author, page count, page size, encryption status, etc. Use it to plan the extraction.

## Common useful options

| Option | Purpose |
|--------|---------|
| `-layout` | Maintain original physical layout (columns, tables) |
| `-raw` | Keep text in content-stream order (rarely useful) |
| `-table` | Optimize for table extraction (newer Poppler) |
| `-f N` | First page to extract |
| `-l N` | Last page to extract |
| `-enc UTF-8` | Force output encoding (default is usually fine) |
| `-nopgbrk` | Omit form-feed page breaks between pages |
| `-eol unix\|dos\|mac` | Choose line-ending style |

### Drop page-break form-feeds

```bash
pdftotext -nopgbrk "input.pdf" "output.txt"
```

### Force UTF-8 output

```bash
pdftotext -enc UTF-8 "input.pdf" "output.txt"
```

## Workflow

1. Verify the input file exists and is a PDF.
2. Optionally run `pdfinfo` to check the page count and whether text is present.
3. Pick `-layout` for columns/tables, plain for prose.
4. Restrict to a page range with `-f`/`-l` if the PDF is large and you only need part.
5. Write to a new `.txt` file (or pipe to stdout for chaining).
6. Read the first lines of the output to confirm the extraction worked.
7. If output is empty, fall back to OCR (`tesseract`) or report that the PDF is image-only.

## Examples

### Read a research paper for summarization

```bash
pdftotext "paper.pdf" - | head -200
```

### Extract a chapter from a book

```bash
pdftotext -f 45 -l 78 "book.pdf" "chapter3.txt"
```

### Extract a table-heavy financial report

```bash
pdftotext -layout "report.pdf" "report.txt"
```

### Quick word count of a PDF

```bash
pdftotext "input.pdf" - | wc -w
```

### Search for a term across a PDF

```bash
pdftotext "input.pdf" - | grep -i -n "deadline"
```

### Pipe into another skill (e.g. Pandoc to clean up Markdown)

```bash
pdftotext "input.pdf" - | pandoc -f markdown -t markdown -o cleaned.md
```

## Troubleshooting

- **Output is empty**: The PDF likely has no text layer (scanned images). Use OCR — for example `tesseract`, or render pages to PNG first with `pdftoppm` and then OCR each page.
- **Text is jumbled or out of order**: Try `-layout` mode. PDFs with columns often render in reading order only with `-layout`.
- **Tables come out as runs of spaces**: This is expected. Use `-layout`, then post-process with `awk` or a script. For real table extraction use a library (Camelot, Tabula).
- **Encrypted / password-protected**: Pass `-upw <password>` for a user password or `-opw <password>` for the owner password.
- **Garbled non-Latin text**: Force `-enc UTF-8` and verify the PDF embeds Unicode mappings; if it uses custom-encoded fonts there is no clean fix without OCR.
- **File path has spaces and command fails**: Always wrap the path in double quotes — `pdftotext "/Users/me/Documents/My File.pdf" -`.
