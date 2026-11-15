// Генератор нагрузки: входит как оператор и создаёт заказы через шлюз.
// В Kubernetes запускается по расписанию (CronJob), в Docker Compose - в цикле.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"time"

	"fooddelivery/internal/platform"
)

var addresses = []string{
	"ул. Садовая, 12, кв. 4", "пр. Мира, 101", "ул. Лесная, 7, офис 3",
	"Северный бульвар, 18", "ул. Южная, 25, подъезд 2", "наб. Речная, 3",
	"ул. Парковая, 44", "пер. Тихий, 9",
}

type client struct {
	base  string
	token string
	http  *http.Client
}

func (c *client) do(method, path string, body, out any) error {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body) //nolint:errcheck
	}
	req, err := http.NewRequest(method, c.base+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e) //nolint:errcheck
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func main() {
	log := platform.Logger("loadgen")
	c := &client{base: platform.Env("GATEWAY_URL", "http://gateway:8080"), http: &http.Client{Timeout: 10 * time.Second}}
	var login struct {
		Token string `json:"token"`
	}
	for attempt := 1; ; attempt++ {
		err := c.do("POST", "/api/auth/login", map[string]string{
			"username": platform.Env("LOADGEN_USER", "operator"),
			"password": platform.MustEnv(log, "LOADGEN_PASSWORD"),
		}, &login)
		if err == nil {
			break
		}
		if attempt == 30 {
			log.Error("login failed", "err", err)
			os.Exit(1)
		}
		log.Info("waiting for gateway", "err", err)
		time.Sleep(2 * time.Second)
	}
	c.token = login.Token
	count := platform.EnvInt("ORDERS_PER_BATCH", 5)
	interval := time.Duration(platform.EnvInt("INTERVAL_SECONDS", 0)) * time.Second
	for {
		created := batch(c, log, count)
		log.Info("batch finished", "created", created, "requested", count)
		if interval == 0 {
			return
		}
		time.Sleep(interval)
	}
}

func batch(c *client, log *slog.Logger, count int) int {
	var restaurants []struct {
		ID     int64 `json:"id"`
		Active bool  `json:"active"`
	}
	if err := c.do("GET", "/api/restaurants", nil, &restaurants); err != nil || len(restaurants) == 0 {
		log.Warn("no restaurants", "err", err)
		return 0
	}
	created := 0
	for i := 0; i < count; i++ {
		r := restaurants[rand.IntN(len(restaurants))]
		if !r.Active {
			continue
		}
		var menu []struct {
			ID        int64 `json:"id"`
			Available bool  `json:"available"`
		}
		if err := c.do("GET", fmt.Sprintf("/api/restaurants/%d/menu", r.ID), nil, &menu); err != nil || len(menu) == 0 {
			continue
		}
		var items []map[string]any
		for _, m := range menu {
			if m.Available && rand.IntN(3) > 0 {
				items = append(items, map[string]any{"item_id": m.ID, "qty": 1 + rand.IntN(3)})
			}
		}
		if len(items) == 0 {
			items = append(items, map[string]any{"item_id": menu[0].ID, "qty": 2})
		}
		var res struct {
			ID    int64 `json:"id"`
			Total int64 `json:"total"`
		}
		err := c.do("POST", "/api/orders", map[string]any{
			"restaurant_id": r.ID,
			"items":         items,
			"address":       addresses[rand.IntN(len(addresses))],
		}, &res)
		if err != nil {
			log.Warn("order rejected", "err", err)
			continue
		}
		created++
		log.Info("order created", "order", res.ID, "total", res.Total)
	}
	return created
}
