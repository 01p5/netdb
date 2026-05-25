package dnsprov

import "testing"

func TestRecordKey(t *testing.T) {
	r := Record{Name: "a.example", Type: "A", Value: "10.0.0.1", TTL: 300}
	if got := r.Key(); got != "a.example|A|10.0.0.1" {
		t.Errorf("Key: %q", got)
	}
	// TTL change shouldn't change the key.
	r2 := r
	r2.TTL = 60
	if r.Key() != r2.Key() {
		t.Error("TTL must not affect Key")
	}
}
