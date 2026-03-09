package main

import (
	"Driver-go/elevio"
	"Driver-go/fsm"
	"fmt"
	"time"
)

// StateManager kombinerer broadcasting av egen state med mottak av andres state
// bruker Network-go sitt bcast og peers system direkte
type StateManager struct {
	elevatorID            int
	myStateCh             <-chan fsm.StateUpdate    // Fra FSM
	stateTxCh             chan<- ElevatorStateMsg   // Til bcast.Transmitter
	stateRxCh             <-chan ElevatorStateMsg   // Fra bcast.Receiver
	hallOrderRxCh         <-chan HallOrderMsg       // Mottatt hall orders (for tracking)
	hallOrdersClearedTxCh chan<- HallOrderMsg       // Til bcast for cleared hall orders
	globalStateCh         chan<- GlobalNetworkState // Utgående komplett tilstand
	knownElevators        map[int]ElevatorStateMsg  // Lagrete stater
	lastPrintedState      map[int]ElevatorStateMsg  // Siste state vi printet (for change detection)
	previousOrderStates   map[int][][]bool          // Forrige tilstand av orders per heis (for change detection)
	activeHallOrders      map[int]HallOrderMsg      // Track alle aktive hall orders (ID -> HallOrderMsg)
	lastPeerUpdate        time.Time
	numFloors             int
}

// NewStateManager oppretter StateManager
func NewStateManager(
	elevatorID int,
	numFloors int,
	myStateCh <-chan fsm.StateUpdate,
	stateTxCh chan<- ElevatorStateMsg,
	stateRxCh <-chan ElevatorStateMsg,
	hallOrderRxCh <-chan HallOrderMsg,
	hallOrdersClearedTxCh chan<- HallOrderMsg,
	globalStateCh chan<- GlobalNetworkState,
) *StateManager {
	return &StateManager{
		elevatorID:            elevatorID,
		myStateCh:             myStateCh,
		stateTxCh:             stateTxCh,
		stateRxCh:             stateRxCh,
		hallOrderRxCh:         hallOrderRxCh,
		hallOrdersClearedTxCh: hallOrdersClearedTxCh,
		globalStateCh:         globalStateCh,
		knownElevators:        make(map[int]ElevatorStateMsg),
		lastPrintedState:      make(map[int]ElevatorStateMsg),
		previousOrderStates:   make(map[int][][]bool),
		activeHallOrders:      make(map[int]HallOrderMsg),
		numFloors:             numFloors,
	}
}

// Run starter state manager løkken
// sender min state via TX kanal hver gang FSM oppdateres
// mottar andres state via RX kanal og publiserer global state
func (sm *StateManager) Run() {
	fmt.Printf("[StateManager-%d] Started\n", sm.elevatorID)

	// Timeout check ticker
	timeoutTicker := time.NewTicker(500 * time.Millisecond)

	// Broadcast tick - send state jevnlig selv om ingenting har endret
	broadcastTicker := time.NewTicker(500 * time.Millisecond)

	// Siste kjente state
	myState := ElevatorStateMsg{
		ID:        sm.elevatorID,
		Floor:     0,
		Direction: 0,
		Orders:    make([][]bool, sm.numFloors),
		Timestamp: time.Now().UnixNano(),
	}

	for {
		select {
		// Når FSM sender state update
		case update := <-sm.myStateCh:
			myState.Floor = update.Floor
			myState.Direction = update.Direction
			myState.Orders = update.Orders
			myState.Timestamp = time.Now().UnixNano()
			fmt.Printf("[StateManager-%d] FSM update: floor=%d dir=%d\n",
				sm.elevatorID, update.Floor, update.Direction)

			// Detekt når hall orders har blitt slettet (cleared)
			sm.detectClearedHallOrders(myState.Orders)

			// Lagre egen state i knownElevators slik at OrderAssigner kan see den
			sm.knownElevators[sm.elevatorID] = myState
			sm.publishGlobalState()

		// Motta state fra andre heiser
		case rcvdState := <-sm.stateRxCh:
			// Ignorer egen state
			if rcvdState.ID == sm.elevatorID {
				continue
			}

			// Sjekk om staten har endret seg
			lastState, exists := sm.lastPrintedState[rcvdState.ID]
			stateChanged := !exists || lastState.Floor != rcvdState.Floor || lastState.Direction != rcvdState.Direction

			if stateChanged {
				fmt.Printf("[StateManager-%d] State change: elev %d floor=%d dir=%d\n",
					sm.elevatorID, rcvdState.ID, rcvdState.Floor, rcvdState.Direction)
				sm.lastPrintedState[rcvdState.ID] = rcvdState
			}

			sm.knownElevators[rcvdState.ID] = rcvdState

			// VIKTIG: Detekt når ANDRE heiser clearer hall orders
			sm.detectClearedHallOrdersForElevator(rcvdState.ID, rcvdState.Orders)

			sm.publishGlobalState()

		// Motta ny hall order
		case hallOrder := <-sm.hallOrderRxCh:
			sm.activeHallOrders[hallOrder.ID] = hallOrder
			fmt.Printf("[StateManager-%d] Tracking active hall order: ID=%d floor=%d button=%d\n",
				sm.elevatorID, hallOrder.ID, hallOrder.Floor, hallOrder.Button)

		// Broadcast min state jevnlig
		case <-broadcastTicker.C:
			myState.Timestamp = time.Now().UnixNano()
			select {
			case sm.stateTxCh <- myState:
			default:
				// Channel full, skip
			}
			// Lagre egen state i knownElevators slik at den ikke timeout-es
			sm.knownElevators[sm.elevatorID] = myState
			sm.publishGlobalState()

		// Periodisk timeout check
		case <-timeoutTicker.C:
			sm.checkForTimeouts()
		}
	}
}

// checkForTimeouts fjerner heiser som ikke har sendt state på over 2 sekunder
func (sm *StateManager) checkForTimeouts() {
	now := time.Now().UnixNano()
	for elevID, state := range sm.knownElevators {
		timeSincens := now - state.Timestamp
		timeSinceSec := float64(timeSincens) / 1e9

		if timeSinceSec > 2.0 {
			fmt.Printf("[StateManager-%d] TIMEOUT: elev %d (%.1fs no update)\n",
				sm.elevatorID, elevID, timeSinceSec)
			delete(sm.knownElevators, elevID)
			sm.publishGlobalState()
		}
	}
}

// publishGlobalState sender oppdatert global state
func (sm *StateManager) publishGlobalState() {
	globalState := GlobalNetworkState{
		Elevators: sm.knownElevators,
		Peers:     make(map[string]time.Time), // Peers oppdateres via peers.Receiver
	}

	select {
	case sm.globalStateCh <- globalState:
	default:
		// Channel full
	}
}

// detectClearedHallOrders monitorerer ALLE heiser sine ordrer og sender cleared signal når hall orders forsvinner
// Dette sikrer at ALLE paneler slukker lysene når NOEN heis clearer en hall order
func (sm *StateManager) detectClearedHallOrders(currentOrders [][]bool) {
	// Først: detekt clearet ordrer fra EGEN heis
	prevOrders := sm.previousOrderStates[sm.elevatorID]

	// Hvis ingen tidligere tilstand, lagre nåværende og return
	if prevOrders == nil {
		sm.previousOrderStates[sm.elevatorID] = copyOrders(currentOrders)
		return
	}

	// Sammenligner og finner ordrer som har forsvunnet fra egen heis
	for floor := 0; floor < len(currentOrders); floor++ {
		for button := 0; button < len(currentOrders[floor]); button++ {
			hadOrder := prevOrders[floor][button]
			hasOrder := currentOrders[floor][button]

			// Hvis ordren har forsvunnet (fra true til false), er den clearet
			if hadOrder && !hasOrder {
				// Kun broadcast hall calls (button 0 og 1), ikke CAB calls (button 2)
				if button == 0 || button == 1 {
					sm.broadcastClearedHallOrder(floor, elevio.ButtonType(button))
				}
			}
		}
	}

	// Oppdater tidligere tilstand for egen heis
	sm.previousOrderStates[sm.elevatorID] = copyOrders(currentOrders)

	// Deretter: monitorere ALLE andre heiser sin state via knownElevators
	for elevID, elevState := range sm.knownElevators {
		if elevID == sm.elevatorID {
			continue // Skip egen heis, allerede håndlet ovenfor
		}

		prevState := sm.previousOrderStates[elevID]
		if prevState == nil {
			sm.previousOrderStates[elevID] = copyOrders(elevState.Orders)
			continue
		}

		// Sjekk om noen hall orders har forsvunnet fra denne heisen
		for floor := 0; floor < len(elevState.Orders); floor++ {
			for button := 0; button < len(elevState.Orders[floor]); button++ {
				hadOrder := prevState[floor][button]
				hasOrder := elevState.Orders[floor][button]

				if hadOrder && !hasOrder {
					if button == 0 || button == 1 {
						sm.broadcastClearedHallOrder(floor, elevio.ButtonType(button))
					}
				}
			}
		}

		sm.previousOrderStates[elevID] = copyOrders(elevState.Orders)
	}
}

// broadcastClearedHallOrder sender cleared signal for en hall order
func (sm *StateManager) broadcastClearedHallOrder(floor int, button elevio.ButtonType) {
	hallOrder := HallOrderMsg{
		Floor:  floor,
		Button: button,
	}
	select {
	case sm.hallOrdersClearedTxCh <- hallOrder:
		fmt.Printf("[StateManager-%d] Sent cleared signal: floor=%d button=%d\n",
			sm.elevatorID, floor, int(button))
	default:
		// Channel full, skip
	}
}

// detectClearedHallOrdersForElevator sjekker en spesifikk heis for clearet hall orders
func (sm *StateManager) detectClearedHallOrdersForElevator(elevID int, currentOrders [][]bool) {
	prevOrders := sm.previousOrderStates[elevID]

	// Hvis ingen tidligere tilstand, lagre nåværende og return
	if prevOrders == nil {
		sm.previousOrderStates[elevID] = copyOrders(currentOrders)
		return
	}

	// Sammenligner og finner ordrer som har forsvunnet
	for floor := 0; floor < len(currentOrders); floor++ {
		for button := 0; button < len(currentOrders[floor]); button++ {
			hadOrder := prevOrders[floor][button]
			hasOrder := currentOrders[floor][button]

			// Hvis ordren har forsvunnet (fra true til false), er den clearet
			if hadOrder && !hasOrder {
				// Kun broadcast hall calls (button 0 og 1), ikke CAB calls (button 2)
				if button == 0 || button == 1 {
					sm.broadcastClearedHallOrder(floor, elevio.ButtonType(button))
					fmt.Printf("[StateManager-%d] Detected cleared order from elev %d: floor=%d button=%d\n",
						sm.elevatorID, elevID, floor, int(button))
				}
			}
		}
	}

	// Oppdater tidligere tilstand
	sm.previousOrderStates[elevID] = copyOrders(currentOrders)
}

// copyOrders lager en kopi av orders for lagring
func copyOrders(orders [][]bool) [][]bool {
	copy := make([][]bool, len(orders))
	for i, row := range orders {
		copy[i] = make([]bool, len(row))
		for j, val := range row {
			copy[i][j] = val
		}
	}
	return copy
}
