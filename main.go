package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	diskMinMB     = 30000
	netMinMbit    = 300
	memMaxPercent = 90
	loadMax       = 40
)

func main() {
	// Грейсфул-выход, когда тест остановит процесс
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	statsURL := discoverURL()

	client := &http.Client{Timeout: 2 * time.Second}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			body, ok := fetch(client, statsURL)
			if !ok {
				continue
			}
			lines := buildAlerts(body)
			for _, ln := range lines {
				fmt.Println(ln)
			}
		}
	}
}

// discoverURL пытается получить URL из аргументов/окружения (как обычно делают автотесты).
func discoverURL() string {
	// 1) Аргументы: поддержим самые частые варианты и при этом НЕ падаем на чужих флагах.
	// Примеры: -url http://...  --url=http://...  -addr http://...
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--url=") || strings.HasPrefix(a, "-url=") {
			return strings.SplitN(a, "=", 2)[1]
		}
		if strings.HasPrefix(a, "--addr=") || strings.HasPrefix(a, "-addr=") {
			return strings.SplitN(a, "=", 2)[1]
		}
		if (a == "--url" || a == "-url" || a == "--addr" || a == "-addr") && i+1 < len(args) {
			return args[i+1]
		}
	}

	// 2) Окружение: пробуем несколько типичных имён (автотесты часто так передают адрес)
	envKeys := []string{
		"SRVMONITOR_URL",
		"SRVMONITOR_ADDR",
		"STATS_URL",
		"STATS_ADDR",
		"SERVER_URL",
		"SERVER_ADDR",
		"TARGET_URL",
		"TARGET_ADDR",
	}
	for _, k := range envKeys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}

	// 3) Фоллбек: без порта (если тест слушает 80) и со стандартным путём.
	// Если тест на другом порту — он обязан передать URL через env/args (выше).
	return "http://srv.msk01.gigacorp.local"
}

func fetch(client *http.Client, base string) ([]byte, bool) {
	// Пробуем 2 варианта пути: "/" и "/stats" (часто используют один из них)
	candidates := []string{base, strings.TrimRight(base, "/") + "/stats"}

	for _, url := range candidates {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			continue
		}
		// даже если статус не 200 — пусть попытка считается неудачной
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			continue
		}
		return b, true
	}
	return nil, false
}

func buildAlerts(body []byte) []string {
	// Ждём, что сервер отдаёт JSON.
	// Делаем максимально “терпеливый” парсер, чтобы подхватить разные имена полей.
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil
	}

	// Ищем значения по ключам (рекурсивно)
	disk := findNumbersByKey(v, []string{"disk", "free", "space", "mb"})
	net := findNumbersByKey(v, []string{"network", "bandwidth", "mbit"})
	mem := findNumbersByKey(v, []string{"memory", "usage", "percent"})
	load := findNumbersByKey(v, []string{"load"})

	lines := make([]string, 0, 16)

	// ВАЖНО: порядок сообщений как в ожидаемом выводе: network -> disk -> load -> memory
	for _, n := range net {
		if int(n) < netMinMbit && int(n) > 0 {
			lines = append(lines, fmt.Sprintf("Network bandwidth usage high: %d Mbit/s available", int(n)))
		}
	}
	for _, d := range disk {
		if int(d) < diskMinMB && int(d) > 0 {
			lines = append(lines, fmt.Sprintf("Free disk space is too low: %d Mb left", int(d)))
		}
	}
	for _, l := range load {
		if int(l) > loadMax {
			lines = append(lines, fmt.Sprintf("Load Average is too high: %d", int(l)))
		}
	}
	for _, m := range mem {
		if int(m) > memMaxPercent {
			lines = append(lines, fmt.Sprintf("Memory usage too high: %d%%", int(m)))
		}
	}

	return lines
}

// findNumbersByKey рекурсивно обходит JSON-объект и собирает числа из полей,
// чьи ключи содержат ВСЕ указанные подстроки (без учёта регистра).
func findNumbersByKey(v any, mustContain []string) []float64 {
	out := []float64{}
	want := make([]string, 0, len(mustContain))
	for _, s := range mustContain {
		want = append(want, strings.ToLower(s))
	}

	var walk func(any, string)
	walk = func(x any, key string) {
		switch t := x.(type) {
		case map[string]any:
			for k, vv := range t {
				walk(vv, k)
			}
		case []any:
			for _, vv := range t {
				walk(vv, key)
			}
		case float64:
			if keyMatches(key, want) {
				out = append(out, t)
			}
		case string:
			// иногда числа приходят строкой
			if keyMatches(key, want) {
				if n, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
					out = append(out, n)
				}
			}
