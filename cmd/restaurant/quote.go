package main

import (
	"errors"
	"fmt"
)

// Позиция меню, участвующая в расчёте.
type menuItem struct {
	ID        int64
	Name      string
	Price     int64
	Available bool
}

type quoteItem struct {
	ItemID int64 `json:"item_id"`
	Qty    int   `json:"qty"`
}

type quoteLine struct {
	ItemID int64  `json:"item_id"`
	Name   string `json:"name"`
	Qty    int    `json:"qty"`
	Price  int64  `json:"price"`
}

// Минимальная сумма заказа, рублей.
const minOrderTotal = 300

// calcQuote считает стоимость заказа по актуальному меню ресторана.
// Одинаковые позиции объединяются, недоступные позиции отклоняются.
func calcQuote(menu map[int64]menuItem, items []quoteItem) ([]quoteLine, int64, error) {
	if len(items) == 0 {
		return nil, 0, errors.New("empty order")
	}
	qty := map[int64]int{}
	var order []int64
	for _, it := range items {
		m, ok := menu[it.ItemID]
		if !ok {
			return nil, 0, fmt.Errorf("item %d is not on the menu of this restaurant", it.ItemID)
		}
		if !m.Available {
			return nil, 0, fmt.Errorf("item %q is not available now", m.Name)
		}
		if it.Qty < 1 {
			return nil, 0, errors.New("quantity must be positive")
		}
		if _, seen := qty[it.ItemID]; !seen {
			order = append(order, it.ItemID)
		}
		qty[it.ItemID] += it.Qty
	}
	var total int64
	lines := make([]quoteLine, 0, len(order))
	for _, id := range order {
		m := menu[id]
		lines = append(lines, quoteLine{ItemID: id, Name: m.Name, Qty: qty[id], Price: m.Price})
		total += m.Price * int64(qty[id])
	}
	if total < minOrderTotal {
		return nil, 0, fmt.Errorf("minimum order is %d", minOrderTotal)
	}
	return lines, total, nil
}
