// -------------------------------------------------------------------------------
// Version Pin Tests - Reading, Upserting, and the States In Between
//
// Author: Alex Freidah
//
// The cases that matter are the ones a real bag produces: a tool nobody has
// pinned, a bag that does not exist yet, and an item that exists while carrying
// nothing worth reading. The first two are ordinary and the third is a fault,
// so the difference between them is asserted rather than assumed.
// -------------------------------------------------------------------------------

package cinc

import (
	"net/http"
	"slices"
	"testing"
)

func TestPin(t *testing.T) {
	t.Run("reads the pinned version", func(t *testing.T) {
		s := serve(t, map[string]reply{
			"GET /data/versions/nomad": {http.StatusOK, map[string]any{"id": "nomad", "version": "1.10.5"}},
		})

		got, err := s.client(t).Pin(t.Context(), "nomad")
		if err != nil {
			t.Fatalf("Pin: %v", err)
		}
		if got != "1.10.5" {
			t.Errorf("Pin = %q, want 1.10.5", got)
		}
	})

	// A tool nobody has pinned is what a first upgrade starts from, so it reads
	// as empty rather than refusing to answer.
	t.Run("an absent item is empty, not an error", func(t *testing.T) {
		s := serve(t, nil)

		got, err := s.client(t).Pin(t.Context(), "consul")
		if err != nil {
			t.Fatalf("Pin against an empty server: %v", err)
		}
		if got != "" {
			t.Errorf("Pin = %q, want empty", got)
		}
	})

	// An item with no version is read by nothing and converges to nothing, so
	// it is reported instead of passing as unpinned.
	t.Run("an item carrying no version is an error", func(t *testing.T) {
		cases := map[string]any{
			"missing": map[string]any{"id": "vault"},
			"empty":   map[string]any{"id": "vault", "version": ""},
			"wrong":   map[string]any{"id": "vault", "version": 115},
		}
		for name, body := range cases {
			t.Run(name, func(t *testing.T) {
				s := serve(t, map[string]reply{
					"GET /data/versions/vault": {http.StatusOK, body},
				})
				if _, err := s.client(t).Pin(t.Context(), "vault"); err == nil {
					t.Error("expected an error rather than an empty pin")
				}
			})
		}
	})

	t.Run("a server error is an error", func(t *testing.T) {
		s := serve(t, map[string]reply{
			"GET /data/versions/nomad": {http.StatusInternalServerError, map[string]any{"error": []string{"boom"}}},
		})
		if _, err := s.client(t).Pin(t.Context(), "nomad"); err == nil {
			t.Error("expected an error for a failing server")
		}
	})

	t.Run("rejects an empty tool", func(t *testing.T) {
		s := serve(t, nil)
		if _, err := s.client(t).Pin(t.Context(), ""); err == nil {
			t.Error("expected an error for an empty tool")
		}
		if len(s.requests()) != 0 {
			t.Errorf("an empty tool reached the server: %v", s.requests())
		}
	})
}

func TestSetPin(t *testing.T) {
	// The steady state: the item is there and the update lands, without the
	// round trip a prior existence check would cost.
	t.Run("updates an existing item", func(t *testing.T) {
		s := serve(t, map[string]reply{
			"PUT /data/versions/nomad": {http.StatusOK, map[string]any{"id": "nomad", "version": "1.10.6"}},
		})

		if err := s.client(t).SetPin(t.Context(), "nomad", "1.10.6"); err != nil {
			t.Fatalf("SetPin: %v", err)
		}
		if got := s.requests(); !slices.Contains(got, "PUT /data/versions/nomad") {
			t.Errorf("requests %v, want a PUT of the item", got)
		}
		if bodies := s.bodies(); len(bodies) == 0 || bodies[0]["version"] != "1.10.6" {
			t.Errorf("bodies %v, want the version written", bodies)
		}
	})

	// Onboarding a tool: neither the bag nor the item exists, and both are
	// created without a manual bootstrap step.
	t.Run("creates the bag and the item when neither exists", func(t *testing.T) {
		s := serve(t, map[string]reply{
			"POST /data":          {http.StatusCreated, map[string]any{"uri": "versions"}},
			"POST /data/versions": {http.StatusCreated, map[string]any{"id": "consul", "version": "1.22.1"}},
		})

		if err := s.client(t).SetPin(t.Context(), "consul", "1.22.1"); err != nil {
			t.Fatalf("SetPin: %v", err)
		}
		want := []string{"PUT /data/versions/consul", "POST /data", "POST /data/versions"}
		if got := s.requests(); !slices.Equal(got, want) {
			t.Errorf("requests %v, want %v", got, want)
		}
	})

	// Another writer having created the bag first is the state this call
	// wanted, so the conflict is not a failure.
	t.Run("tolerates the bag already existing", func(t *testing.T) {
		s := serve(t, map[string]reply{
			"POST /data":          {http.StatusConflict, map[string]any{"error": []string{"exists"}}},
			"POST /data/versions": {http.StatusCreated, map[string]any{"id": "vault", "version": "1.21.0"}},
		})

		if err := s.client(t).SetPin(t.Context(), "vault", "1.21.0"); err != nil {
			t.Fatalf("SetPin with the bag already present: %v", err)
		}
	})

	t.Run("reports a bag that cannot be created", func(t *testing.T) {
		s := serve(t, map[string]reply{
			"POST /data": {http.StatusInternalServerError, map[string]any{"error": []string{"boom"}}},
		})
		if err := s.client(t).SetPin(t.Context(), "vault", "1.21.0"); err == nil {
			t.Error("expected an error when the bag cannot be created")
		}
	})

	t.Run("reports a failing create", func(t *testing.T) {
		s := serve(t, map[string]reply{
			"POST /data":          {http.StatusCreated, map[string]any{"uri": "versions"}},
			"POST /data/versions": {http.StatusInternalServerError, map[string]any{"error": []string{"boom"}}},
		})
		if err := s.client(t).SetPin(t.Context(), "vault", "1.21.0"); err == nil {
			t.Error("expected an error when the item cannot be created")
		}
	})

	t.Run("reports a refused update", func(t *testing.T) {
		s := serve(t, map[string]reply{
			"PUT /data/versions/nomad": {http.StatusForbidden, map[string]any{"error": []string{"nope"}}},
		})
		if err := s.client(t).SetPin(t.Context(), "nomad", "1.10.6"); err == nil {
			t.Error("expected an error when the update is refused")
		}
	})

	t.Run("rejects empty arguments", func(t *testing.T) {
		s := serve(t, nil)
		c := s.client(t)
		if err := c.SetPin(t.Context(), "", "1.10.5"); err == nil {
			t.Error("expected an error for an empty tool")
		}
		if err := c.SetPin(t.Context(), "nomad", ""); err == nil {
			t.Error("expected an error for an empty version")
		}
		if len(s.requests()) != 0 {
			t.Errorf("an empty argument reached the server: %v", s.requests())
		}
	})
}
