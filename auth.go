package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

const (
	cookieName = "sid"
	pbkdfIter  = 120_000
	sessionTTL = 12 * time.Hour
)

// pbkdf2 (RFC 8018) with HMAC-SHA256, so no x/crypto dependency is needed.
func pbkdf2(pass, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, pass)
	hLen := prf.Size()
	var dk []byte
	for block := 1; len(dk) < keyLen; block++ {
		prf.Reset()
		prf.Write(salt)
		prf.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		dk = prf.Sum(dk)
		t := dk[len(dk)-hLen:]
		u := append([]byte(nil), t...)
		for i := 1; i < iter; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for j := range t {
				t[j] ^= u[j]
			}
		}
	}
	return dk[:keyLen]
}

func hashPassword(pw string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	h := pbkdf2([]byte(pw), salt, pbkdfIter, 32)
	return fmt.Sprintf("pbkdf2$%d$%s$%s", pbkdfIter, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(h))
}

func checkPassword(pw, stored string) bool {
	p := strings.Split(stored, "$")
	if len(p) != 4 || p[0] != "pbkdf2" {
		return false
	}
	iter, err := strconv.Atoi(p[1])
	salt, err2 := base64.RawStdEncoding.DecodeString(p[2])
	want, err3 := base64.RawStdEncoding.DecodeString(p[3])
	if err != nil || err2 != nil || err3 != nil {
		return false
	}
	return subtle.ConstantTimeCompare(pbkdf2([]byte(pw), salt, iter, len(want)), want) == 1
}

func randomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func user(c echo.Context) *User { return c.Get("user").(*User) }

func normEmail(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	addr, err := mail.ParseAddress(s)
	return s, err == nil && addr.Address == s
}

func (a *App) auth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if ck, err := c.Cookie(cookieName); err == nil {
			if u, err := a.userBySession(ck.Value); err == nil {
				c.Set("user", u)
				return next(c)
			}
		}
		if c.Request().Header.Get("HX-Request") == "true" {
			c.Response().Header().Set("HX-Redirect", "/login")
			return c.NoContent(http.StatusUnauthorized)
		}
		return c.Redirect(http.StatusFound, "/login")
	}
}

func adminOnly(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if user(c).Role != "admin" {
			return c.String(http.StatusForbidden, "Admins only")
		}
		return next(c)
	}
}

func (a *App) authPage(mode string) echo.HandlerFunc {
	return func(c echo.Context) error {
		if mode == "register" && !a.allowRegister {
			return c.String(http.StatusNotFound, "Registration is closed")
		}
		return a.render(c, 200, "auth", map[string]any{"Mode": mode, "Open": a.allowRegister})
	}
}

func (a *App) startSession(c echo.Context, uid int64) error {
	tok := randomToken(32)
	if err := a.createSession(tok, uid, sessionTTL); err != nil {
		return err
	}
	c.SetCookie(&http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true, Secure: c.IsTLS(),
		SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(sessionTTL)})
	return c.Redirect(http.StatusSeeOther, "/")
}

func (a *App) login(c echo.Context) error {
	email, _ := normEmail(c.FormValue("email"))
	u, err := a.userByEmail(email)
	if err != nil || !checkPassword(c.FormValue("password"), u.hash) {
		return a.render(c, http.StatusUnauthorized, "auth", map[string]any{"Mode": "login", "Open": a.allowRegister, "Error": "Wrong email or password", "Email": email})
	}
	return a.startSession(c, u.ID)
}

func (a *App) register(c echo.Context) error {
	if !a.allowRegister {
		return c.String(http.StatusNotFound, "Registration is closed")
	}
	biz := strings.TrimSpace(c.FormValue("business"))
	email, ok := normEmail(c.FormValue("email"))
	pw := c.FormValue("password")
	fail := func(msg string) error {
		return a.render(c, http.StatusUnprocessableEntity, "auth", map[string]any{"Mode": "register", "Open": true, "Error": msg, "Email": email, "Business": biz})
	}
	switch {
	case biz == "":
		return fail("Enter your business name")
	case !ok:
		return fail("Enter a valid email")
	case len(pw) < 8:
		return fail("Password must be at least 8 characters")
	}
	if err := a.registerTenant(biz, email, hashPassword(pw), normalizeTemplate(defaultZPL)); err != nil {
		return fail(err.Error())
	}
	u, err := a.userByEmail(email)
	if err != nil {
		return err
	}
	return a.startSession(c, u.ID)
}

func (a *App) logout(c echo.Context) error {
	if ck, err := c.Cookie(cookieName); err == nil {
		a.deleteSession(ck.Value)
	}
	c.SetCookie(&http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	return c.Redirect(http.StatusSeeOther, "/login")
}

// Anyone can change their own password; other sessions of that user are signed out.
func (a *App) changePassword(c echo.Context) error {
	u := user(c)
	if !checkPassword(c.FormValue("current"), u.hash) {
		return c.String(http.StatusUnprocessableEntity, "Current password is wrong")
	}
	if len(c.FormValue("new")) < 8 {
		return c.String(http.StatusUnprocessableEntity, "New password must be at least 8 characters")
	}
	ck, _ := c.Cookie(cookieName)
	if err := a.setPassword(u.ID, hashPassword(c.FormValue("new")), ck.Value); err != nil {
		return err
	}
	return c.HTML(200, `<span class="ok">Password updated</span>`)
}
