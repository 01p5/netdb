package store

import (
	"context"
	"errors"
	"testing"
)

func TestNormalizeIP(t *testing.T) {
	cases := []struct {
		in, out string
		ok      bool
	}{
		{"10.0.0.1", "10.0.0.1", true},
		{"2001:DB8::1", "2001:db8::1", true},
		{"::ffff:1.2.3.4", "::ffff:1.2.3.4", true},
		{"not-an-ip", "", false},
		{"10.0.0.256", "", false},
	}
	for _, c := range cases {
		got, err := NormalizeIP(c.in)
		if c.ok {
			if err != nil {
				t.Errorf("%q: %v", c.in, err)
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

func TestCreateIPAssignmentValidates(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	h, _ := st.CreateHost(ctx, "h", "", "")
	n, _ := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:01", "")

	if _, err := st.CreateIPAssignment(ctx, n.ID, nil, "10.0.0.1", "weird"); err == nil {
		t.Fatal("bad kind should reject")
	}
	if _, err := st.CreateIPAssignment(ctx, n.ID, nil, "not-an-ip", "static"); err == nil {
		t.Fatal("bad ip should reject")
	}
}

func TestIPsCRUDAndSubnetLink(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	h, _ := st.CreateHost(ctx, "h", "", "")
	n, _ := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:02", "")

	// Make a subnet, link the assignment to it through SubnetID.
	sb, err := st.CreateSubnet(ctx, modelSubnetCIDR("10.0.0.0/24"))
	if err != nil {
		t.Fatalf("CreateSubnet: %v", err)
	}
	sid := sb.ID
	a, err := st.CreateIPAssignment(ctx, n.ID, &sid, "10.0.0.7", "reserved")
	if err != nil {
		t.Fatalf("CreateIPAssignment: %v", err)
	}
	if a.SubnetID == nil || *a.SubnetID != sid {
		t.Errorf("expected SubnetID populated, got %+v", a.SubnetID)
	}
	if a.IP != "10.0.0.7" || a.Kind != "reserved" {
		t.Errorf("fields wrong: %+v", a)
	}

	list, err := st.ListIPsByNIC(ctx, n.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListIPsByNIC: %v len=%d", err, len(list))
	}
	if list[0].SubnetID == nil || *list[0].SubnetID != sid {
		t.Errorf("ListIPs lost SubnetID linkage: %+v", list[0])
	}

	if err := st.DeleteIPAssignment(ctx, a.ID); err != nil {
		t.Fatalf("DeleteIPAssignment: %v", err)
	}
	if err := st.DeleteIPAssignment(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete again: %v want ErrNotFound", err)
	}
}

func TestIPAssignmentUniqueOnNICIP(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	h, _ := st.CreateHost(ctx, "h", "", "")
	n, _ := st.CreateNIC(ctx, h.ID, "aa:bb:cc:dd:ee:03", "")

	if _, err := st.CreateIPAssignment(ctx, n.ID, nil, "10.0.0.10", "static"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := st.CreateIPAssignment(ctx, n.ID, nil, "10.0.0.10", "static"); err == nil {
		t.Fatal("expected UNIQUE(nic_id, ip) violation")
	}
}
