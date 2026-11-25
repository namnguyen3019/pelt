package testshop

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func do(t *testing.T, h http.Handler, method, path, body, token string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestShopJourney(t *testing.T) {
	s := New()
	if code, _ := do(t, s, "POST", "/login", `{"username": "admin", "password": "x"}`, ""); code != 401 {
		t.Errorf("non-user login = %d, want 401", code)
	}
	code, out := do(t, s, "POST", "/login", `{"username": "user1", "password": "x"}`, "")
	tok, _ := out["token"].(string)
	if code != 200 || tok == "" {
		t.Fatalf("login = %d %v", code, out)
	}
	_, other := do(t, s, "POST", "/login", `{"username": "user2", "password": "x"}`, "")
	tok2 := other["token"].(string)

	if code, _ := do(t, s, "POST", "/cart", `{"product_id": 1}`, ""); code != 401 {
		t.Errorf("cart without a session = %d, want 401", code)
	}
	if code, _ := do(t, s, "POST", "/checkout", ``, tok); code != 409 {
		t.Errorf("checkout of an empty cart = %d, want 409", code)
	}
	if code, _ := do(t, s, "POST", "/cart", `{"product_id": "99"}`, tok); code != 404 {
		t.Errorf("unknown product = %d, want 404", code)
	}
	if code, _ := do(t, s, "POST", "/cart", `{"product_id": "3", "qty": 2}`, tok); code != 200 {
		t.Errorf("add to cart = %d", code)
	}
	code, out = do(t, s, "POST", "/checkout", ``, tok)
	oid, _ := out["order_id"].(string)
	if code != 201 || oid == "" {
		t.Fatalf("checkout = %d %v", code, out)
	}
	if code, _ := do(t, s, "GET", "/orders/"+oid, ``, tok); code != 200 {
		t.Errorf("own order = %d, want 200", code)
	}
	if code, _ := do(t, s, "GET", "/orders/"+oid, ``, tok2); code != 404 {
		t.Errorf("someone else's order = %d, want 404", code)
	}
	code, out = do(t, s, "GET", "/products?limit=3", ``, "")
	if items, _ := out["items"].([]any); code != 200 || len(items) != 3 {
		t.Errorf("products = %d %v", code, out)
	}
	if code, _ := do(t, s, "GET", "/products/0", ``, ""); code != 404 {
		t.Errorf("product 0 = %d, want 404", code)
	}
}
