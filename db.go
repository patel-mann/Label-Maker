package main

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go, no CGO
)

// Every business-owned table carries tenant_id, and every query below filters on it.
const schema = `
CREATE TABLE IF NOT EXISTS tenants (
  id INTEGER PRIMARY KEY, name TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  email TEXT NOT NULL UNIQUE COLLATE NOCASE, pass_hash TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin','user')),
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS sessions (
  token TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS templates (
  id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL, zpl TEXT NOT NULL, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS products (
  id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL, template_id INTEGER REFERENCES templates(id) ON DELETE SET NULL,
  barcode TEXT NOT NULL DEFAULT '', origin TEXT NOT NULL DEFAULT '', ingredients TEXT NOT NULL DEFAULT '',
  weight TEXT NOT NULL DEFAULT '', weight_unit TEXT NOT NULL DEFAULT 'g', price TEXT NOT NULL DEFAULT '',
  shelf_life_days INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS print_jobs (
  id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL, user_id INTEGER, product_id INTEGER, template_id INTEGER,
  qty INTEGER NOT NULL, batch TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE INDEX IF NOT EXISTS idx_products_tenant ON products(tenant_id);
CREATE INDEX IF NOT EXISTS idx_templates_tenant ON templates(tenant_id);
CREATE INDEX IF NOT EXISTS idx_jobs_tenant ON print_jobs(tenant_id, id);
`

var errEmailTaken = errors.New("that email is already registered")

type User struct {
	ID         int64
	TenantID   int64
	TenantName string
	Email      string
	Role       string
	hash       string
}
type Template struct {
	ID   int64
	Name string
	ZPL  string
}
type Product struct {
	ID            int64
	Name          string
	TemplateID    int64 // 0 = none
	Barcode       string
	Origin        string
	Ingredients   string
	Weight        string
	WeightUnit    string
	Price         string
	ShelfLifeDays int
}
type Job struct {
	When, Product, Batch, Email string
	Qty                         int
}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite is single-writer; one connection keeps PRAGMAs and ordering simple
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000", schema} {
		if _, err := db.Exec(q); err != nil {
			return nil, err
		}
	}
	return db, nil
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func uniqueErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return errEmailTaken
	}
	return err
}

// ---- tenants, users, sessions ----

// registerTenant creates a business, its first admin and a starter template in one transaction.
func (a *App) registerTenant(business, email, hash, starterZPL string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO tenants(name) VALUES(?)`, business)
	if err != nil {
		return err
	}
	tid, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO users(tenant_id,email,pass_hash,role) VALUES(?,?,?,'admin')`, tid, email, hash); err != nil {
		return uniqueErr(err)
	}
	if _, err := tx.Exec(`INSERT INTO templates(tenant_id,name,zpl) VALUES(?,?,?)`, tid, "Starter label (text only)", starterZPL); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) userByEmail(email string) (*User, error) {
	u := &User{}
	err := a.db.QueryRow(`SELECT id,tenant_id,email,role,pass_hash FROM users WHERE email=?`, email).Scan(&u.ID, &u.TenantID, &u.Email, &u.Role, &u.hash)
	return u, err
}
func (a *App) userBySession(token string) (*User, error) {
	u := &User{}
	err := a.db.QueryRow(`SELECT u.id,u.tenant_id,t.name,u.email,u.role,u.pass_hash FROM sessions s
		JOIN users u ON u.id=s.user_id JOIN tenants t ON t.id=u.tenant_id
		WHERE s.token=? AND s.expires_at>?`, token, time.Now().Unix()).Scan(&u.ID, &u.TenantID, &u.TenantName, &u.Email, &u.Role, &u.hash)
	return u, err
}
func (a *App) createSession(token string, uid int64, ttl time.Duration) error {
	a.db.Exec(`DELETE FROM sessions WHERE expires_at<?`, time.Now().Unix()) // opportunistic cleanup
	_, err := a.db.Exec(`INSERT INTO sessions(token,user_id,expires_at) VALUES(?,?,?)`, token, uid, time.Now().Add(ttl).Unix())
	return err
}
func (a *App) deleteSession(token string) { a.db.Exec(`DELETE FROM sessions WHERE token=?`, token) }

func (a *App) setPassword(uid int64, hash, keepToken string) error {
	if _, err := a.db.Exec(`UPDATE users SET pass_hash=? WHERE id=?`, hash, uid); err != nil {
		return err
	}
	_, err := a.db.Exec(`DELETE FROM sessions WHERE user_id=? AND token<>?`, uid, keepToken)
	return err
}

func (a *App) listTeam(tenant int64) ([]User, error) {
	rows, err := a.db.Query(`SELECT id,email,role FROM users WHERE tenant_id=? ORDER BY role, email`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Role); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Admins manage non-admin accounts only (role='user') inside their own business.
func (a *App) getTeamUser(tenant, id int64) (User, error) {
	var u User
	err := a.db.QueryRow(`SELECT id,email,role FROM users WHERE id=? AND tenant_id=? AND role='user'`, id, tenant).Scan(&u.ID, &u.Email, &u.Role)
	return u, err
}
func (a *App) createTeamUser(tenant int64, email, hash string) error {
	_, err := a.db.Exec(`INSERT INTO users(tenant_id,email,pass_hash,role) VALUES(?,?,?,'user')`, tenant, email, hash)
	return uniqueErr(err)
}
func (a *App) updateTeamUser(tenant, id int64, email, hash string) error {
	var res sql.Result
	var err error
	if hash == "" {
		res, err = a.db.Exec(`UPDATE users SET email=? WHERE id=? AND tenant_id=? AND role='user'`, email, id, tenant)
	} else {
		res, err = a.db.Exec(`UPDATE users SET email=?,pass_hash=? WHERE id=? AND tenant_id=? AND role='user'`, email, hash, id, tenant)
	}
	if err != nil {
		return uniqueErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	if hash != "" {
		_, err = a.db.Exec(`DELETE FROM sessions WHERE user_id=?`, id) // password reset signs them out
	}
	return err
}
func (a *App) deleteTeamUser(tenant, id int64) error {
	_, err := a.db.Exec(`DELETE FROM users WHERE id=? AND tenant_id=? AND role='user'`, id, tenant)
	return err
}

// ---- templates ----
func (a *App) listTemplates(tenant int64) ([]Template, error) {
	rows, err := a.db.Query(`SELECT id,name,zpl FROM templates WHERE tenant_id=? ORDER BY name`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Template
	for rows.Next() {
		var t Template
		if err := rows.Scan(&t.ID, &t.Name, &t.ZPL); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (a *App) getTemplate(tenant, id int64) (Template, error) {
	var t Template
	err := a.db.QueryRow(`SELECT id,name,zpl FROM templates WHERE id=? AND tenant_id=?`, id, tenant).Scan(&t.ID, &t.Name, &t.ZPL)
	return t, err
}
func (a *App) saveTemplate(tenant int64, t Template) error {
	var err error
	if t.ID == 0 {
		_, err = a.db.Exec(`INSERT INTO templates(tenant_id,name,zpl) VALUES(?,?,?)`, tenant, t.Name, t.ZPL)
		return err
	}
	res, err := a.db.Exec(`UPDATE templates SET name=?,zpl=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND tenant_id=?`, t.Name, t.ZPL, t.ID, tenant)
	if err == nil {
		if n, _ := res.RowsAffected(); n == 0 {
			err = sql.ErrNoRows
		}
	}
	return err
}
func (a *App) deleteTemplate(tenant, id int64) error {
	_, err := a.db.Exec(`DELETE FROM templates WHERE id=? AND tenant_id=?`, id, tenant)
	return err
}

// ---- products ----
const prodCols = `id,name,COALESCE(template_id,0),barcode,origin,ingredients,weight,weight_unit,price,shelf_life_days`

func scanProduct(s interface{ Scan(...any) error }) (Product, error) {
	var p Product
	err := s.Scan(&p.ID, &p.Name, &p.TemplateID, &p.Barcode, &p.Origin, &p.Ingredients, &p.Weight, &p.WeightUnit, &p.Price, &p.ShelfLifeDays)
	return p, err
}
func (a *App) listProducts(tenant int64) ([]Product, error) {
	rows, err := a.db.Query(`SELECT `+prodCols+` FROM products WHERE tenant_id=? ORDER BY name`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (a *App) getProduct(tenant, id int64) (Product, error) {
	return scanProduct(a.db.QueryRow(`SELECT `+prodCols+` FROM products WHERE id=? AND tenant_id=?`, id, tenant))
}
func (a *App) saveProduct(tenant int64, p Product) error {
	if p.TemplateID != 0 { // a product may only point at its own business's template
		if _, err := a.getTemplate(tenant, p.TemplateID); err != nil {
			p.TemplateID = 0
		}
	}
	if p.ID == 0 {
		_, err := a.db.Exec(`INSERT INTO products(tenant_id,name,template_id,barcode,origin,ingredients,weight,weight_unit,price,shelf_life_days)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, tenant, p.Name, nullID(p.TemplateID), p.Barcode, p.Origin, p.Ingredients, p.Weight, p.WeightUnit, p.Price, p.ShelfLifeDays)
		return err
	}
	res, err := a.db.Exec(`UPDATE products SET name=?,template_id=?,barcode=?,origin=?,ingredients=?,weight=?,weight_unit=?,price=?,shelf_life_days=?
		WHERE id=? AND tenant_id=?`, p.Name, nullID(p.TemplateID), p.Barcode, p.Origin, p.Ingredients, p.Weight, p.WeightUnit, p.Price, p.ShelfLifeDays, p.ID, tenant)
	if err == nil {
		if n, _ := res.RowsAffected(); n == 0 {
			err = sql.ErrNoRows
		}
	}
	return err
}
func (a *App) deleteProduct(tenant, id int64) error {
	_, err := a.db.Exec(`DELETE FROM products WHERE id=? AND tenant_id=?`, id, tenant)
	return err
}

// ---- print jobs ----
func (a *App) logJob(tenant, uid, pid, tid int64, qty int, batch string) {
	a.db.Exec(`INSERT INTO print_jobs(tenant_id,user_id,product_id,template_id,qty,batch) VALUES(?,?,?,?,?,?)`,
		tenant, uid, nullID(pid), nullID(tid), qty, batch)
}
func (a *App) recentJobs(tenant int64, n int) ([]Job, error) {
	rows, err := a.db.Query(`SELECT j.created_at, COALESCE(p.name,'(deleted product)'), j.qty, j.batch, COALESCE(u.email,'')
		FROM print_jobs j LEFT JOIN products p ON p.id=j.product_id AND p.tenant_id=j.tenant_id LEFT JOIN users u ON u.id=j.user_id
		WHERE j.tenant_id=? ORDER BY j.id DESC LIMIT ?`, tenant, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.When, &j.Product, &j.Qty, &j.Batch, &j.Email); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
