package main

import (
	"Driver-go/elevio"
	"Driver-go/fsm"
	"Network-go/network/bcast"
	"Network-go/network/localip"
	"Network-go/network/peers"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"
)

func main() {
	// Parse flags
	var elevatorIDStr string
	var port string
	flag.StringVar(&elevatorIDStr, "id", "", "Elevator ID (0, 1, 2, ...)")
	flag.StringVar(&port, "port", "15657", "Elevator hardware port")
	flag.Parse()

	// hvis ingen ID gitt, bruk lokal IP + PID
	if elevatorIDStr == "" {
		if localIP, err := localip.LocalIP(); err == nil {
			elevatorIDStr = fmt.Sprintf("%s-%d", localIP, os.Getpid())
		} else {
			elevatorIDStr = "elevator"
		}
	}

	// Konverter elevator ID til integer (0, 1, 2...)
	elevatorID := 0
	if id, err := strconv.Atoi(elevatorIDStr); err == nil {
		elevatorID = id % 3 // Sikre 0-2
	}

	numFloors := 4
	elevio.Init("localhost:"+port, numFloors)

	fmt.Printf("\n========================================\n")
	fmt.Printf("Elevator %d starting\n", elevatorID)
	fmt.Printf("Hardware port: %s\n", port)
	fmt.Printf("========================================\n\n")

	// ===== HARDWARE CHANNELS (FSM) =====
	drvButtons := make(chan elevio.ButtonEvent)
	drvFloors := make(chan int)
	drvObstr := make(chan bool)
	drvOrders := make(chan fsm.Order, 10)
	fsmStateUpdates := make(chan fsm.StateUpdate, 10)

	// ===== NETWORK CHANNELS (via Network-go/bcast) =====
	// State broadcast - begge heiser sender og mottar via samme port
	stateTxCh := make(chan ElevatorStateMsg)
	stateRxCh := make(chan ElevatorStateMsg)

	// Hall order broadcast
	hallOrderTxCh := make(chan HallOrderMsg)
	hallOrderRxCh := make(chan HallOrderMsg)

	// Global state (bygget av StateManager)
	globalStateCh := make(chan GlobalNetworkState)

	// Peer detection channels
	peerUpdateCh := make(chan peers.PeerUpdate)
	peerTxEnable := make(chan bool)

	// ===== START HARDWARE POLLING =====
	go elevio.PollButtons(drvButtons)
	go elevio.PollFloorSensor(drvFloors)
	go elevio.PollObstructionSwitch(drvObstr)

	// ===== START NETWORK-GO COMPONENTS =====
	fmt.Println("[Main] Starting network components...")

	// Peer detection (bruker own ID as elevator ID string)
	elevatorIDStr2 := fmt.Sprintf("elev-%d", elevatorID)
	go peers.Transmitter(15647, elevatorIDStr2, peerTxEnable)
	go peers.Receiver(15647, peerUpdateCh)

	// State broadcast via bcast
	// Denne sender/mottar ElevatorStateMsg på port 16789
	go bcast.Transmitter(16789, stateTxCh)
	go bcast.Receiver(16789, stateRxCh)

	// Hall order broadcast via bcast
	// Denne sender/mottar HallOrderMsg på port 16790
	go bcast.Transmitter(16790, hallOrderTxCh)
	go bcast.Receiver(16790, hallOrderRxCh)

	// Monitor peer updates (for debugging)
	go func() {
		for peerUpdate := range peerUpdateCh {
			fmt.Printf("[Network] Peers: %v (new: %v, lost: %v)\n",
				peerUpdate.Peers, peerUpdate.New, peerUpdate.Lost)
		}
	}()

	// ===== START STATE MANAGEMENT =====
	fmt.Println("[Main] Starting state manager...")

	stateManager := NewStateManager(
		elevatorID,
		numFloors,
		fsmStateUpdates,
		stateTxCh,
		stateRxCh,
		globalStateCh,
	)
	go stateManager.Run()

	// ===== START ORDER ASSIGNER =====
	fmt.Println("[Main] Starting distributed order assigner...")

	orderAssigner := NewDeterministicOrderAssigner(
		elevatorID,
		hallOrderRxCh,
		globalStateCh,
		drvOrders,
	)
	go orderAssigner.Run()

	// ===== START FSM =====
	fmt.Println("[Main] Starting FSM...")

	go fsm.Run(numFloors, drvButtons, drvFloors, drvObstr, drvOrders, fsmStateUpdates)

	fmt.Println("[Main] System ready!")

	// TEMP: Simulator hall order generator for testing
	go func() {
		time.Sleep(3 * time.Second)
		hallOrderID := 1
		for {
			hallOrder := HallOrderMsg{
				ID:     hallOrderID,
				Floor:  2,
				Button: elevio.BT_HallUp,
				Time:   time.Now().UnixNano(),
			}
			fmt.Printf("[Main] Publishing test hall order: floor=%d\n", hallOrder.Floor)
			hallOrderTxCh <- hallOrder
			hallOrderID++
			time.Sleep(10 * time.Second)
		}
	}()

	// Keep running
	select {}
}

// func main() {

// 	numFloors := 4

// 	elevio.Init("localhost:15657", numFloors)

// 	var d elevio.MotorDirection = elevio.MD_Up
// 	elevio.SetMotorDirection(d)

// 	drv_buttons := make(chan elevio.ButtonEvent)
// 	drv_floors := make(chan int)
// 	drv_obstr := make(chan bool)
// 	drv_stop := make(chan bool)

// 	go elevio.PollButtons(drv_buttons)
// 	go elevio.PollFloorSensor(drv_floors)
// 	go elevio.PollObstructionSwitch(drv_obstr)
// 	go elevio.PollStopButton(drv_stop)

// 	for {
// 		select {
// 		case a := <-drv_buttons:
// 			fmt.Printf("%+v\n", a)
// 			elevio.SetButtonLamp(a.Button, a.Floor, true)

// 		case a := <-drv_floors:
// 			fmt.Printf("%+v\n", a)
// 			if a == numFloors-1 {
// 				d = elevio.MD_Down
// 			} else if a == 0 {
// 				d = elevio.MD_Up
// 			}
// 			elevio.SetMotorDirection(d)

// 		case a := <-drv_obstr:
// 			fmt.Printf("%+v\n", a)
// 			if a {
// 				elevio.SetMotorDirection(elevio.MD_Stop)
// 			} else {
// 				elevio.SetMotorDirection(d)
// 			}

// 		case a := <-drv_stop:
// 			fmt.Printf("%+v\n", a)
// 			for f := 0; f < numFloors; f++ {
// 				for b := elevio.ButtonType(0); b < 3; b++ {
// 					elevio.SetButtonLamp(b, f, false)
// 				}
// 			}
// 		}
// 	}
// }
