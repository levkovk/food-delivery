package main

// Статусы заказа в порядке продвижения по конвейеру.
const (
	StatusCreated    = "created"
	StatusPaid       = "paid"
	StatusCooking    = "cooking"
	StatusReady      = "ready"
	StatusDelivering = "delivering"
	StatusDelivered  = "delivered"
	StatusDeclined   = "declined"
	StatusCancelled  = "cancelled"
)

var rank = map[string]int{
	StatusCreated:    0,
	StatusPaid:       1,
	StatusCooking:    2,
	StatusReady:      3,
	StatusDelivering: 4,
	StatusDelivered:  5,
}

// Terminal сообщает, что заказ больше не меняет статус.
func Terminal(status string) bool {
	return status == StatusDelivered || status == StatusDeclined || status == StatusCancelled
}

// Next вычисляет новый статус заказа после события. События разных топиков
// могут прийти не по порядку, поэтому статус только продвигается вперёд:
// запоздавшее событие не откатывает заказ назад.
func Next(current, target string) (string, bool) {
	if Terminal(current) || target == "" {
		return current, false
	}
	if target == StatusDeclined || target == StatusCancelled {
		return target, true
	}
	if rank[target] > rank[current] {
		return target, true
	}
	return current, false
}

// Cancellable сообщает, можно ли ещё отменить заказ: после начала
// приготовления отмена не принимается.
func Cancellable(status string) bool {
	return status == StatusCreated || status == StatusPaid
}
