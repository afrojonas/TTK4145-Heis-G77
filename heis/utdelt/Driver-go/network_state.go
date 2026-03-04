package main

import (
	"Driver-go/elevio"
	"time"
)

// ElevatorStateMsg er den primære structen som blir sendt over nettverket
// alle heiser deler denne informasjonen (broacast via bcast.Transmitter/Receiver)
type ElevatorStateMsg struct {
	ID        int      // Hvilken heis dette er (0, 1, 2...)
	Floor     int      // Hvilken etasje (-1 hvis mellom etasjer)
	Direction int      // -1=down, 0=stop, 1=up
	Orders    [][]bool // [floor][buttontype] - lokale ordrer
	Timestamp int64    // Unix timestamp (ns) for timeout-deteksjon
}

// HallOrderMsg representerer en hallkald som må tildeles
// sendes via bcast slik at alle heiser kan tildele den samme måten
type HallOrderMsg struct {
	ID     int               // Unik ordre-ID (sekvensnummer)
	Floor  int               // Hvilken etasje
	Button elevio.ButtonType // BT_HallUp eller BT_HallDown
	Time   int64             // Når ordren ble opprettet
}

// GlobalNetworkState er den komplette tilstanden som alle heiser har
// denne bygges lokalt basert på mottatte ElevatorStateMsg fra alle
type GlobalNetworkState struct {
	Elevators map[int]ElevatorStateMsg // ID -> state
	Peers     map[string]time.Time     // Oppdatert fra peers.Receiver
}
