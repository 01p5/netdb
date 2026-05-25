package store

import (
	"context"
	"errors"
	"testing"
)

func TestNormalizeMAC(t *testing.T) {
	cases := []struct {
		in, out string
		ok      bool
	}{
		{"AA:BB:CC:DD:EE:FF", "aa:bb:cc:dd:ee:ff", true},
		{"aa-bb-cc-dd-ee-ff", "aa:bb:cc:dd:ee:ff", true},
		{"aabb.ccdd.eeff", "aa:bb:cc:dd:ee:ff", true},
		{"  aabbccddeeff  ", "aa:bb:cc:dd:ee:ff", true},
		{"aabbccddee", "", false},
		{"aabbccddeeggg", "", false},
		{"zzzzzzzzzzzz", "", false},
	}
	for _, c := range cases {
		got, err := NormalizeMAC(c.in)
		if c.ok {
			if err != nil {
				t.Errorf("%q: unexpected error: %v", c.in, err)
				continue
			}
			if got != c.out {
				t.Errorf("%q: got %q want %q", c.in, got, c.out)
			}
		} else if err == nil {
			t.Errorf("%q: expected error", c.in)
		}
	}
}

func TestNICsCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	h, _ := st.CreateHost(ctx, "h1", "", "")

	n, err := st.CreateNIC(ctx, h.ID, "AA-BB-CC-DD-EE-FF", "wan")
	if err != nil {
		t.Fatalf("CreateNIC: %v", err)
	}
	if n.MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("MAC not normalized on insert: %q", n.MAC)
	}

	got, err := st.GetNIC(ctx, n.ID)
	if err != nil || got.MAC != n.MAC {
		t.Errorf("GetNIC: %v %+v", err, got)
	}

	list, err := st.ListNICsByHost(ctx, h.ID)
	if err != nil || len(list) != 1 {
		t.Errorf("ListNICsByHost: %v len=%d", err, len(list))
	}

	if err := st.DeleteNIC(ctx, n.ID); err != nil {
		t.Fatalf("DeleteNIC: %v", err)
	}
	if _, err := st.GetNIC(ctx, n.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
}

func TestCreateNICRejectsBadMAC(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	h, _ := st.CreateHost(ctx, "h", "", "")
	if _, err := st.CreateNIC(ctx, h.ID, "garbage", ""); err == nil {
		t.Fatal("expected MAC validation error")
	}
}

func TestDeleteNICNotFound(t *testing.T) {
	st := newTestStore(t)
	if err := st.DeleteNIC(context.Background(), 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v want ErrNotFound", err)
	}
}

func TestNICCascadesOnHostDelete(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	h, _ := st.CreateHost(ctx, "x", "", "")
	if _, err := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:11", ""); err != nil {
		t.Fatalf("CreateNIC: %v", err)
	}
	if err := st.DeleteHost(ctx, h.ID); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}
	nics, err := st.ListNICsByHost(ctx, h.ID)
	if err != nil {
		t.Fatalf("ListNICsByHost: %v", err)
	}
	if len(nics) != 0 {
		t.Errorf("expected cascade: %d NICs left", len(nics))
	}
}
