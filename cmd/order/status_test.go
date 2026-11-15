package main

import (
	"testing"

	"fooddelivery/internal/events"
)

func TestNextMovesForwardOnly(t *testing.T) {
	cases := []struct {
		current, target, want string
		changed               bool
	}{
		{StatusCreated, StatusPaid, StatusPaid, true},
		{StatusPaid, StatusCooking, StatusCooking, true},
		{StatusCreated, StatusCooking, StatusCooking, true}, // событие кухни пришло раньше оплаты
		{StatusCooking, StatusPaid, StatusCooking, false},   // запоздавшая оплата не откатывает
		{StatusReady, StatusDelivering, StatusDelivering, true},
		{StatusDelivered, StatusReady, StatusDelivered, false},
		{StatusCreated, StatusDeclined, StatusDeclined, true},
		{StatusCancelled, StatusPaid, StatusCancelled, false},
		{StatusPaid, "", StatusPaid, false},
	}
	for _, c := range cases {
		got, changed := Next(c.current, c.target)
		if got != c.want || changed != c.changed {
			t.Errorf("Next(%s,%s)=%s,%v want %s,%v", c.current, c.target, got, changed, c.want, c.changed)
		}
	}
}

func TestCancellable(t *testing.T) {
	for st, want := range map[string]bool{StatusCreated: true, StatusPaid: true, StatusCooking: false, StatusDelivered: false} {
		if Cancellable(st) != want {
			t.Errorf("Cancellable(%s) != %v", st, want)
		}
	}
}

func TestTargetStatus(t *testing.T) {
	ev, _ := events.New(events.CourierAssigned, "courier", 1, events.DeliveryData{CourierName: "Анна"})
	if st, note := targetStatus(ev); st != StatusDelivering || note != "Анна" {
		t.Fatalf("got %s %s", st, note)
	}
	ev, _ = events.New(events.PaymentDeclined, "payment", 1, events.PaymentData{Reason: "amount exceeds limit"})
	if st, note := targetStatus(ev); st != StatusDeclined || note == "" {
		t.Fatalf("got %s %s", st, note)
	}
	ev, _ = events.New(events.DeliveryNoCouriers, "courier", 1, events.DeliveryData{})
	if st, note := targetStatus(ev); st != "" || note == "" {
		t.Fatalf("waiting event must only add a note, got %q %q", st, note)
	}
}

func TestCreateRequestValidate(t *testing.T) {
	ok := createRequest{RestaurantID: 1, Items: []orderItem{{ItemID: 2, Qty: 1}}, Address: "ул. Садовая, 1"}
	if err := ok.validate(); err != nil {
		t.Fatal(err)
	}
	bad := []createRequest{
		{Items: ok.Items, Address: ok.Address},
		{RestaurantID: 1, Address: ok.Address},
		{RestaurantID: 1, Items: []orderItem{{ItemID: 2, Qty: 0}}, Address: ok.Address},
		{RestaurantID: 1, Items: ok.Items, Address: "x"},
	}
	for i, b := range bad {
		if b.validate() == nil {
			t.Errorf("case %d must be rejected", i)
		}
	}
}
