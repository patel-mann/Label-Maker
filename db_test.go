package main

import "testing"

func TestRebind(t *testing.T) {
	tests := []struct{ in, want string }{
		{"SELECT * FROM t WHERE a=? AND b=?", "SELECT * FROM t WHERE a=$1 AND b=$2"},
		{"UPDATE t SET a=? WHERE id=? AND tenant_id=?", "UPDATE t SET a=$1 WHERE id=$2 AND tenant_id=$3"},
		{"INSERT INTO t(a,b,c) VALUES(?,?,?)", "INSERT INTO t(a,b,c) VALUES($1,$2,$3)"},
		{"no placeholders here", "no placeholders here"},
	}
	for _, tt := range tests {
		if got := rebind(tt.in); got != tt.want {
			t.Errorf("rebind(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
