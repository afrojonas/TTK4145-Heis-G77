package main

import (
	"fmt"
	"net"
	"time"
)

//Server IP: 10.100.23.11

func main1() {

	listenPort := 20006

	addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf(":%d", listenPort))
	if err != nil {
		fmt.Println("Error resolving address:", err)
		return
	}

	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		fmt.Println("Error listening:", err)
		return
	}

	defer conn.Close()

	//Receiver
	go func() {
		//buffer for received messages
		buffer := make([]byte, 1024)
		fmt.Println("Listening on port 20006...")

		for {
			numBytesReceived, fromWho, err := conn.ReadFromUDP(buffer)
			if err != nil {
				fmt.Println("Error reading:", err)
				continue
			}

			// the buffer just contains a bunch of bytes, so you may have to explicitly convert it to a string
			message := string(buffer[:numBytesReceived])
			fmt.Printf("Received %d bytes from %s: %s\n", numBytesReceived, fromWho.String(), message)
			fmt.Printf("Raw bytes: %v\n", buffer[:numBytesReceived])
			fmt.Printf("Hex: %x\n", buffer[:numBytesReceived])
		}
	}()

	//Sender
	serverAddr := &net.UDPAddr{IP: net.IPv4(10, 100, 23, 11), Port: 20006}

	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			_, err := conn.WriteToUDP([]byte("Hello\n"), serverAddr)
			if err != nil {
				fmt.Println("Error write:", err)
				continue
			}
			fmt.Println("Sent message to", serverAddr.String())
		}
	}()

	select {}
}
