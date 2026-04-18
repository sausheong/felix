---
name: pandoc
description: Convert documents between formats (Markdown, HTML, DOCX, EPUB, plain text) using Pandoc.
tags: [pandoc, convert, document, markdown, html, docx, epub, format, export, word]
---

# Pandoc

## Purpose

Use Pandoc to convert documents between common markup and document formats such as Markdown, HTML, DOCX, EPUB, and plain text. This skill is useful when you need reliable document transformation, metadata injection, table of contents generation, or format normalization.

## When to use this skill

Use Pandoc when you need to:

- Convert Markdown to HTML, DOCX, EPUB, or plain text
- Convert HTML, DOCX, or other supported inputs into Markdown
- Generate polished documents from structured text
- Apply a template, stylesheet, or reference document
- Add metadata such as title, author, and date
- Produce a table of contents or numbered sections
- Normalize document structure before further editing

Do not use Pandoc when:

- The task requires pixel-perfect desktop publishing
- The source document depends heavily on complex layout fidelity
- The document is mostly images, scanned pages, or OCR output
- Spreadsheet manipulation is required

## Environment notes

Pandoc is pre-installed and available at `/usr/bin/pandoc`. No LaTeX PDF engine is installed, so direct PDF generation (`-t pdf`) is not available. For PDF output, convert to HTML first and use other tools, or output to DOCX instead.

## Core principles

1. Prefer Markdown as the working intermediate format unless the user explicitly wants another format.
2. Preserve structure first, styling second.
3. Use explicit input and output formats when there is any ambiguity.
4. Keep source files unchanged. Write outputs to new files.
5. Validate the output file exists and is readable after conversion.

## Basic command patterns

### Detect installed version

```bash
pandoc --version
```

### Convert Markdown to HTML

```bash
pandoc input.md -f markdown -t html -s -o output.html
```

### Convert Markdown to DOCX

```bash
pandoc input.md -f markdown -t docx -o output.docx
```

### Convert DOCX to Markdown

```bash
pandoc input.docx -f docx -t markdown -o output.md
```

### Convert HTML to Markdown

```bash
pandoc input.html -f html -t markdown -o output.md
```

### Convert Markdown to EPUB

```bash
pandoc book.md -o book.epub
```

### Convert Markdown to plain text

```bash
pandoc input.md -f markdown -t plain -o output.txt
```

## Common useful options

### Metadata

```bash
pandoc input.md -o output.html \
  --metadata title="Document Title" \
  --metadata author="Author Name" \
  --metadata date="2026-03-24"
```

### Table of contents

```bash
pandoc input.md -o output.html -s --toc
```

### Numbered sections

```bash
pandoc input.md -o output.html -s --number-sections
```

### Standalone output

Use `-s` or `--standalone` when generating a full HTML document:

```bash
pandoc input.md -o output.html -s
```

### Reference DOCX for styling

```bash
pandoc input.md -o output.docx --reference-doc=reference.docx
```

### CSS for HTML

```bash
pandoc input.md -o output.html -s --css=styles.css
```

### Explicit formats

```bash
pandoc input.txt -f markdown -t gfm -o output.md
```

Useful format targets: `markdown`, `gfm`, `html`, `html5`, `docx`, `epub`, `plain`

## Handling resources

If the source document references local images or assets, ensure paths remain valid relative to the output location.

For extracting media from DOCX:

```bash
pandoc input.docx -t markdown --extract-media=media -o output.md
```

## Workflow

1. Inspect the input file type.
2. Choose explicit `-f` and `-t` formats where useful.
3. Write to a new output path.
4. Run conversion.
5. Check that the output file exists.
6. If fidelity is poor, convert first to Markdown as an intermediate and inspect structure.

## Examples

### Markdown report to DOCX

```bash
pandoc report.md -f markdown -t docx -o report.docx
```

### DOCX notes to clean Markdown

```bash
pandoc notes.docx -f docx -t markdown -o notes.md
```

### HTML article to Markdown

```bash
pandoc article.html -f html -t markdown -o article.md
```

### Markdown to standalone HTML with TOC

```bash
pandoc report.md -o report.html -s --toc --number-sections
```

## Troubleshooting

- **Output formatting looks wrong**: Specify input and output formats explicitly. Use a reference DOCX or template. Convert to Markdown first and inspect structure.
- **Images missing**: Verify relative paths. Use `--extract-media` when converting from DOCX.
- **Tables degrade**: Complex tables may need manual cleanup after conversion.
