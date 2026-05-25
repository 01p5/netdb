package store

import (
	"context"
	"errors"
	"testing"
)

func TestProvidersCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	p, err := st.CreateProvider(ctx, "cf-main", "cloudflare", "", true)
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if p.ConfigJSON != "{}" {
		t.Errorf("expected ConfigJSON default '{}', got %q", p.ConfigJSON)
	}
	if !p.Enabled {
		t.Error("Enabled should be true")
	}

	got, err := st.GetProvider(ctx, p.ID)
	if err != nil || got.Name != "cf-main" {
		t.Errorf("GetProvider: %v %+v", err, got)
	}

	list, err := st.ListProviders(ctx)
	if err != nil || len(list) != 1 {
		t.Errorf("ListProviders: %v len=%d", err, len(list))
	}

	if err := st.DeleteProvider(ctx, p.ID); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	if _, err := st.GetProvider(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
}

func TestProvidersValidation(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateProvider(ctx, "weird", "wrong-kind", "", true); err == nil {
		t.Fatal("expected kind validation error")
	}
	if err := st.DeleteProvider(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
}
