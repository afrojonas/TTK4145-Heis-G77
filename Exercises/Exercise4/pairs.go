package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	udpAddr        = "127.0.0.1:30000"
	heartbeatEvery = 600 * time.Millisecond
	readTimeout    = 900 * time.Millisecond
	missedLimit    = 5
)

func main() {
	role := os.Getenv("ROLE")
	if role == "backup" {
		runBackup()
		return
	}
	runPrimary(0) // acked=0 => start printing from 1
}

// PRIMARY: prints numbers and sends heartbeat messages "ACKED:<n>"
func runPrimary(startAcked int) {
	// Spawn a backup child process (same program, ROLE=backup).
	_ = spawnBackupChild()

	raddr, err := net.ResolveUDPAddr("udp", udpAddr)
	if err != nil {
		panic(err)
	}

	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	acked := startAcked
	next := acked + 1

	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()

	fmt.Printf("[P] started (PID=%d, acked=%d)\n", os.Getpid(), acked)

	for range ticker.C {
		// Print next number
		fmt.Printf("[P][PID=%d] %d\n", os.Getpid(), next)

		// Update acked AFTER printing
		acked = next
		next++

		// Send heartbeat with last acked
		msg := fmt.Sprintf("ACKED:%d", acked)
		_, _ = conn.Write([]byte(msg))
	}
}

// BACKUP: listens for heartbeats; on missedLimit timeouts, becomes primary.
func runBackup() {
	laddr, err := net.ResolveUDPAddr("udp", udpAddr)
	if err != nil {
		panic(err)
	}

	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		fmt.Printf("[B][PID=%d] failed to bind %s: %v\n", os.Getpid(), udpAddr, err)
		os.Exit(1)
	}
	defer conn.Close()

	fmt.Printf("[B] started (PID=%d), listening for primary heartbeats\n", os.Getpid())

	acked := 0
	missed := 0
	buf := make([]byte, 1024)

	for {
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			missed++
			fmt.Printf("[B][PID=%d] missed heartbeat (%d/%d)\n", os.Getpid(), missed, missedLimit)

			if missed >= missedLimit {
				fmt.Printf("[B] takeover! PID=%d last acked=%d -> becoming primary\n", os.Getpid(), acked)


				_ = conn.Close()

				runPrimary(acked)
				return
			}
			continue
		}

		missed = 0
		msg := strings.TrimSpace(string(buf[:n]))
		if strings.HasPrefix(msg, "ACKED:") {
			numStr := strings.TrimPrefix(msg, "ACKED:")
			if v, e := strconv.Atoi(numStr); e == nil {
				if v > acked {
					acked = v
				}
			}
		}
	}
}

func spawnBackupChild() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}

	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), "ROLE=backup")

	// Send backup output to same terminal
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Start()
}

