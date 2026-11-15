package main

import (
	"strings"

	"fooddelivery/internal/auth"
)

// rule связывает префикс пути и метод с сервисом и допустимыми ролями.
type rule struct {
	methods  string // методы через пробел, "*" - любой
	prefix   string
	upstream string
	roles    []string
}

var (
	all       = []string{auth.RoleAdmin, auth.RoleOperator}
	adminOnly = []string{auth.RoleAdmin}
)

// policy - таблица маршрутизации и прав доступа шлюза. Правила проверяются
// сверху вниз, срабатывает первое подходящее. Пути /internal не описаны,
// поэтому снаружи они недоступны.
var policy = []rule{
	{"GET", "/api/orders", "order", all},
	{"POST", "/api/orders", "order", all},
	{"GET", "/api/stats", "order", all},
	{"GET", "/api/restaurants", "restaurant", all},
	{"POST PUT DELETE", "/api/restaurants", "restaurant", adminOnly},
	{"PUT DELETE", "/api/menu", "restaurant", adminOnly},
	{"GET", "/api/couriers", "courier", all},
	{"PATCH", "/api/couriers", "courier", all},
	{"POST", "/api/couriers", "courier", adminOnly},
	{"GET", "/api/deliveries", "courier", all},
	{"GET", "/api/payments", "payment", adminOnly},
	{"GET", "/api/events/recent", "notification", all},
	{"GET", "/api/events/stream", "notification", all},
}

// decision - результат проверки запроса.
type decision int

const (
	allow decision = iota
	forbidden
	unknownRoute
)

// authorize находит правило для запроса и проверяет роль.
func authorize(method, path, role string) (string, decision) {
	for _, r := range policy {
		if !matchPrefix(path, r.prefix) {
			continue
		}
		if r.methods != "*" && !strings.Contains(" "+r.methods+" ", " "+method+" ") {
			continue
		}
		for _, allowed := range r.roles {
			if allowed == role {
				return r.upstream, allow
			}
		}
		return r.upstream, forbidden
	}
	return "", unknownRoute
}

// matchPrefix совпадает с самим префиксом и с путями под ним, но не с
// путями, которые лишь начинаются с тех же букв (/api/ordersX).
func matchPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// upstreamPath убирает префикс /api и переименовывает пути шлюза в пути
// сервисов (/api/events/stream -> /stream).
func upstreamPath(path string) string {
	p := strings.TrimPrefix(path, "/api")
	return strings.TrimPrefix(p, "/events")
}
