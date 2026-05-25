package store

import (
	"context"
	"testing"
)

func TestListReservationsOnlyReservedKind(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	h, _ := st.CreateHost(ctx, "fileserver", "", "")
	n1, _ := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:a1", "lan")
	n2, _ := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:a2", "wan")

	if _, err := st.CreateIPAssignment(ctx, n1.ID, nil, "10.0.0.10", "reserved"); err != nil {
		t.Fatalf("reserved: %v", err)
	}
	if _, err := st.CreateIPAssignment(ctx, n2.ID, nil, "10.0.0.20", "static"); err != nil {
		t.Fatalf("static: %v", err)
	}
	if err := st.UpsertDynamicLease(ctx, LeaseObserved{MAC: "ff:ee:dd:cc:bb:aa", IP: "10.0.0.30"}); err != nil {
		t.Fatalf("dynamic: %v", err)
	}

	res, err := st.ListReservations(ctx)
	if err != nil {
		t.Fatalf("ListReservations: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 reservation (kind=reserved), got %d: %+v", len(res), res)
	}
	r := res[0]
	if r.MAC != "aa:bb:cc:dd:ee:a1" || r.IP != "10.0.0.10" || r.HostName != "fileserver" || r.NICLabel != "lan" {
		t.Errorf("reservation fields wrong: %+v", r)
	}
}
