package main

// Результат проверки платежа.
type decision struct {
	Approved bool
	Reason   string
}

// decide - упрощённая модель платёжного шлюза: заказ без суммы и заказ
// выше лимита отклоняются, остальные авторизуются.
func decide(amount, limit int64) decision {
	switch {
	case amount <= 0:
		return decision{Reason: "empty amount"}
	case amount > limit:
		return decision{Reason: "amount exceeds limit"}
	default:
		return decision{Approved: true}
	}
}
