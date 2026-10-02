package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscoverRejeitaCIDRInvalido(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/discover", strings.NewReader(`{"cidr":"rede-de-casa"}`))
	rec := httptest.NewRecorder()
	s.handleDiscover(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, esperado 400; corpo: %s", rec.Code, rec.Body)
	}
}

func TestDiscoverRejeitaPortaInvalida(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/discover", strings.NewReader(`{"cidr":"127.0.0.1/32","ports":"0"}`))
	rec := httptest.NewRecorder()
	s.handleDiscover(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, esperado 400; corpo: %s", rec.Code, rec.Body)
	}
}
