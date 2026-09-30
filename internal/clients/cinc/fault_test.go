// -------------------------------------------------------------------------------
// Fault Injection - The Answers A Working Server Will Not Give
//
// Author: Alex Freidah
//
// Everything about how pins behave is covered against the real server in
// pin_test.go. What remains is the handling of answers a healthy server never
// produces: a refusing server, and an item that exists while carrying nothing
// worth reading. Those are reached by standing in front of the client with
// something that gives the answer on purpose.
//
// This is a fault injector, not a model of the server: it asserts nothing about
// which requests were made, and any behaviour that a real server can be put
// into belongs next door instead.
// -------------------------------------------------------------------------------

package cinc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// faulty returns a client talking to a server that answers every data bag
// request with status and body.
func faulty(t *testing.T, status int, body any) *Cinc {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Ops-Server-API-Version", `{"min_version":"0","max_version":"2"}`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := New(Options{
		ServerURL:  srv.URL + "/organizations/" + testOrg,
		ClientName: "hashi-upgrade",
		KeyPath:    testKeyPath(t),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// An item read by nothing and converging to nothing is reported rather than
// passing as unpinned, because an unpinned tool and a broken pin call for
// different actions.
func TestPinOfAMalformedItem(t *testing.T) {
	cases := map[string]any{
		"no version field": map[string]any{"id": "vault"},
		"empty version":    map[string]any{"id": "vault", "version": ""},
		"version not text": map[string]any{"id": "vault", "version": 115},
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := faulty(t, http.StatusOK, body).Pin(t.Context(), "vault"); err == nil {
				t.Error("expected an error rather than an empty pin")
			}
		})
	}
}

func TestPinAgainstARefusingServer(t *testing.T) {
	c := faulty(t, http.StatusInternalServerError, map[string]any{"error": []string{"boom"}})

	if _, err := c.Pin(t.Context(), "nomad"); err == nil {
		t.Error("expected an error for a failing server")
	}
}

// Every write path reports rather than continuing: the pin is what makes an
// upgrade real, so a run that could not set it must not proceed as though it
// had.
func TestSetPinAgainstARefusingServer(t *testing.T) {
	cases := map[string]int{
		"refused":     http.StatusForbidden,
		"unavailable": http.StatusInternalServerError,
	}

	for name, status := range cases {
		t.Run(name, func(t *testing.T) {
			c := faulty(t, status, map[string]any{"error": []string{"nope"}})

			if err := c.SetPin(t.Context(), "nomad", "2.0.6"); err == nil {
				t.Error("expected an error when the write is refused")
			}
		})
	}
}
