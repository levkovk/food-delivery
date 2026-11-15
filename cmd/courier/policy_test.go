package main

import (
	"reflect"
	"testing"
	"time"
)

func TestCandidateZones(t *testing.T) {
	if got := candidateZones("north", 0); !reflect.DeepEqual(got, []string{"north"}) {
		t.Fatalf("got %v", got)
	}
	got := candidateZones("north", fallbackAfter)
	if !reflect.DeepEqual(got, []string{"north", "center", "south"}) {
		t.Fatalf("got %v", got)
	}
}

func TestTravelTime(t *testing.T) {
	m := time.Second
	if travelTime("автомобиль", true, m) >= travelTime("пешком", true, m) {
		t.Fatal("car must be faster than walking")
	}
	if travelTime("велосипед", false, m) <= travelTime("велосипед", true, m) {
		t.Fatal("other zone must take longer")
	}
	if travelTime("дрон", true, m) != 20*m {
		t.Fatal("unknown transport must use default")
	}
}
