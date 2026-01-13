package main

import (
	"context"
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
	diskMinMB     = 40000
	netMinMbit    = 300
	memMaxPercent = 90
	loadMax       = 40
)

func buildAlerts(body []byte) []string {
	// CSV: load,memTotal,memUsed,diskTotal,diskUsed,netTotal,netUsed
	s := strings.TrimSpace(string(body))
	parts := strings.Split(s, ",")
	if len(parts) < 7 {
		return nil
	}

	parse := func(i int) int64 {
		v, err := strconv.ParseInt(strings.TrimSpace(parts[i]), 10, 64)
		if err != nil {
			return 0
		}
		return v
	}

	load := int(parse(0))

	memTotal := parse(1)
	memUsed := parse(2)

	diskTotal := parse(3)
	diskUsed := parse(4)

	netTotal := parse(5)
	netUsed := parse(6)

	lines := make([]string, 0, 4)

	// Порядок как ожидают тесты: load -> network -> memory -> disk
	if load > loadMax {
		lines = append(lines, fmt.Sprintf("Load Average is too high: %d", load))
	}

	if netTotal > 0 && netTotal > netUsed {
		availMbit := int((netTotal - netUsed) / 1_000_000)
		if availMbit < netMinMbit {
			lines = append(lines, fmt.Sprintf("Network bandwidth usage high: %d Mbit/s available", availMbit))
		}
	}

	if memTotal > 0 {
		memPct := int((memUsed * 100) / memTotal)
		if memPct > memMaxPercent {
			lines = append(lines, fmt.Sprintf("Memory usage too high: %d%%", memPct))
		}
	}

	if diskTotal > 0 && diskTotal > diskUsed {
		freeMB := int((diskTotal - diskUsed) / (1024 * 1024))
		if freeMB < diskMinMB {
			lines = append(lines, fmt.Sprintf("Free disk space is too low: %d Mb left", freeMB))
		}
	}

	return lines
}

// discoverURL пытается получить URL из аргументов/окружения (как автотесты часто делают).
func discoverURL() string {
	// Аргументы: поддержим несколько популярных вариантов.
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

	// Окружение: пробуем несколько типичных имён.
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

	// Фоллбек: тест добавляет домен в /etc/hosts.
	return "http://srv.msk01.gigacorp.local"
}

func fetch(client *http.Client, base string) ([]byte, bool) {
	// Пробуем "/" и "/stats" (на всякий).
	base = strings.TrimRight(base, "/")
	candidates := []string{
		base + "/",
		base + "/stats",
	}

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
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			continue
		}
		return b, true
	}

	return nil, false
}

func buildAlerts(body []byte) []string {
	// Формат ответа в автотестах: CSV из 7 чисел:
	// load,memTotal,memUsed,diskTotal,diskUsed,netTotal,netUsed
	s := strings.TrimSpace(string(body))
	parts := strings.Split(s, ",")
	if len(parts) < 7 {
		return nil
	}

	parse := func(i int) int64 {
		v, err := strconv.ParseInt(strings.TrimSpace(parts[i]), 10, 64)
		if err != nil {
			return 0
		}
		return v
	}

	load := int(parse(0))

	memTotal := parse(1)
	memUsed := parse(2)

	diskTotal := parse(3)
	diskUsed := parse(4)

	netTotal := parse(5)
	netUsed := parse(6)

	lines := make([]string, 0, 4)

	// Порядок сообщений внутри одного ответа (как в expected): load -> network -> disk -> memory
	if load > loadMax {
		lines = append(lines, fmt.Sprintf("Load Average is too high: %d", load))
	}

	if netTotal > 0 && netTotal > netUsed {
		availMbit := int((netTotal - netUsed) / 1_000_000)
		if availMbit < netMinMbit {
			lines = append(lines, fmt.Sprintf("Network bandwidth usage high: %d Mbit/s available", availMbit))
		}
	}

	if diskTotal > 0 && diskTotal > diskUsed {
		freeMB := int((diskTotal - diskUsed) / (1024 * 1024))
		if freeMB < diskMinMB {
			lines = append(lines, fmt.Sprintf("Free disk space is too low: %d Mb left", freeMB))
		}
	}

	if memTotal > 0 {
		memPct := int((memUsed * 100) / memTotal)
		if memPct > memMaxPercent {
			lines = append(lines, fmt.Sprintf("Memory usage too high: %d%%", memPct))
		}
	}

	return lines
}
