package main

import (
	"reflect"
	"testing"
)

func TestParseVehicleRangeDefault(t *testing.T) {
	got := parseVehicleRange(defaultVehicleRange)
	if len(got) != 35 {
		t.Fatalf("default range has %d ids, want 35 (801..835)", len(got))
	}
	if got[0] != "801" || got[34] != "835" {
		t.Errorf("default range = %v..%v, want 801..835", got[0], got[34])
	}
}

func TestParseVehicleRangeMixed(t *testing.T) {
	// "bogus-x" is an invalid range and is skipped; the repeated "803" is deduped.
	got := parseVehicleRange(" 801-803, 900 ,905-906,bogus-x,,803")
	want := []string{"801", "802", "803", "900", "905", "906"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseVehicleRange = %v, want %v", got, want)
	}
}
