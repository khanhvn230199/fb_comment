# fb_comment

Project Go dùng Gin, PostgreSQL và GORM với cấu trúc:

```text
fb_comment/
├── controller/
├── model/
├── view/
├── go.mod
└── main.go
```

## Chức năng user

- Đăng nhập bằng tài khoản và mật khẩu.
- Sinh JWT sau khi đăng nhập.
- Lưu JWT trong cookie `token` cho giao diện HTML hoặc trả về JSON khi request có `Accept: application/json`.
- Middleware kiểm tra JWT trước khi vào các màn/API cần đăng nhập.
- Cập nhật mật khẩu sau khi đã đăng nhập, nút đổi mật khẩu nằm trong module User.
- User có role `admin` hoặc `user`; tài khoản mặc định được seed với role `admin`.
- User đã đăng nhập có thể vào module User để xem username/role của chính mình và đổi mật khẩu.
- Admin có thể xem toàn bộ user và quản lý user: thêm, sửa role/password/limit, xóa user.

## Cấu hình và chạy ứng dụng

Ứng dụng đọc `DATABASE_DSN` nếu có. Nếu không có, ứng dụng dùng các biến sau:

```bash
DB_HOST=localhost
DB_PORT=5435
DB_USER=postgres
DB_PASSWORD=postgres
DB_NAME=fb_comment
DB_SSLMODE=disable
DB_TIMEZONE=Asia/Ho_Chi_Minh
JWT_SECRET=change-this-secret-in-production
APP_PORT=8080
ADMIN_USERNAME=admin
ADMIN_PASSWORD=123456789
```

### Chạy bằng Docker Compose trên VPS

Nếu muốn bootstrap tự động trên VPS, chạy:

```bash
bash bootstrap-vps.sh
```

Script này sẽ cài Docker nếu cần, clone repo vào `/root/fb_comment`, tạo `.env`, yêu cầu các secrets cần thiết và khởi động stack.

1. Tạo file môi trường từ mẫu:

```bash
cp .env.example .env
```

2. Sửa các giá trị quan trọng trong `.env`:
   - `DB_PASSWORD`
   - `JWT_SECRET`
   - `ADMIN_PASSWORD`
   - nếu cần thì đổi thêm `APP_PORT`

3. Khởi động stack bằng image do workflow push lên GHCR:

```bash
APP_IMAGE=ghcr.io/<owner>/<repo>:sha-<commit> docker compose up -d --no-build
```

Nếu bạn dùng VPS ở `root@204.168.143.212`, thư mục deploy mình đã chuẩn bị là `/root/fb_comment`.

4. Kiểm tra container:

```bash
docker compose ps
```

5. Xem log app:

```bash
docker compose logs -f app
```

6. Kiểm tra health endpoint:

```bash
curl -fsS http://127.0.0.1:8080/healthz
```

7. Mở ứng dụng:

```text
http://<VPS_IP>:8080
```

Ghi chú:

- PostgreSQL chỉ bind nội bộ ở `127.0.0.1:5435` trên VPS.
- App container đã dùng image có Chromium/Playwright sẵn, không cần cài browser thủ công.
- Dừng stack bằng `docker compose down`.

## CI/CD với GitHub Actions

Pipeline được đề xuất cho repo này:

- **Pull request / push bất kỳ nhánh nào**: chạy `gofmt` check, `go mod verify`, `go vet ./...`, `go test ./...`, `docker build`, và `docker compose config`.
- **Push vào `main`**: build image từ `Dockerfile`, push lên GHCR với tag `sha-<commit>` và `latest`, sau đó SSH vào VPS để `docker compose pull app` và `docker compose up -d --no-build`.
- Sau deploy, workflow gọi `GET /healthz` để xác nhận app và PostgreSQL đã sẵn sàng.

Secrets cần có trên GitHub:

- `VPS_HOST`
- `VPS_USER`
- `VPS_SSH_KEY`
- `VPS_APP_DIR`
- `VPS_PORT` nếu VPS không dùng SSH port mặc định

Secrets của ứng dụng vẫn nên nằm trên VPS trong file `.env`:

- `DB_PASSWORD`
- `JWT_SECRET`
- `ADMIN_PASSWORD`
- các biến cấu hình DB/app khác nếu bạn thay đổi mặc định

### Chạy local không Docker

Nếu muốn chạy trực tiếp trên máy local:

```bash
go run github.com/mxschmitt/playwright-go/cmd/playwright install chromium
go mod tidy
go run .
```

Mở: http://localhost:8080

Tài khoản mặc định lần đầu: `admin` / `123456789` với role `admin`.

Mật khẩu user được mã hóa bằng `bcrypt` trước khi lưu vào PostgreSQL.

## Quản lý user

User đã đăng nhập có thể truy cập màn User. Role `user` chỉ xem được tài khoản của chính mình; role `admin` xem được toàn bộ user. Các nút/form CRUD chỉ hiển thị và chỉ dùng được với user có role `admin`.

Màn HTML:

```text
http://localhost:8080/users
```

Route HTML:

- `GET /users` — danh sách user gồm username/role và nút cập nhật mật khẩu của chính mình
- `GET /users/new` — admin mở màn thêm user
- `POST /users` — admin thêm user
- `GET /users/:id/edit` — admin mở form sửa user
- `POST /users/:id` — admin cập nhật username, role, limit hoặc password
- `POST /users/:id/delete` — admin xóa user
- `POST /users-bulk-delete` — admin xóa nhiều user đã chọn trên màn danh sách

API JSON có JWT và role admin:

- `GET /api/users`
- `POST /api/users`
- `GET /api/users/:id`
- `PATCH /api/users/:id`
- `POST /api/users/bulk-delete`
- `DELETE /api/users/:id`

Guardrail:

- Không thể xóa chính tài khoản đang đăng nhập.
- Không thể xóa hoặc hạ quyền admin cuối cùng.
- Bulk delete user là all-or-nothing: nếu có user không tồn tại/không hợp lệ thì không xóa partial.
- Password khi tạo/sửa user luôn được hash bằng `bcrypt`.

Admin có thể cấu hình limit cho từng user:

- `link_on_limit` — giới hạn link on
- `link_off_limit` — giới hạn link off
- `like_limit` — giới hạn like
- `daily_limit` — giới hạn theo ngày

Giá trị `0` nghĩa là không giới hạn.

## Quản lý resource

Resource được quản lý theo `user_id`: user thường chỉ thấy/sửa/xóa resource của chính họ; admin xem/sửa/xóa/cấp resource cho toàn bộ user. Field `created_by_id` chỉ dùng để audit người import, không dùng để phân quyền.

Resource hỗ trợ các type:

- `token`
- `proxy`
- `cookie`

Status hỗ trợ:

- `active`
- `inactive`
- `used`
- `error`

Màn HTML:

```text
http://localhost:8080/resources
```

Route HTML có JWT cookie:

- `GET /resources` — danh sách resource có paging, user thường chỉ thấy resource của mình; admin có filter type/status/user
- `POST /resources/import` — nhập đúng 1 token/proxy/cookie value; admin có thể gửi `user_id` để cấp cho user khác
- `GET /resources/:id/edit` — form sửa resource, không hiển thị raw value
- `POST /resources/:id` — cập nhật type/status/replace value; admin có thể đổi owner
- `POST /resources/:id/delete` — xóa một resource theo scope user/admin
- `POST /resources-bulk-delete` — xóa nhiều resource đã chọn trên trang hiện tại theo scope user/admin
- `POST /resources/delete-by-status` — xóa nhiều resource theo status, có thể kèm type và user_id cho admin

API JSON có JWT:

- `GET /api/resources?type=token&status=active&page=1&per_page=50`
- `POST /api/resources/import`
- `GET /api/resources/:id`
- `PATCH /api/resources/:id`
- `POST /api/resources/bulk-delete`
- `DELETE /api/resources/:id`
- `DELETE /api/resources?status=error&type=proxy`

Ví dụ admin import resource cho user ID 2:

```bash
curl -X POST http://localhost:8080/api/resources/import \
  -H 'Authorization: Bearer <JWT_TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{
    "user_id": 2,
    "type": "proxy",
    "status": "active",
    "value": "host:port:user:pass"
  }'
```

User thường import resource thì hệ thống tự gán `user_id` bằng user đang đăng nhập; nếu gửi `user_id` khác sẽ bị từ chối. Mỗi request import chỉ nhận đúng 1 `value` single-line; legacy `items`/`list` chỉ được chấp nhận khi có đúng 1 value non-empty, bulk nhiều dòng sẽ bị từ chối. Resource type `token` còn bị chặn nếu value chứa khoảng trắng/tab/newline để tránh nhập nhầm nhiều access token vào cùng một record; mỗi token phải là một resource riêng. Resource được check trùng theo từng user bằng `(user_id, type, value_hash)`, nên cùng một token/proxy/cookie có thể cấp cho nhiều user khác nhau nhưng không duplicate trong cùng user. Khi list HTML/API, giá trị token/proxy/cookie chỉ hiển thị dạng masked, không trả raw value. Bulk delete resource theo ID dùng scoping hiện tại: user thường chỉ xóa resource của mình, admin xóa được toàn bộ; nếu có resource `active` thì request cần `confirm=true`.

Các API list (`/api/resources`, `/api/users`, `/api/links`, `/api/comments`) đều trả thêm object `pagination` và hỗ trợ `page`/`per_page`; vẫn tương thích `limit`/`offset` cho client cũ.

## Module link

Sau khi đăng nhập, mở trang quản lý link:

```text
http://localhost:8080/links?active=true
```

Sidebar `Links` mặc định mở tab `Active`. Bảng link có 2 tab filter cứng:

- `Active` — chỉ hiện link đang bật `active = true`
- `Inactive` — chỉ hiện link đang tắt `active = false`

Bảng `links` được migrate tự động bằng GORM với các field chính. Màn Links hiển thị thêm tổng comment và tổng like/reaction nếu app lấy được metrics từ Facebook Graph API; nếu chưa lấy được thì hiện `Chưa có dữ liệu` thay vì lấy số comment đã cào làm tổng.

Lịch crawl comment và refresh metrics được quản lý chung tại màn Settings, áp dụng cho toàn bộ link active.

Các field chính:

- `url`, `final_url`
- `active`, `status`, `last_error`
- `max_comments`, `max_scrolls`, `idle_passes`
- `last_scraped_at`, `next_scrape_at`
- `metrics_next_refresh_at`, `metrics_fetched_at`
- `total_comment_count`, `total_like_count`

Route HTML:

- `GET /links` — danh sách link
- `POST /links` — thêm link
- `GET /links/:id/edit` — form sửa link
- `POST /links/:id` — cập nhật link
- `POST /links/:id/toggle` — bật/tắt link
- `POST /links/:id/delete` — xóa link
- `GET /settings` — cấu hình polling chung cho admin

API JSON có JWT:

- `GET /api/links` hoặc `GET /api/links?active=true|false`
- `POST /api/links`
- `PATCH /api/links/:id`
- `DELETE /api/links/:id`

## Module settings

Màn Settings chỉ dành cho admin:

```text
http://localhost:8080/settings
```

Tại đây chỉnh 2 lịch chạy chung cho toàn bộ link active:

- `comment_poll_interval_seconds`
- `metrics_poll_interval_seconds`

## Module comment và API cào Facebook

Màn hình comment HTML sau khi đăng nhập:

```text
http://localhost:8080/comments
```

Màn này có form nhập nhiều Facebook link để gọi `/api/scrape`, đồng thời hiển thị danh sách comment kèm link tham chiếu từ bảng `links`.

Bảng `comments` được migrate tự động bằng GORM và liên kết với `links` qua `link_id`.
Comment được lưu tách riêng:

- `author` — tên user/comment author Facebook, hiển thị dạng tên FB kèm link `profile_url` nếu có
- `author_uid` — UID Facebook, tự parse từ `profile_url` dạng `profile.php?id=...` khi có
- `phone` — SĐT parse từ nội dung comment/raw text nếu user comment số điện thoại; Facebook Graph không public SĐT người comment
- `comment_text` — nội dung comment đã clean
- `date_label`, `raw_text`, `profile_url`, `permalink`, `facebook_created_at`
- tên bài/link bài lấy từ bảng `links` qua `link_id` (`title`, `url`, `final_url`)
- chống trùng bằng `comment_key`; nếu permalink có `comment_id` hoặc `reply_comment_id` thì tách ID đó lưu thẳng vào `comment_key` để check trùng

Cài browser cho Playwright nếu chạy local không Docker:

```bash
go run github.com/mxschmitt/playwright-go/cmd/playwright install chromium
```

Biến môi trường scraper (Docker image đã có Chromium sẵn, phần này chỉ cần cho local):

```bash
SCRAPER_HEADLESS=true
SCRAPER_WAIT_MS=1000
SCRAPER_MAX_SCROLLS=1
SCRAPER_TIMEOUT_MS=60000
SCRAPER_STORAGE_PATH=
```

API CRUD comment có JWT:

- `GET /api/comments?comment_date=2026-08-03&link_id=1&page=1&per_page=50`
- `GET /api/comments?link_id=1&limit=50&offset=0`
- `GET /api/comments/:id`
- `POST /api/comments`
- `PATCH /api/comments/:id`
- `DELETE /api/comments/:id`

Màn `/comments` mặc định filter `comment_date` là ngày hôm nay. Có thể bấm nút `Excel` để tải CSV mở bằng Excel theo filter hiện tại:

- `GET /comments/export?comment_date=2026-08-03&link_id=1`

Ví dụ tạo comment thủ công:

```bash
curl -X POST http://localhost:8080/api/comments \
  -H 'Authorization: Bearer <JWT_TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{
    "link_id": 1,
    "author": "Nguyen Van A",
    "comment_text": "Nội dung comment",
    "date_label": "2 giờ",
    "profile_url": "https://www.facebook.com/profile.php?id=...",
    "permalink": "https://www.facebook.com/..."
  }'
```

API cào comment từ list link Facebook:

```bash
curl -X POST http://localhost:8080/api/scrape \
  -H 'Authorization: Bearer <JWT_TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{
    "links": [
      "https://www.facebook.com/.../posts/..."
    ],
    "max_comments": 10,
    "max_scrolls": 1,
    "refresh": true
  }'
```

Background poller:

- Khi app chạy, một goroutine sẽ tự động poll bảng `links` mỗi 5 giây.
- Link nào `active = true` và đến hạn `next_scrape_at <= now` sẽ được cào lại bằng Playwright.
- Comment mới được insert vào `comments`, comment cũ không bị trùng nhờ `(link_id, comment_key)`.
- Màn `/comments` đọc comment từ DB; có thể refresh để thấy comment mới.

Luồng `/api/scrape`:

1. Nhận list link Facebook.
2. Chuẩn hóa và check trùng link trong request.
3. Lưu link mới hoặc kích hoạt lại link đã có trong bảng `links`.
4. Dùng Playwright để cào comment.
5. Lưu comment vào bảng `comments`, tách riêng `author` và `comment_text`.
6. Không lưu trùng comment nhờ `(link_id, comment_key)`.

## API JSON

Đăng nhập:

```bash
curl -X POST http://localhost:8080/login \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json' \
  -d '{"username":"admin","password":"123456789"}'
```

Đổi mật khẩu:

```bash
curl -X POST http://localhost:8080/password \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json' \
  -H 'Authorization: Bearer <JWT_TOKEN>' \
  -d '{"old_password":"123456789","new_password":"newpass123","confirm_password":"newpass123"}'
```
