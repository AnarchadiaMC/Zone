package game

import "testing"

func TestSafeZone(t *testing.T) {
	sz := CheckSafeZone(-211.3, -20.2, -145.8, "l01_escape")
	if sz == nil {
		t.Fatal("Expected safe zone, got nil")
	}
	if sz.ZoneID != "sz_cordon_rookie" {
		t.Fatalf("Expected sz_cordon_rookie, got %s", sz.ZoneID)
	}
}
