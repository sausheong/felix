---
name: cortex
description: Personal knowledge graph and memory system. Remember facts, sync files, recall knowledge, manage entities and relationships.
tags:
  - memory
  - knowledge
  - recall
  - remember
  - forget
  - entities
  - relationships
  - graph
  - notes
  - sync
---

# Cortex

Cortex is a personal knowledge graph and memory system. Use it to store and retrieve knowledge — people, organizations, concepts, events, and their relationships.

## Two Ingestion Paths

1. **`cortex remember <text>`** — ingest ad-hoc text, extract entities/relationships/memories
2. **`cortex sync <dir>`** — sync a directory of text files (`.md`, `.csv`, `.yaml`, `.json`, `.txt`, `.tsv`, `.xml`, `.toml`). Auto-detects format. Incremental — only re-processes changed files.

## Commands

```
cortex init                           Create brain.db in the current directory
cortex remember <text>                Ingest text, extract entities/relationships/memories
cortex recall <query>                 Natural language query with multi-strategy search
cortex sync <dir>                     Sync text files from a directory (incremental, auto-detects format)
cortex entity list [--type <type>]    List entities, optionally filtered by type
cortex entity get <id>                Show entity details, attributes, and relationships
cortex forget --source <src>          Remove all knowledge from a source
cortex forget --entity <id>           Remove a specific entity and all linked data
```

## Usage Patterns

Remember facts from conversations:
```bash
cortex remember "Alice works at Stripe as a staff engineer"
cortex remember "Bob and Alice went to Stanford together"
cortex remember "Meeting with Carol next Tuesday to discuss the Series A"
```

Sync a directory of notes, data files, or exported data:
```bash
cortex sync ~/notes
cortex sync ~/exports/contacts.csv
```

Query the knowledge graph:
```bash
cortex recall "who works at Stripe"
cortex recall "what do I know about Alice"
cortex recall "who should I invite to dinner who knows both Alice and Bob"
```

Browse entities and relationships:
```bash
cortex entity list --type person
cortex entity list --type organization
cortex entity get <entity-id>
```

Clean up:
```bash
cortex forget --source "file:/path/to/old-notes"
cortex forget --entity <entity-id>
```

## When to Use

- User mentions remembering something, storing knowledge, or building context
- User wants to look up people, organizations, relationships, or past facts
- User wants to sync notes, CSV data, YAML configs, or JSON exports into a searchable graph
- User asks "what do I know about X" or "who is connected to Y"
- User wants to forget or clean up old knowledge

## Notes

- Cortex stores everything in a single `brain.db` SQLite file in the current directory
- Run `cortex init` first to create the database
- Requires `OPENAI_API_KEY` for LLM extraction and semantic search (works without it but limited to deterministic extraction and keyword search)
- Supports Anthropic (`LLM_PROVIDER=anthropic`) and OpenAI-compatible APIs (`OPENAI_BASE_URL`)
- All data (emails, calendar events, PDFs) should be converted to text or supported file formats first, then ingested via `remember` or `sync`
