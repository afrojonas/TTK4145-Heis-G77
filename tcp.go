package main

import (
	"bufio"
	"fmt"
	"net"
	"time"
)

func main() {
	addrServer := net.TCPAddr{
		IP:   net.IPv4(10, 100, 23, 11),
		Port: 33546,
	}

	//Sette opp listening
	listenPort := 40000

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", listenPort))
	if err != nil {
		fmt.Println("Error listening:", err)
		return
	}
	defer ln.Close()
	fmt.Println("Listening on port", listenPort)

	//Ring ring ring
	conn, err := net.DialTCP("tcp", nil, &addrServer)
	if err != nil {
		fmt.Println("Error dialing:", err)
		return
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	fmt.Println("Connected to server, sending and reading responses...")

	myIP := conn.LocalAddr().(*net.TCPAddr).IP.String()

	// Ber server om å koble tilbake
	connectBackMsg := fmt.Sprintf("Connect to: %s:%d\x00", myIP, listenPort)
	_, err = conn.Write([]byte(connectBackMsg))
	if err != nil {
		fmt.Println("Error writing connect-back message:", err)
		return
	}
	fmt.Printf("Sent to server: %q\n", connectBackMsg)

	// Vent på at serveren kobler tilbake
	fmt.Println("Waiting for server to connect back...")
	backConn, err := ln.Accept()
	if err != nil {
		fmt.Println("Accept error:", err)
		return
	}
	defer backConn.Close()
	fmt.Println("Server connected back from:", backConn.RemoteAddr().String())

	// 6) Bruker backConn istender for conn
	reader = bufio.NewReader(backConn)
	fmt.Println("Using callback connection for send/recv...")

	go func() {
		for {
			bytesReceived, err := reader.ReadBytes(0) // les til '\0'
			if err != nil {
				fmt.Println("Error reading:", err)
				return
			}

			if len(bytesReceived) > 1 {
				message := string(bytesReceived[:len(bytesReceived)-1]) // fjern '\0'
				fmt.Printf("Received %d bytes: %s\n", len(bytesReceived)-1, message)
			}
		}
	}()

	go func() {
		for {
			_, err := backConn.Write([]byte("Sigve har svær dong (callback)!\x00"))
			if err != nil {
				fmt.Println("Error write:", err)
				return
			}
			fmt.Println("Sent message on callback connection")
			time.Sleep(5 * time.Second)
		}
	}()

	select {}
}
