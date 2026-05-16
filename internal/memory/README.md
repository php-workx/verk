# internal/memory

Package `memory` is the storage layer for verk's memory learning loop.

## Files on disk

Both files live in the per-project data directory (`.verk/memory/` by default):

```
escaped-defects.jsonl    — one EscapedDefect JSON object per line
promoted-rules.jsonl     — one PromotionEntry JSON object per line
```

Both files are **append-only JSONL**. Records are never edited in place.
Status updates write a new record with the same `id` and the updated
`status` field; readers deduplicate using last-record-wins.

## ID scheme

Lesson IDs follow the pattern `learn-<unixNano>`, where `<unixNano>` is
`time.Now().UnixNano()` at the time the lesson is created. This keeps IDs
monotonically sortable and avoids collisions under normal usage.

Example: `learn-1715600000000000000`

## Status transitions

```
proposed → promoted     (operator ran: verk learn promote)
proposed → rejected     (operator ran: verk learn reject)
promoted → superseded   (operator recorded a newer lesson that replaces this one)
```

All transitions are recorded as new JSONL lines, never by editing existing
lines. The effective state of a lesson is the record with the highest
`created_at` for that `id`.

## Deduplication (last-record-wins)

`ListLessons` reads every line and builds a map keyed by `id`. When the
same `id` appears more than once, the record with the later (or equal)
`created_at` wins. Output order preserves the first-appearance order of
each unique `id`.

## Length limits (validated at write time)

| Field | Limit |
|---|---|
| `EscapedDefect.Summary` | 4096 chars |
| `EscapedDefect.RecommendedRule` | 2048 chars |
| `PromotionEntry.RuleID` | 256 chars |
| `PromotionEntry.Summary` | 2048 chars |

`AppendLesson` and `AppendPromotion` return an error if any limit is
exceeded. No data is written to disk on validation failure.
