package api

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/identity"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

//go:embed ui/*
var localAssets embed.FS

const sessionCookie = "judgegrid_session"

type LocalWeb struct {
	store        identity.Store
	origin, host string
	secure       bool
}

func NewLocalWeb(store identity.Store, origin string) (*LocalWeb, error) {
	u, e := url.Parse(origin)
	if e != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") {
		return nil, fmt.Errorf("local origin must be an exact loopback HTTP(S) origin")
	}
	return &LocalWeb{store, origin, u.Host, u.Scheme == "https"}, nil
}
func (w *LocalWeb) session(r *http.Request) (identity.Session, error) {
	if r.Host != w.host {
		return identity.Session{}, domain.ErrUnauthorized
	}
	cookie, e := r.Cookie(sessionCookie)
	if e != nil {
		return identity.Session{}, domain.ErrUnauthorized
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	return w.store.Session(ctx, cookie.Value)
}
func (w *LocalWeb) Resolve(r *http.Request) (string, error) {
	s, e := w.session(r)
	return s.Principal, e
}
func (w *LocalWeb) validMutation(r *http.Request) bool {
	return r.Host == w.host && r.Header.Get("Origin") == w.origin
}
func (w *LocalWeb) Wrap(base http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(out http.ResponseWriter, r *http.Request) {
		if r.Host != w.host {
			http.NotFound(out, r)
			return
		}
		out.Header().Set("Content-Type", "text/html; charset=utf-8")
		b, _ := localAssets.ReadFile("ui/index.html")
		out.Write(b)
	})
	for _, asset := range []string{"app.js", "style.css"} {
		name := asset
		mux.HandleFunc("GET /assets/"+name, func(out http.ResponseWriter, r *http.Request) {
			if r.Host != w.host {
				http.NotFound(out, r)
				return
			}
			kind := "text/javascript"
			if strings.HasSuffix(name, ".css") {
				kind = "text/css"
			}
			out.Header().Set("Content-Type", kind+"; charset=utf-8")
			b, _ := localAssets.ReadFile("ui/" + name)
			out.Write(b)
		})
	}
	mux.HandleFunc("POST /api/v1/session", w.login)
	mux.HandleFunc("GET /api/v1/session", func(out http.ResponseWriter, r *http.Request) {
		s, e := w.session(r)
		if e != nil {
			apiError(out, e)
			return
		}
		respond(out, 200, s)
	})
	mux.HandleFunc("DELETE /api/v1/session", func(out http.ResponseWriter, r *http.Request) {
		s, e := w.session(r)
		if e != nil {
			apiError(out, e)
			return
		}
		if !w.validMutation(r) || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.CSRF)) != 1 {
			respond(out, 403, errorBody("forbidden", "same-origin session confirmation required"))
			return
		}
		c, _ := r.Cookie(sessionCookie)
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if e = w.store.Logout(ctx, c.Value); e != nil {
			apiError(out, e)
			return
		}
		http.SetCookie(out, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: w.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		out.WriteHeader(204)
	})
	mux.Handle("/", base)
	return http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		out.Header().Set("X-Content-Type-Options", "nosniff")
		out.Header().Set("Referrer-Policy", "no-referrer")
		out.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		out.Header().Set("Cache-Control", "no-store")
		if r.Method == "POST" && r.URL.Path == "/api/v1/submissions" {
			s, e := w.session(r)
			if e != nil {
				apiError(out, e)
				return
			}
			if !w.validMutation(r) || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.CSRF)) != 1 {
				respond(out, 403, errorBody("forbidden", "same-origin session confirmation required"))
				return
			}
		}
		mux.ServeHTTP(out, r)
	})
}
func (w *LocalWeb) login(out http.ResponseWriter, r *http.Request) {
	if !w.validMutation(r) {
		respond(out, 403, errorBody("forbidden", "same-origin login required"))
		return
	}
	r.Body = http.MaxBytesReader(out, r.Body, 2048)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if d.Decode(&input) != nil {
		apiError(out, domain.ErrInvalid)
		return
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		apiError(out, domain.ErrInvalid)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	token, s, e := w.store.Login(ctx, input.Username, input.Password)
	if e != nil {
		apiError(out, e)
		return
	}
	http.SetCookie(out, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: w.secure, SameSite: http.SameSiteStrictMode, MaxAge: 1800})
	respond(out, 200, s)
}
