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
	drvButtonsRaw := make(chan elevio.ButtonEvent, 10) // Raw input fra hardware
	drvCabCalls := make(chan elevio.ButtonEvent, 10)   // Kun CAB calls (BT_Cab=2) til FSM
	drvFloors := make(chan int)
	drvObstr := make(chan bool)
	drvOrders := make(chan fsm.Order, 10)
	fsmStateUpdates := make(chan fsm.StateUpdate, 10)

	// ===== NETWORK CHANNELS (via Network-go/bcast) =====
	// State broadcast - begge heiser sender og mottar via samme port
	stateTxCh := make(chan ElevatorStateMsg)
	stateRxCh := make(chan ElevatorStateMsg)

	// Hall order broadcast - to separate receivers slik at både HallLightMgr og OrderAssigner får alle meldinger
	hallOrderTxCh := make(chan HallOrderMsg)
	hallOrderRxCh1 := make(chan HallOrderMsg) // For HallLightMgr
	hallOrderRxCh2 := make(chan HallOrderMsg) // For OrderAssigner

	// Hall order cleared broadcast (når en heis har clearet en hall order)
	hallOrdersClearedTxCh := make(chan HallOrderMsg)
	hallOrdersClearedRxCh := make(chan HallOrderMsg)

	// Global state (bygget av StateManager)
	globalStateCh := make(chan GlobalNetworkState)

	// Peer detection channels
	peerUpdateCh := make(chan peers.PeerUpdate)
	peerTxEnable := make(chan bool)

	// ===== START HARDWARE POLLING =====
	go elevio.PollButtons(drvButtonsRaw)
	go elevio.PollFloorSensor(drvFloors)
	go elevio.PollObstructionSwitch(drvObstr)

	// ===== BUTTON ROUTER & HALL CALL BROADCASTER =====
	// Filtrerer buttons:
	// - Hall calls (BT_HallUp=0, BT_HallDown=1) -> broadcast via hallOrderTxCh
	// - CAB calls (BT_Cab=2) -> lokal ordre direkte til FSM via drvCabCalls
	var hallOrderCounter int
	go func() {
		for btn := range drvButtonsRaw {
			if btn.Button == 0 || btn.Button == 1 { // BT_HallUp eller BT_HallDown
				// Broadcast hall call slik at alle heiser kan tildele det
				hallOrderCounter++
				hallOrder := HallOrderMsg{
					ID:     hallOrderCounter,
					Floor:  btn.Floor,
					Button: btn.Button,
					Time:   time.Now().UnixNano(),
				}
				select {
				case hallOrderTxCh <- hallOrder:
					fmt.Printf("[HallCaller] Broadcast hall call: floor=%d button=%d (ID=%d)\n",
						btn.Floor, btn.Button, hallOrderCounter)
				default:
					fmt.Printf("[HallCaller] Channel full, dropped hall call\n")
				}
			} else if btn.Button == 2 { // BT_Cab
				// CAB calls går direkte til FSM (lokale ordrer)
				select {
				case drvCabCalls <- btn:
					fmt.Printf("[ButtonRouter] CAB call: floor=%d\n", btn.Floor)
				default:
					fmt.Printf("[ButtonRouter] CAB channel full\n")
				}
			}
		}
	}()

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

	// Hall order broadcast via bcast - TWO separate receivers
	// Denne sender HallOrderMsg på port 16790
	go bcast.Transmitter(16790, hallOrderTxCh)
	go bcast.Receiver(16790, hallOrderRxCh1) // For HallLightMgr
	go bcast.Receiver(16790, hallOrderRxCh2) // For OrderAssigner

	// Hall orders cleared broadcast via bcast
	// Denne sender/mottar HallOrderMsg på port 16791 (signal når en heis har clearet en hall order)
	go bcast.Transmitter(16791, hallOrdersClearedTxCh)
	go bcast.Receiver(16791, hallOrdersClearedRxCh)

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
		hallOrdersClearedTxCh,
		globalStateCh,
	)
	go stateManager.Run()

	// ===== START ORDER ASSIGNER =====
	fmt.Println("[Main] Starting distributed order assigner...")

	orderAssigner := NewDeterministicOrderAssigner(
		elevatorID,
		hallOrderRxCh2,
		globalStateCh,
		drvOrders,
	)
	go orderAssigner.Run()

	// ===== START FSM =====
	fmt.Println("[Main] Starting FSM...")

	go fsm.Run(numFloors, drvCabCalls, drvFloors, drvObstr, drvOrders, fsmStateUpdates)

	// ===== HALL BUTTON LIGHT MANAGER =====
	// Håndterer hall button lights globalt på tvers av alle heiser
	// Tennerer lysene når en hall order mottas, sletter når den cleareres
	go func() {
		activeHallOrders := make(map[int]HallOrderMsg) // OrderID -> HallOrderMsg

		for {
			select {
			// Ny hall order -> sett lyset
			case hallOrder := <-hallOrderRxCh1:
				if _, exists := activeHallOrders[hallOrder.ID]; !exists {
					activeHallOrders[hallOrder.ID] = hallOrder
					elevio.SetButtonLamp(hallOrder.Button, hallOrder.Floor, true)
					fmt.Printf("[HallLightMgr] Set light: floor=%d button=%d (ID=%d)\n",
						hallOrder.Floor, hallOrder.Button, hallOrder.ID)
				}

			// Hall order cleared -> slett lyset
			case hallOrder := <-hallOrdersClearedRxCh:
				if _, exists := activeHallOrders[hallOrder.ID]; exists {
					delete(activeHallOrders, hallOrder.ID)
					elevio.SetButtonLamp(hallOrder.Button, hallOrder.Floor, false)
					fmt.Printf("[HallLightMgr] Cleared light: floor=%d button=%d (ID=%d)\n",
						hallOrder.Floor, hallOrder.Button, hallOrder.ID)
				}
			}
		}
	}()

	fmt.Println("[Main] System ready!")

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
