# labelprint

Multi-tenant label printing. Go + Echo + HTMX + SQLite. Each business has its own products, ZPL templates and
users. The server fills the ZPL template from the database; the browser sends it to the USB printer (WebUSB).

## Run
    go mod tidy
    go run .
    # env: ADDR=:8080  DB_PATH=labelprint.db  ALLOW_REGISTER=true|false

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
- Upload your full .zpl (with logos) in Templates; ^BE (EAN-13) is converted to ^BU (UPC-A) and ^FO decimals rounded.
- htmx loads from jsdelivr; download it into static/ for offline use.
- Print jobs are logged when ZPL is rendered, not when the printer confirms.
- A fresh DB file is needed if you ran the earlier single-business version (the schema changed).
