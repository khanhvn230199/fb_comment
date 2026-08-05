# Plan: multi-link Facebook comment tracker với DB + UI

## Context

User muốn nâng project `/Users/godhitech/Desktop/playwright` từ API scrape 1 link thành hệ thống theo dõi nhiều link Facebook public:

- Input nhiều link cần theo dõi.
- Mỗi link được scrape lại theo chu kỳ khoảng 5 giây.
- Mỗi lần scrape chỉ lấy tối đa 10 comment mới nhất.
- Comment mới được lưu vào DB, không lưu trùng.
- DB cần 2 bảng chính: bảng link và bảng comment thuộc các link đó.
- UI cần form nhập link và bảng comment để theo dõi realtime-ish.

Project hiện tại đã có phần lõi hữu ích trong `main.go`:

- `APIScraper` giữ Chromium sống xuyên suốt API server.
- `FacebookScraper` có lifecycle `StartWithBrowser`, `OpenPost`, `PrepareComments`, `ExtractComments`.
- `FacebookComment` đã có `Key`, `Author`, `Content`, `Date`, `ProfileURL`, `Permalink`, `FirstSeenAt`.
- Helper đã có: `validateFacebookURL`, `dedupeComments`, `toAPIComments`, `commentKey`, `normalizeText`, `cleanCommentContent`.
- Chưa có DB và chưa có UI.

Recommended architecture: vẫn dùng Go `net/http` hiện tại, thêm SQLite local DB và background poller. UI không gọi Playwright trực tiếp mỗi 5 giây; UI chỉ poll DB-backed API. Background poller là nơi scrape Facebook.

---

## Phase 1 — DB foundation

### Goal

Thêm SQLite persistence để lưu tracked links và comments, chưa động vào background polling/UI.

### Implementation

- Thêm dependency SQLite thuần Go: `modernc.org/sqlite` qua `database/sql`.
- Thêm flag config:
  - `-db data/comments.db`
- Tạo file mới nếu muốn tách code:
  - `store.go`
- Tạo migration khi server start hoặc khi mở store.
- Bật SQLite options:
  - `PRAGMA foreign_keys = ON`
  - `PRAGMA journal_mode = WAL`
  - `PRAGMA busy_timeout = 5000`

### Schema

`links`:

```sql
CREATE TABLE IF NOT EXISTS links (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  url TEXT NOT NULL UNIQUE,
  final_url TEXT,
  active INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL DEFAULT 'pending',
  last_error TEXT,
  poll_interval_seconds INTEGER NOT NULL DEFAULT 5,
  max_comments INTEGER NOT NULL DEFAULT 10,
  max_scrolls INTEGER NOT NULL DEFAULT 1,
  last_scraped_at TEXT,
  next_scrape_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
```

Indexes:

```sql
CREATE INDEX IF NOT EXISTS idx_links_active_next_scrape_at
ON links(active, next_scrape_at);

CREATE INDEX IF NOT EXISTS idx_links_status
ON links(status);
```

`comments`:

```sql
CREATE TABLE IF NOT EXISTS comments (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  link_id INTEGER NOT NULL REFERENCES links(id) ON DELETE CASCADE,
  comment_key TEXT NOT NULL,
  author TEXT,
  content TEXT NOT NULL,
  date_label TEXT,
  raw_text TEXT,
  profile_url TEXT,
  permalink TEXT,
  first_seen_at TEXT NOT NULL,
  scraped_at TEXT NOT NULL,
  UNIQUE(link_id, comment_key)
);
```

Indexes:

```sql
CREATE INDEX IF NOT EXISTS idx_comments_link_scraped_at
ON comments(link_id, scraped_at DESC);

CREATE INDEX IF NOT EXISTS idx_comments_first_seen_at
ON comments(first_seen_at DESC);
```

### Repository methods

Add reusable methods:

- `OpenStore(path string) (*Store, error)`
- `(*Store).Close() error`
- `(*Store).Migrate() error`
- `(*Store).CreateOrReactivateLink(url string) (TrackedLink, error)`
- `(*Store).ListLinks() ([]TrackedLink, error)`
- `(*Store).GetDueLink(now time.Time) (TrackedLink, bool, error)`
- `(*Store).MarkLinkScraping(id int64) error`
- `(*Store).MarkLinkScraped(id int64, finalURL string, next time.Time) error`
- `(*Store).MarkLinkError(id int64, errText string, next time.Time) error`
- `(*Store).DeleteLink(id int64) error`
- `(*Store).SetLinkActive(id int64, active bool) error`
- `(*Store).InsertComments(linkID int64, comments []FacebookComment, scrapedAt time.Time) (int, error)`
- `(*Store).ListRecentComments(limit int) ([]StoredComment, error)`
- `(*Store).ListLinkComments(linkID int64, limit int) ([]StoredComment, error)`

### Tests

Update/add tests in `main_test.go` or `store_test.go`:

- Migration creates tables.
- Insert same link twice reactivates/returns same link.
- Insert same comment twice dedupes by `(link_id, comment_key)`.
- Recent comments are sorted newest first.

### Prompt for this phase

```text
Implement Phase 1 only for /Users/godhitech/Desktop/playwright: add SQLite persistence using database/sql and modernc.org/sqlite. Add a -db flag defaulting to data/comments.db. Create migrations for links and comments, add Store/repository methods for creating/reactivating links, listing links, selecting due links, updating scrape status, deleting/toggling links, inserting comments with dedupe, and listing newest comments. Do not add background polling or UI yet. Add unit tests for migrations, link reactivation, comment dedupe, and newest comment queries.
```

---

## Phase 2 — enforce “max 10 newest comments” in scraper/API

### Goal

Chuẩn hóa requirement: mỗi scrape/get chỉ lấy tối đa 10 comment mới nhất.

### Implementation

Modify existing `main.go`:

- Extend `CommentListRequest`:

```go
type CommentListRequest struct {
    URL         string `json:"url"`
    MaxScrolls  int    `json:"maxScrolls,omitempty"`
    OldestFirst bool   `json:"oldestFirst,omitempty"`
    Refresh     bool   `json:"refresh,omitempty"`
    Limit       int    `json:"limit,omitempty"`
}
```

- Add constants:

```go
const defaultCommentLimit = 10
const maxCommentLimit = 10
```

- Update `parseCommentListRequest`:
  - GET supports `limit`.
  - POST supports `limit`.
  - If missing/zero: default 10.
  - If greater than 10: cap to 10.
  - If negative: error.
- Update `commentCacheKey` to include `Limit`.
- Update `APIScraper.scrapeFresh`:
  - Prefer newest order by setting `OldestFirst=false` for tracker/poller.
  - After `dedupeComments`, trim to `req.Limit`.
- Keep old `/comments` compatibility.

### Reuse existing code

- `APIScraper.Scrape`
- `APIScraper.scrapeFresh`
- `dedupeComments`
- `toAPIComments`
- `parseCommentListRequest`

### Tests

- GET `/comments?...&limit=100` caps to 10.
- POST body with no limit defaults to 10.
- Negative limit returns error.
- Helper `limitComments(comments, limit)` if added.

### Prompt for this phase

```text
Implement Phase 2 only for /Users/godhitech/Desktop/playwright: update the existing /comments scrape API so every scrape returns at most 10 newest comments by default. Add a Limit field to CommentListRequest, parse it from GET and POST, default it to 10, cap it at 10, reject negative values, include it in the cache key, and trim results after dedupe. Preserve existing /comments behavior and response shape. Add tests for limit parsing/capping and result limiting. Do not add DB poller or UI yet.
```

---

## Phase 3 — background poller for many links

### Goal

Server tự động scrape nhiều link trong DB theo chu kỳ 5 giây/link và lưu comment mới.

### Implementation

Add file:

- `poller.go`

Add types:

```go
type CommentScraper interface {
    Scrape(ctx context.Context, req CommentListRequest) (CommentListResponse, error)
}

type LinkPoller struct {
    store   *Store
    scraper CommentScraper
    logger  *log.Logger
    interval time.Duration
n}
```

Behavior:

- Start poller inside `runAPIServer` after DB and `APIScraper` are ready.
- Poller loop ticks every 1 second to find due work.
- For each due active link:
  1. `GetDueLink(now)`.
  2. `MarkLinkScraping(id)`.
  3. Call scraper:

```go
CommentListRequest{
    URL: link.URL,
    MaxScrolls: link.MaxScrolls,
    OldestFirst: false,
    Refresh: true,
    Limit: 10,
}
```

  4. Convert API comments back to `FacebookComment` or make scraper return internal comments for DB insert. Simpler: add helper to map `APIComment` -> `FacebookComment` retaining `Key`, `User`, `Comment`, `Date`, etc.
  5. `InsertComments(link.ID, comments, response.ScrapedAt)`.
  6. `MarkLinkScraped(id, response.FinalURL, now + 5s)`.
  7. On error: `MarkLinkError(id, err.Error(), now + 5s)`.

Important:

- Start with concurrency 1 because current `APIScraper` already serializes with `mu`.
- If many links exist, exact “every 5s per link” may drift because Facebook scraping is slow. This is acceptable and safer than parallel hammering.
- UI reads DB only; UI does not trigger Playwright every 5s.

### Tests

Use fake scraper, not Playwright:

- Due link gets scraped and comments inserted.
- Duplicate comments are ignored.
- Error updates `status='error'` and `last_error`.
- `next_scrape_at` moves about 5 seconds forward.

### Prompt for this phase

```text
Implement Phase 3 only for /Users/godhitech/Desktop/playwright: add a background LinkPoller that runs with the HTTP server, reads due active links from SQLite, scrapes each due link with Refresh=true, OldestFirst=false, Limit=10, stores only new comments, and schedules the next scrape 5 seconds later. Use one worker/concurrency 1. Update link status, final URL, last error, last scraped time, and next scrape time. Use a CommentScraper interface so tests can use a fake scraper. Do not build UI yet. Add tests for successful polling, dedupe, scheduling, and error handling.
```

---

## Phase 4 — DB-backed tracking API

### Goal

Expose API để UI quản lý links và đọc comments từ DB.

### Endpoints

Keep existing:

- `GET /health`
- `GET/POST /comments` direct scrape compatibility

Add:

- `GET /api/links`
  - Returns tracked links and scrape status.
- `POST /api/links`
  - Body: `{ "url": "https://www.facebook.com/..." }`
  - Validate with existing `validateFacebookURL`.
  - Insert/reactivate link.
  - Schedule immediate scrape by setting `next_scrape_at=now`.
- `PATCH /api/links/{id}`
  - Body: `{ "active": true }` or `{ "active": false }`.
- `DELETE /api/links/{id}`
  - Hard delete link and comments for simplicity.
- `GET /api/links/{id}/comments?limit=10`
  - Return newest comments for one link.
- `GET /api/comments?limit=10`
  - Return newest comments across all links, includes link URL/info.

### Response shape

Links:

```json
{
  "links": [
    {
      "id": 1,
      "url": "https://www.facebook.com/...",
      "finalUrl": "https://www.facebook.com/...",
      "active": true,
      "status": "ok",
      "lastError": "",
      "lastScrapedAt": "2026-07-30T...Z",
      "nextScrapeAt": "2026-07-30T...Z"
    }
  ]
}
```

Comments:

```json
{
  "comments": [
    {
      "id": 1,
      "linkId": 1,
      "url": "https://www.facebook.com/...",
      "user": "Khánh Vũ",
      "comment": "hay",
      "date": "17 phút",
      "profileUrl": "https://www.facebook.com/...",
      "permalink": "https://www.facebook.com/...",
      "firstSeenAt": "2026-07-30T...Z",
      "scrapedAt": "2026-07-30T...Z"
    }
  ]
}
```

### Handler structure

Can keep in `main.go` or split:

- `api.go` for DB-backed handlers.
- Extend `APIHandler`:

```go
type APIHandler struct {
    cfg     ScraperConfig
    scraper *APIScraper
    store   *Store
}
```

### Tests

Use `httptest`:

- POST invalid URL returns 400.
- POST valid URL creates link.
- GET links returns inserted link.
- GET comments caps limit to 10.
- DELETE removes link/comments.

### Prompt for this phase

```text
Implement Phase 4 only for /Users/godhitech/Desktop/playwright: add DB-backed JSON endpoints for tracking links and comments: GET/POST /api/links, PATCH/DELETE /api/links/{id}, GET /api/links/{id}/comments, and GET /api/comments. Validate Facebook URLs with the existing validateFacebookURL helper. All comment list endpoints should default to 10 and cap at 10. Keep the existing direct scrape /comments endpoint working. Add httptest coverage for create/list/delete links, invalid URLs, and comment limit capping.
```

---

## Phase 5 — simple tracking UI

### Goal

Có giao diện để nhập link và xem bảng comment đang theo dõi.

### UI approach

No React/Vite yet. Use simple server-rendered HTML with inline CSS/JS from Go.

Routes:

- `GET /tracker` serves HTML.
- Optionally make `/` redirect to `/tracker` or render same UI.

UI components:

1. Form nhập link:
   - Text input: Facebook post URL.
   - Submit button: “Theo dõi link”.
   - Calls `POST /api/links`.

2. Bảng links:
   - ID
   - URL
   - active
   - status
   - last scraped
   - next scrape
   - last error
   - delete/toggle buttons

3. Bảng comments mới nhất:
   - Link URL
   - User
   - Comment
   - Facebook date label
   - First seen
   - Permalink/profile links

Frontend JS:

- On page load:
  - fetch `/api/links`
  - fetch `/api/comments?limit=10`
- Every 5 seconds:
  - refresh links/comments from DB endpoints.
- After add link:
  - clear input
  - refresh immediately.
- Show error message if API fails.

### Tests

- `GET /tracker` returns HTML and contains form/table markers.

### Prompt for this phase

```text
Implement Phase 5 only for /Users/godhitech/Desktop/playwright: add a dependency-free tracking UI served by Go at /tracker, with inline HTML/CSS/JavaScript. The page must have a form to add Facebook links, a tracked links/status table, and a latest comments table. The browser should poll /api/links and /api/comments?limit=10 every 5 seconds and refresh immediately after adding a link. Add delete/toggle actions if the Phase 4 endpoints exist. Add a basic handler test that /tracker serves HTML with the form and comments table.
```

---

## Phase 6 — server wiring, docs, and manual verification

### Goal

Wire everything into one usable app and document how to run it.

### Implementation

Update `runAPIServer`:

1. Open DB from `cfg.DBPath`.
2. Run migrations.
3. Start `APIScraper`.
4. Start `LinkPoller` with the same scraper and store.
5. Create handler with `cfg`, `scraper`, `store`.
6. Gracefully stop HTTP server/poller/browser on Ctrl+C.

Update `README.md`:

- Install deps.
- Optional Facebook login/session:

```bash
go run . -login -headful
```

- Start tracker:

```bash
go run . \
  -serve \
  -addr=:8080 \
  -headless=true \
  -storage=data/facebook-auth.json \
  -db=data/comments.db \
  -max-scrolls=1 \
  -wait-ms=300
```

- Open UI:

```text
http://localhost:8080/tracker
```

- Explain:
  - Background poller checks due links.
  - Each link is scheduled every 5s.
  - Each scrape stores max 10 newest comments.
  - UI refreshes every 5s from DB.
  - If many links are tracked, actual scrape cadence can drift because Facebook/Playwright is slow and concurrency is intentionally 1.

### Verification

Run:

```bash
go test ./...
```

Manual smoke test:

1. Start server.
2. Open `/tracker`.
3. Add 2 Facebook links.
4. Confirm links appear with `pending/scraping/ok`.
5. Wait 5–15 seconds.
6. Confirm comments appear in DB/UI.
7. Add new Facebook comment manually.
8. Confirm it appears after next background scrape.
9. Delete/toggle link and confirm it stops updating.

### Prompt for this phase

```text
Implement Phase 6 only for /Users/godhitech/Desktop/playwright: wire the SQLite Store and LinkPoller into runAPIServer, add graceful shutdown, update README with the full multi-link tracker workflow, and run verification. Document login/session, DB path, starting the server, opening /tracker, adding multiple links, 5-second background polling, max 10 newest comments per scrape/get, and expected drift when many links are tracked. Run go test ./... and do a manual smoke test if possible. Fix only issues discovered during verification, without adding unrelated features.
```

---

## Recommended execution order

1. Phase 1: DB foundation.
2. Phase 2: max 10 newest comments.
3. Phase 3: background poller.
4. Phase 4: DB-backed API.
5. Phase 5: UI.
6. Phase 6: wiring/docs/verification.

This keeps each phase small and testable. Avoid doing UI before DB/poller, because UI should read from DB and not directly trigger expensive Playwright scrapes.

---

## Important notes / risks

- Facebook DOM is unstable, so reuse existing `ExtractComments` instead of writing new extraction logic.
- “Mỗi link delay 5s” should mean each link is scheduled for scrape every 5s, but exact timing can drift if a scrape takes longer than 5s.
- Start with one scrape worker to avoid hammering Facebook and because `APIScraper` currently serializes scrape calls.
- SQLite is good for local/single-process use. If later deploying multiple server instances, migrate to Postgres.
- Do not store Facebook cookies/tokens in DB. Keep Playwright storage state in ignored file `data/facebook-auth.json`.
