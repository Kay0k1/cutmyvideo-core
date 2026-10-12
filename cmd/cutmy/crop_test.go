package main

import "testing"

func TestParseCrop(t *testing.T) {
	crop, err := parseCrop("0.25, 0, 0.5, 1")
	if err != nil || crop.X != .25 || crop.Width != .5 || crop.Height != 1 {
		t.Fatalf("crop %+v: %v", crop, err)
	}
	if crop, err = parseCrop(""); err != nil || crop != nil {
		t.Fatalf("original crop %+v: %v", crop, err)
	}
	for _, value := range []string{"1,2,3", "NaN,0,1,1", "0,0,Inf,1", "0,0,0,1", "-.1,0,1,1", ".5,0,1,1"} {
		if _, err = parseCrop(value); err == nil {
			t.Fatalf("accepted invalid crop %q", value)
		}
	}
}
