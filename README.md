# labelprint

Multi-tenant label printing. Go + Echo + HTMX + PostgreSQL. Each business has its own products, ZPL templates and
users. The server fills the ZPL template from the database; the browser sends it to the USB printer (WebUSB).

## Run with Docker
    docker compose up --build
    # app on http://localhost:8080, PostgreSQL in the "db" service (data in the pgdata volume)
    # env: DATABASE_URL  ADDR=:8080  ALLOW_REGISTER=true|false

## Run locally (needs PostgreSQL already running)
    export DATABASE_URL='postgres://labelprint:labelprint@localhost:5432/labelprint?sslmode=disable'
    go mod tidy
    go run .

Open http://localhost:8080 in Chrome/Edge on the PC the printer is plugged into, click "Create one", and
register your business. That account is the business admin.

## Roles (per business)
- admin: Print, Products, Templates, Team, Account. Adds/edits/deletes the business's non-admin users
  (email + password; blank password on edit keeps it; a reset signs that user out).
- user: Print and Account only.

## Notes
- Businesses are isolated: every table has tenant_id and every query filters on it.
- Set ALLOW_REGISTER=false once your businesses exist to close public sign-up.
- WebUSB works only on https:// or http://localhost; other machines need HTTPS (Caddy, mkcert).
- Windows needs the printer on the WinUSB driver (Zadig) for WebUSB to claim it.
- Upload your full .zpl (with logos) in Templates; barcodes are UPC-A (^BU) and ^FO decimals are rounded.
- UPC-A is optional: leave it blank and the field containing ${BARCODE} is omitted from the label.
- htmx loads from jsdelivr; download it into static/ for offline use.
- Print jobs are logged when ZPL is rendered, not when the printer confirms.
- The schema is created automatically on startup (CREATE TABLE IF NOT EXISTS); no migration tooling yet.
