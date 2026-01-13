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
	diskMinMB     = 30000
	netMinMbit    = 300
	memMaxPercent = 90
	loadMax       = 40
)

func main() {
	// Аккуратно завершаемся, когда автотесты остановят процесс
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
	

