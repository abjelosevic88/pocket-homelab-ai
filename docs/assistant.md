# Personal assistant

The home screen defaults to **Work on the server**, which keeps the coding agents and their settings. **Ask my apps** is the second choice. A connected Pocket Ask account is required for app operations. App-only threads use restricted Claude with no shell/file tools; normal coding threads retain the model the user selected.

## Sources and performance

New app reads and writes are recorded per person. Sources & app activity is available in each conversation; Action history lists the latest 200 records. Reads are retained for 30 days, writes until their conversation is deleted. Results include the exact arguments, timestamps, public source links where available, and call duration. Read latency excludes model reasoning. Large overviews explicitly report their limits; they are not complete exports.

A small relevant tool set is advertised initially. `pocket_find_tools` returns exact schemas for additional approved tools; `pocket_call_tool` runs through the same server-side write approval as a direct call. `pocket_search` searches all accessible collections that expose a text-search endpoint, plus returned overview metadata for Garage vehicles, Split groups, Habits and recent Office documents. It reports coverage and display limits. `pocket_briefing` reads calendar, task/review summaries, recurring bills and maintenance concurrently.

`pocket_vehicle_costs` separates confirmed linked duplicates from possible date/amount matches. Confirmation requires the recorded transaction ID and Money owner plus matching date, currency and amount. Uncertain matches are never subtracted. The tool does not produce a combined total from incomplete records.

## Approvals and undo

Every app write pauses for an exact operation approval, including coding threads in full-access mode. Multiple pending writes can be reviewed and selected; new proposals are never preselected. A timeout/denial sends nothing. An interrupted in-flight write is marked unknown: check the app before retrying.

Undo is available for Notes updates with recorded before/after versions. It opens a new conversation and requires a new approval. The app's etag precondition prevents overwriting later edits. Other operations explicitly show undo as unavailable.

## Briefing, workflows and memory

Briefing is separate from Today (agent run history). It shows current app data and partial failures, with source details and per-item help.

Workflows store a reusable prompt, model and trigger. Manual, daily/weekly/cron schedules and polling for new Tasks/Documents are supported. Event checks run about once a minute, examine at most 100 recent task events or documents ordered by descending ID and establish a baseline on first enable. Large imports may exceed this window; this is not guaranteed event delivery. An occurrence has a unique key, so it cannot silently replay. Runs use fresh data, retain normal write approvals, and can notify on completion. No workflows are enabled automatically for the user.

Memory contains only explicitly saved preferences. Disable, edit or forget each one; disable all saved preferences for an individual conversation from its menu. The current preferences are loaded at actual dispatch, including queued messages. Removing memory does not delete earlier conversation messages. Fresh application records take precedence.

## Documents

Attach up to 10 images, PDFs, DOCX/XLSX/PPTX, OpenDocument, old DOC/XLS/PPT (converted by Collabora when `OFFICE_CONVERT_URL` is set), or UTF-8 text files: CSV/TSV, JSON, HTML, XML, YAML/TOML/INI/conf, logs, source code, diffs, subtitles (SRT/VTT/ASS), ICS/VCF, EML and SVG (read as markup). Office extraction does not execute macros, evaluate formulas or fetch external resources. PDF extraction requires **pdftotext** on the service PATH (Poppler utilities); it reads at most 100 pages with a 10-second deadline. Extracted text is bounded at 180,000 bytes per document and 250,000 per message. Scanned PDFs without readable text need OCR in Documents or page images. Text extraction does not verify visual layout or signatures. File downloads are owner-scoped and use attachment disposition.

## Release checks

```sh
go test -race ./internal/server ./internal/documenttext
go test ./internal/...
cd web
npm run build
npm run test:activity
AI_TEST_BASE=http://127.0.0.1:8195 \
  AI_TEST_ENV=/path/to/test-account.env \
  AI_TEST_CDP=http://127.0.0.1:19239 \
  npm run test:mobile
```

The environment file supplies `ADMIN_EMAIL` and `ADMIN_PASSWORD` for the test account. Credentials are never logged. With no CDP endpoint the suite launches an installed Playwright Chromium. `AI_TEST_ASSETS=/absolute/path/to/web/dist` tests a candidate build against a running backend. Assistant endpoint fixtures prevent model runs and real app writes. Optional `AI_TEST_SCREENSHOTS` saves screenshots.

The suite checks 320/390/1280 widths in both themes, keyboard viewport offsets, Markdown sanitization/tables, file chips, source links, memory/workflow forms, selective approval and overflow. Browser viewport simulation is not a physical iPhone test: also check Safari keyboard open/close, long drafts, rotation and Ask focus on a real device when available.
