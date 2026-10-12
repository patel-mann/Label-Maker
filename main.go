package main

import (
	"bytes"
	"database/sql"
	"embed"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

//go:embed templates/*.html
var tplFS embed.FS

//go:embed static
var staticFS embed.FS

//go:embed seed/default.zpl
var defaultZPL string

var units = []string{"g", "kg", "oz", "lb", "ml", "L"}

type App struct {
	db            *sql.DB
	tpl           *template.Template
	allowRegister bool
}

type renderer struct{ a *App }

func (r *renderer) Render(w io.Writer, name string, data interface{}, _ echo.Context) error {
	return r.a.tpl.ExecuteTemplate(w, name, data)
}

// render executes a template, always supplying the current user and CSRF token.
func (a *App) render(c echo.Context, status int, name string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	data["User"] = c.Get("user")
	data["CSRF"] = c.Get("csrf")
	return c.Render(status, name, data)
}

func id64(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	dsn := envOr("DATABASE_URL", "postgres://labelprint:labelprint@localhost:5432/labelprint?sslmode=disable")
	db, err := openDB(dsn)
	if err != nil {
		log.Fatal(err)
	}
	a := &App{db: db, allowRegister: envOr("ALLOW_REGISTER", "true") != "false"}
	a.tpl = template.Must(template.ParseFS(tplFS, "templates/*.html"))

	e := echo.New()
	e.HideBanner = true
	e.Renderer = &renderer{a}
	e.Use(middleware.Recover(), middleware.Logger(), middleware.BodyLimit("8M"))
	e.Use(middleware.CSRFWithConfig(middleware.CSRFConfig{
		TokenLookup:    "header:X-CSRF-Token,form:_csrf",
		CookieSameSite: http.SameSiteLaxMode,
		CookiePath:     "/",
	}))

	sub, _ := fs.Sub(staticFS, "static")
	e.GET("/static/*", echo.WrapHandler(http.StripPrefix("/static/", http.FileServer(http.FS(sub)))))

	// public: sign in / create a business (rate limited per IP)
	limit := middleware.RateLimiter(middleware.NewRateLimiterMemoryStoreWithConfig(
		middleware.RateLimiterMemoryStoreConfig{Rate: 0.5, Burst: 8, ExpiresIn: 10 * time.Minute}))
	e.GET("/login", a.authPage("login"))
	e.GET("/register", a.authPage("register"))
	e.POST("/login", a.login, limit)
	e.POST("/register", a.register, limit)

	// signed in: one shell, tabs swapped in with htmx
	g := e.Group("", a.auth)
	g.POST("/logout", a.logout)
	g.GET("/", a.tab("print"))
	g.GET("/print", a.tab("print"))
	g.GET("/account", a.tab("account"))
	g.GET("/print/fields", a.printFields)
	g.POST("/print/render", a.printRender)
	g.POST("/account/password", a.changePassword)

	adm := g.Group("", adminOnly)
	adm.GET("/products", a.tab("products"))
	adm.GET("/products/form", a.productForm)
	adm.POST("/products", a.productSave)
	adm.DELETE("/products/:id", a.productDelete)

	adm.GET("/templates", a.tab("templates"))
	adm.GET("/templates/form", a.templateForm)
	adm.POST("/templates", a.templateSave)
	adm.DELETE("/templates/:id", a.templateDelete)

	adm.GET("/team", a.tab("team"))
	adm.GET("/team/form", a.teamForm)
	adm.POST("/team", a.teamSave)
	adm.DELETE("/team/:id", a.teamDelete)

	addr := envOr("ADDR", ":8080")
	log.Printf("listening on %s (registration open: %v)", addr, a.allowRegister)
	e.Logger.Fatal(e.Start(addr))
}

// tab renders one panel. htmx navigation gets just the app fragment; a direct load or
// history restore gets the full shell around it.
func (a *App) tab(name string) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := user(c)
		data := map[string]any{"Tab": name, "Units": units, "P": Product{WeightUnit: "g"}, "T": Template{}}
		var err error
		switch name {
		case "print":
			if data["Products"], err = a.listProducts(u.TenantID); err == nil {
				data["Jobs"], err = a.recentJobs(u.TenantID, 8)
			}
		case "products":
			if data["Products"], err = a.listProducts(u.TenantID); err == nil {
				data["Templates"], err = a.listTemplates(u.TenantID)
			}
		case "templates":
			data["Templates"], err = a.listTemplates(u.TenantID)
		case "team":
			data["Team"], err = a.listTeam(u.TenantID)
			data["U"] = User{}
		}
		if err != nil {
			return err
		}
		data["User"], data["CSRF"] = u, c.Get("csrf")
		var b bytes.Buffer
		if err := a.tpl.ExecuteTemplate(&b, "panel-"+name, data); err != nil {
			return err
		}
		data["Panel"] = template.HTML(b.String())

		c.Response().Header().Add("Vary", "HX-Request")
		page := "shell"
		if c.Request().Header.Get("HX-Request") == "true" && c.Request().Header.Get("HX-History-Restore-Request") != "true" {
			page = "app"
		}
		return c.Render(200, page, data)
	}
}

// ---------------- print ----------------

func (a *App) printFields(c echo.Context) error {
	u := user(c)
	pid := id64(c.QueryParam("product_id"))
	if pid == 0 {
		return c.NoContent(200)
	}
	p, err := a.getProduct(u.TenantID, pid)
	if err != nil {
		return c.String(404, "Product not found")
	}
	ts, err := a.listTemplates(u.TenantID)
	if err != nil {
		return err
	}
	now := time.Now()
	exp := ""
	if p.ShelfLifeDays > 0 {
		exp = now.AddDate(0, 0, p.ShelfLifeDays).Format("2006-01-02")
	}
	return a.render(c, 200, "print-fields", map[string]any{
		"P": p, "Templates": ts, "Units": units, "Prep": now.Format("2006-01-02"), "Exp": exp,
	})
}

// printRender fills the business's DB template and returns the raw ZPL; the browser sends it to the printer.
func (a *App) printRender(c echo.Context) error {
	u := user(c)
	tid := id64(c.FormValue("template_id"))
	t, err := a.getTemplate(u.TenantID, tid)
	if err != nil {
		return c.String(422, "Pick a template.")
	}
	qty, _ := strconv.Atoi(c.FormValue("qty"))
	l := Label{
		Name: c.FormValue("name"), Origin: c.FormValue("origin"), Ingredients: c.FormValue("ingredients"),
		Barcode: c.FormValue("barcode"), Weight: c.FormValue("weight"), WeightUnit: c.FormValue("weight_unit"),
		Price: c.FormValue("price"), Prep: c.FormValue("prep"), Exp: c.FormValue("exp"), Batch: c.FormValue("batch"), Qty: qty,
	}
	zpl, err := renderZPL(t.ZPL, l)
	if err != nil {
		return c.String(422, err.Error())
	}
	pid := id64(c.FormValue("product_id"))
	if _, err := a.getProduct(u.TenantID, pid); err != nil {
		pid = 0
	}
	a.logJob(u.TenantID, u.ID, pid, tid, qty, l.Batch)
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.Blob(200, "text/plain; charset=utf-8", []byte(zpl))
}

// ---------------- products ----------------

func (a *App) productForm(c echo.Context) error {
	u := user(c)
	p := Product{WeightUnit: "g"}
	if id := id64(c.QueryParam("id")); id != 0 {
		var err error
		if p, err = a.getProduct(u.TenantID, id); err != nil {
			return c.String(404, "Not found")
		}
	}
	ts, _ := a.listTemplates(u.TenantID)
	return a.render(c, 200, "product-form", map[string]any{"P": p, "Templates": ts, "Units": units})
}

func (a *App) productSave(c echo.Context) error {
	u := user(c)
	days, _ := strconv.Atoi(c.FormValue("shelf_life_days"))
	p := Product{
		ID: id64(c.FormValue("id")), Name: strings.TrimSpace(c.FormValue("name")), TemplateID: id64(c.FormValue("template_id")),
		Barcode: digits(c.FormValue("barcode")), Origin: strings.TrimSpace(c.FormValue("origin")),
		Ingredients: strings.TrimSpace(c.FormValue("ingredients")), Weight: strings.TrimSpace(c.FormValue("weight")),
		WeightUnit: c.FormValue("weight_unit"), Price: strings.TrimSpace(c.FormValue("price")), ShelfLifeDays: days,
	}
	if p.Name == "" {
		return c.String(422, "Name is required")
	}
	if _, err := upcData(p.Barcode); err != nil {
		return c.String(422, err.Error())
	}
	if err := a.saveProduct(u.TenantID, p); err != nil {
		return c.String(404, "Not found")
	}
	return a.productsFragment(c, "product-saved")
}

func (a *App) productDelete(c echo.Context) error {
	if err := a.deleteProduct(user(c).TenantID, id64(c.Param("id"))); err != nil {
		return err
	}
	return a.productsFragment(c, "product-rows")
}

func (a *App) productsFragment(c echo.Context, name string) error {
	u := user(c)
	ps, err := a.listProducts(u.TenantID)
	if err != nil {
		return err
	}
	ts, _ := a.listTemplates(u.TenantID)
	return a.render(c, 200, name, map[string]any{"Products": ps, "Templates": ts, "Units": units, "P": Product{WeightUnit: "g"}})
}

// ---------------- templates ----------------

func (a *App) templateForm(c echo.Context) error {
	t := Template{}
	if id := id64(c.QueryParam("id")); id != 0 {
		var err error
		if t, err = a.getTemplate(user(c).TenantID, id); err != nil {
			return c.String(404, "Not found")
		}
	}
	return a.render(c, 200, "template-form", map[string]any{"T": t})
}

func (a *App) templateSave(c echo.Context) error {
	t := Template{ID: id64(c.FormValue("id")), Name: strings.TrimSpace(c.FormValue("name")), ZPL: normalizeTemplate(strings.TrimSpace(c.FormValue("zpl")))}
	if t.Name == "" || !strings.Contains(t.ZPL, "^XZ") {
		return c.String(422, "A name and a ZPL body ending in ^XZ are required")
	}
	if err := a.saveTemplate(user(c).TenantID, t); err != nil {
		return c.String(422, "Could not save template")
	}
	return a.templatesFragment(c, "template-saved")
}

func (a *App) templateDelete(c echo.Context) error {
	if err := a.deleteTemplate(user(c).TenantID, id64(c.Param("id"))); err != nil {
		return err
	}
	return a.templatesFragment(c, "template-rows")
}

func (a *App) templatesFragment(c echo.Context, name string) error {
	ts, err := a.listTemplates(user(c).TenantID)
	if err != nil {
		return err
	}
	return a.render(c, 200, name, map[string]any{"Templates": ts, "T": Template{}})
}

// ---------------- team (admin manages non-admin users) ----------------

func (a *App) teamForm(c echo.Context) error {
	u := User{}
	if id := id64(c.QueryParam("id")); id != 0 {
		var err error
		if u, err = a.getTeamUser(user(c).TenantID, id); err != nil {
			return c.String(404, "Not found")
		}
	}
	return a.render(c, 200, "team-form", map[string]any{"U": u})
}

func (a *App) teamSave(c echo.Context) error {
	me := user(c)
	id := id64(c.FormValue("id"))
	email, ok := normEmail(c.FormValue("email"))
	pw := c.FormValue("password")
	switch {
	case !ok:
		return c.String(422, "Enter a valid email")
	case id == 0 && len(pw) < 8, id != 0 && pw != "" && len(pw) < 8:
		return c.String(422, "Password must be at least 8 characters")
	}
	hash := ""
	if pw != "" {
		hash = hashPassword(pw)
	}
	var err error
	if id == 0 {
		err = a.createTeamUser(me.TenantID, email, hash)
	} else {
		err = a.updateTeamUser(me.TenantID, id, email, hash)
	}
	if err != nil {
		if err == errEmailTaken {
			return c.String(422, "That email is already registered")
		}
		return c.String(404, "Not found")
	}
	return a.teamFragment(c, "team-saved")
}

func (a *App) teamDelete(c echo.Context) error {
	if err := a.deleteTeamUser(user(c).TenantID, id64(c.Param("id"))); err != nil {
		return err
	}
	return a.teamFragment(c, "team-rows")
}

func (a *App) teamFragment(c echo.Context, name string) error {
	us, err := a.listTeam(user(c).TenantID)
	if err != nil {
		return err
	}
	return a.render(c, 200, name, map[string]any{"Team": us, "U": User{}})
}
