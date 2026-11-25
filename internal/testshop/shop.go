// Package testshop is a tiny in-memory web shop used as a realistic target
// for Pelt's flow mode: log in, list products, add to a cart, check out and
// look up the order. It backs `testserver -app shop` and the flow tests.
//
// It is deliberately simple and not secure; it exists only to exercise
// sessions, cookies, tokens and values carried between requests.
package testshop

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// ProductCount is how many products the shop sells, with ids 1..ProductCount.
const ProductCount = 20

// Shop is an http.Handler serving the shop routes:
//
//	POST /login            {"username": "user...", "password": "..."} -> {"token": "..."} + session cookie
//	GET  /products?limit=N {"items": [{"id": 1, "name": "...", "price": 49.5}, ...]}
//	GET  /products/{id}    one product, 404 if unknown
//	GET  /cart             the session's cart (needs a session)
//	POST /cart             {"product_id": 3, "qty": 1} (needs a session)
//	POST /checkout         201 {"order_id": "..."}, 409 on an empty cart (needs a session)
//	GET  /orders/{id}      200 only for the session that placed the order, else 404
//
// A session is found from the "session" cookie or an "Authorization: Bearer
// <token>" header; both carry the same id.
type Shop struct {
	mux *http.ServeMux

	mu       sync.Mutex
	sessions map[string]*session // by session id
	orders   map[string]*order   // by order id
}

type session struct {
	user string
	cart map[int]int // product id -> quantity
}

type order struct {
	session string
	user    string
	items   map[int]int
}

type product struct {
	ID    int     `json:"id"`
	Name  string  `json:"name"`
	Price float64 `json:"price"`
}

// New returns an empty shop.
func New() *Shop {
	s := &Shop{
		mux:      http.NewServeMux(),
		sessions: make(map[string]*session),
		orders:   make(map[string]*order),
	}
	s.mux.HandleFunc("POST /login", s.login)
	s.mux.HandleFunc("GET /products", s.listProducts)
	s.mux.HandleFunc("GET /products/{id}", s.getProduct)
	s.mux.HandleFunc("GET /cart", s.withSession(s.getCart))
	s.mux.HandleFunc("POST /cart", s.withSession(s.addToCart))
	s.mux.HandleFunc("POST /checkout", s.withSession(s.checkout))
	s.mux.HandleFunc("GET /orders/{id}", s.withSession(s.getOrder))
	return s
}

func (s *Shop) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// Sessions reports how many sessions have logged in. Handy in tests.
func (s *Shop) Sessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// Orders reports how many orders have been placed. Handy in tests.
func (s *Shop) Orders() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.orders)
}

func productByID(id int) (product, bool) {
	if id < 1 || id > ProductCount {
		return product{}, false
	}
	return product{ID: id, Name: fmt.Sprintf("Paddle %d", id), Price: 40 + float64(id)*2.5}, true
}

func (s *Shop) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "body must be JSON with username and password")
		return
	}
	if !strings.HasPrefix(in.Username, "user") || in.Password == "" {
		writeError(w, http.StatusUnauthorized, "unknown user or wrong password")
		return
	}
	id := newID()
	s.mu.Lock()
	s.sessions[id] = &session{user: in.Username, cart: make(map[int]int)}
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{Name: "session", Value: id, Path: "/", HttpOnly: true})
	writeJSON(w, http.StatusOK, map[string]string{"token": id, "user": in.Username})
}

func (s *Shop) listProducts(w http.ResponseWriter, r *http.Request) {
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = min(n, ProductCount)
	}
	items := make([]product, 0, limit)
	for id := 1; id <= limit; id++ {
		p, _ := productByID(id)
		items = append(items, p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Shop) getProduct(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	p, ok := productByID(id)
	if err != nil || !ok {
		writeError(w, http.StatusNotFound, "no such product")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// withSession resolves the caller's session or answers 401.
func (s *Shop) withSession(h func(http.ResponseWriter, *http.Request, string, *session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := ""
		if c, err := r.Cookie("session"); err == nil {
			id = c.Value
		}
		if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			id = strings.TrimSpace(tok)
		}
		s.mu.Lock()
		sess := s.sessions[id]
		s.mu.Unlock()
		if sess == nil {
			writeError(w, http.StatusUnauthorized, "log in first")
			return
		}
		h(w, r, id, sess)
	}
}

func (s *Shop) getCart(w http.ResponseWriter, _ *http.Request, _ string, sess *session) {
	s.mu.Lock()
	items := cartItems(sess.cart)
	user := sess.user
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "items": items})
}

func (s *Shop) addToCart(w http.ResponseWriter, r *http.Request, _ string, sess *session) {
	// product_id and qty may be numbers or numeric strings, since flow
	// templates often produce strings.
	var in struct {
		ProductID json.Number `json:"product_id"`
		Qty       json.Number `json:"qty"`
	}
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "body must be JSON with product_id")
		return
	}
	id, err := strconv.Atoi(string(in.ProductID))
	if _, ok := productByID(id); err != nil || !ok {
		writeError(w, http.StatusNotFound, "no such product")
		return
	}
	qty := 1
	if in.Qty != "" {
		if qty, err = strconv.Atoi(string(in.Qty)); err != nil || qty < 1 {
			writeError(w, http.StatusBadRequest, "qty must be a positive number")
			return
		}
	}
	s.mu.Lock()
	sess.cart[id] += qty
	n := len(sess.cart)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]int{"lines": n})
}

func (s *Shop) checkout(w http.ResponseWriter, _ *http.Request, id string, sess *session) {
	s.mu.Lock()
	if len(sess.cart) == 0 {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "cart is empty")
		return
	}
	oid := newID()
	s.orders[oid] = &order{session: id, user: sess.user, items: sess.cart}
	sess.cart = make(map[int]int)
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]string{"order_id": oid})
}

func (s *Shop) getOrder(w http.ResponseWriter, r *http.Request, id string, _ *session) {
	s.mu.Lock()
	o := s.orders[r.PathValue("id")]
	var items []map[string]int
	if o != nil && o.session == id {
		items = cartItems(o.items)
	}
	s.mu.Unlock()
	if items == nil {
		writeError(w, http.StatusNotFound, "no such order")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"order_id": r.PathValue("id"), "user": o.user, "status": "paid", "items": items})
}

// cartItems lists a cart in product id order. Call with s.mu held.
func cartItems(cart map[int]int) []map[string]int {
	items := make([]map[string]int, 0, len(cart))
	for id := 1; id <= ProductCount; id++ {
		if q := cart[id]; q > 0 {
			items = append(items, map[string]int{"product_id": id, "qty": q})
		}
	}
	return items
}

func newID() string {
	var b [12]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
