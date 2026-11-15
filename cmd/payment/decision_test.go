package main

import "testing"

func TestDecide(t *testing.T) {
	if d := decide(1200, 5000); !d.Approved {
		t.Fatal("regular payment declined")
	}
	if d := decide(5000, 5000); !d.Approved {
		t.Fatal("payment equal to limit declined")
	}
	if d := decide(5001, 5000); d.Approved || d.Reason == "" {
		t.Fatal("payment over limit approved")
	}
	if d := decide(0, 5000); d.Approved {
		t.Fatal("empty payment approved")
	}
}
