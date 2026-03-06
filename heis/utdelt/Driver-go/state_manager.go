package main

import (
	"Driver-go/fsm"
	"fmt"
	"time"
)

// StateManager kombinerer broadcasting av egen state med mottak av andres state
// bruker Network-go sitt bcast og peers system direkte
type StateManager struct {
	elevatorID     int
	myStateCh      <-chan fsm.StateUpdate    // Fra FSM
	stateTxCh      chan<- ElevatorStateMsg   // Til bcast.Transmitter
	stateRxCh      <-chan ElevatorStateMsg   // Fra bcast.Receiver
	globalStateCh  chan<- GlobalNetworkState // Utgående komplett tilstand
	knownElevators map[int]ElevatorStateMsg  // Lagrete stater
	lastPeerUpdate time.Time
	numFloors      int
}

// NewStateManager oppretter StateManager
func NewStateManager(
	elevatorID int,
	numFloors int,
	myStateCh <-chan fsm.StateUpdate,
	stateTxCh chan<- ElevatorStateMsg,
	stateRxCh <-chan ElevatorStateMsg,
	globalStateCh chan<- GlobalNetworkState,
) *StateManager {
	return &StateManager{
		elevatorID:     elevatorID,
		myStateCh:      myStateCh,
		stateTxCh:      stateTxCh,
		stateRxCh:      stateRxCh,
		globalStateCh:  globalStateCh,
		knownElevators: make(map[int]ElevatorStateMsg),
		numFloors:      numFloors,
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

		// Motta state fra andre heiser
		case rcvdState := <-sm.stateRxCh:
			// Bare printe hvis det er fra en annen heis
			if rcvdState.ID != sm.elevatorID {
				fmt.Printf("[StateManager-%d] Received state from elev %d: floor=%d\n",
					sm.elevatorID, rcvdState.ID, rcvdState.Floor)
			}
			sm.knownElevators[rcvdState.ID] = rcvdState
			sm.publishGlobalState()

		// Broadcast min state jevnlig
		case <-broadcastTicker.C:
			myState.Timestamp = time.Now().UnixNano()
			select {
			case sm.stateTxCh <- myState:
			default:
				// Channel full, skip
			}

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
