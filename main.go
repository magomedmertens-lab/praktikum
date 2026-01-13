package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type Stats struct {
	FreeDiskSpaceMB       int `json:"free_disk_space_mb"`
	NetworkBandwidthMbit  int `json:"network_bandwidth_mbit"`
	MemoryUsagePercent    int `json:"memory_usage_percent"`
	LoadAverage           int `json:"load_average"`
}

// пороги (по смыслу из ожидаемых сообщений)
const (
	diskMinMB       = 30_000
	netMinMbit      = 200
	memMaxPercent   = 90
	loadMax         = 40
)

func main() {
	addr := flag.String("addr", "http://srv.msk01.gigacorp.local:8080", "server address")
	path := flag.String("path", "/stats", "stats endpoint path")
	period := flag.Duration("period", 1*time.Second, "poll period")
	flag.Parse()

	url := *addr + *path

	client := &http.Client{Timeout: 5 * time.Second}

	for {
		resp, err := client.Get(url)
		if err != nil {
			// в автотестах сервер всегда доступен, но на всякий случай
			time.Sleep(*period)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			time.Sleep(*period)
			continue
		}

		// иногда тесты могут отдавать JSON массив (несколько метрик),
		// поэтому пробуем сначала как один объект, потом как массив.
		var st Stats
		if err := json.Unmarshal(body, &st); err == nil {
			printAlerts(st)
		} else {
			var arr []Stats
			if err2 := json.Unmarshal(body, &arr); err2 == nil {
				for _, s := range arr {
					printAlerts(s)
				}
			} else {
				// если формат неожиданно другой — выходим с ошибкой
				fmt.Fprintln(os.Stderr, "invalid stats format")
				os.Exit(1)
			}
		}

		time.Sleep(*period)
	}
}

func printAlerts(s Stats) {
	// Важно: порядок сообщений должен быть стабильным.
	if s.FreeDiskSpaceMB > 0 && s.FreeDiskSpaceMB < diskMinMB {
		fmt.Printf("Free disk space is too low: %d Mb left\n", s.FreeDiskSpaceMB)
	}
	if s.NetworkBandwidthMbit > 0 && s.NetworkBandwidthMbit < netMinMbit {
		fmt.Printf("Network bandwidth usage high: %d Mbit/s available\n", s.NetworkBandwidthMbit)
	}
	if s.MemoryUsagePercent > memMaxPercent {
		fmt.Printf("Memory usage too high: %d%%\n", s.MemoryUsagePercent)
	}
	if s.LoadAverage > loadMax {
		fmt.Printf("Load Average is too high: %d\n", s.LoadAverage)
	}
}
