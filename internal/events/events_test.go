package events

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNewAndDecode(t *testing.T) {
	ev, err := New(OrderCreated, "order-service", 42, OrderCreatedData{RestaurantID: 3, Total: 990, Zone: "north"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.ID) != 24 || ev.OrderID != 42 || ev.Type != OrderCreated {
		t.Fatalf("bad envelope %+v", ev)
	}
	raw, _ := json.Marshal(ev)
	var back Event
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	var d OrderCreatedData
	if err := back.Decode(&d); err != nil || d.Total != 990 || d.Zone != "north" {
		t.Fatalf("decode: %+v %v", d, err)
	}
	other, _ := New(OrderCreated, "x", 42, nil)
	if other.ID == ev.ID {
		t.Fatal("event ids must be unique")
	}
}

func TestBrokers(t *testing.T) {
	got := Brokers(" kafka-0:9092, kafka-1:9092 ,,")
	if !reflect.DeepEqual(got, []string{"kafka-0:9092", "kafka-1:9092"}) {
		t.Fatalf("got %v", got)
	}
}
