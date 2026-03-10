package main

import (
	"Driver-go/fsm"
	"fmt"
	"math"
)

// DeterministicOrderAssigner kjører samme orden-tildelings-algoritme
// på alle heiser, slik at alle får samme resultat
type DeterministicOrderAssigner struct {
	elevatorID     int
	hallOrderRxCh  <-chan HallOrderMsg       // Nye hall-ordrer
	globalStateCh  <-chan GlobalNetworkState // Global tilstand
	orderTxCh      chan<- fsm.Order          // Til egen FSM
	assignedOrders map[int]int               // OrderID -> AssignedElevID
}

// NewDeterministicOrderAssigner oppretter order assigner
func NewDeterministicOrderAssigner(
	elevatorID int,
	hallOrderRxCh <-chan HallOrderMsg,
	globalStateCh <-chan GlobalNetworkState,
	orderTxCh chan<- fsm.Order,
) *DeterministicOrderAssigner {
	return &DeterministicOrderAssigner{
		elevatorID:     elevatorID,
		hallOrderRxCh:  hallOrderRxCh,
		globalStateCh:  globalStateCh,
		orderTxCh:      orderTxCh,
		assignedOrders: make(map[int]int),
	}
}

// Run starter order assigner løkken
func (doa *DeterministicOrderAssigner) Run() {
	fmt.Printf("[OrderAssigner-%d] Started (deterministic distributed)\n", doa.elevatorID)

	var lastGlobalState GlobalNetworkState

	for {
		select {
		// Ny hallorder mottatt
		case hallOrder := <-doa.hallOrderRxCh:
			doa.onNewHallOrder(hallOrder, lastGlobalState)

		// Global state oppdatert
		case globalState := <-doa.globalStateCh:
			lastGlobalState = globalState
		}
	}
}

// onNewHallOrder kalles når en ny hallorder kommer
func (doa *DeterministicOrderAssigner) onNewHallOrder(order HallOrderMsg, globalState GlobalNetworkState) {
	fmt.Printf("[OrderAssigner-%d] New hall order: floor=%d button=%d\n",
		doa.elevatorID, order.Floor, order.Button)

	// Kjør samme algoritme på alle heiser
	assignedElevID := doa.assignOrderDeterministic(order, globalState)

	fmt.Printf("[OrderAssigner-%d] Assigned order {floor:%d btn:%d} to elev %d\n",
		doa.elevatorID, order.Floor, order.Button, assignedElevID)

	// Hvis det skal til vår heis, send til FSM
	if assignedElevID == doa.elevatorID {
		fsmOrder := fsm.Order{
			Floor:  order.Floor,
			Button: order.Button,
		}

		select {
		case doa.orderTxCh <- fsmOrder:
			fmt.Printf("[OrderAssigner-%d] Sent order to my FSM\n", doa.elevatorID)
		default:
			fmt.Printf("[OrderAssigner-%d] Order channel full\n", doa.elevatorID)
		}
	}
}

// assignOrderDeterministic bestemmer hvilken heis som skal ta ordren
// samme algoritme på alle heiser med samme global state = samme resultat
func (doa *DeterministicOrderAssigner) assignOrderDeterministic(
	order HallOrderMsg,
	globalState GlobalNetworkState,
) int {
	if len(globalState.Elevators) == 0 {
		return doa.elevatorID
	}

	bestElevID := -1
	bestCost := math.MaxInt32

	for elevID, elevState := range globalState.Elevators {
		cost := doa.calculateCost(order, elevState)

		if cost < bestCost || (cost == bestCost && elevID < bestElevID) {
			bestCost = cost
			bestElevID = elevID
		}
	}

	if bestElevID == -1 {
		return doa.elevatorID
	}

	return bestElevID
}

// calculateCost beregner kostnad for en heis å ta ordren
// Optimalisert for at hall calls skal gå til NÆRMESTE heis
func (doa *DeterministicOrderAssigner) calculateCost(
	order HallOrderMsg,
	elevState ElevatorStateMsg,
) int {
	distance := abs(elevState.Floor - order.Floor)

	// Hvis heisen er på samme etasje og står stille (idle), gi den lavest mulig kostnad
	if distance == 0 && elevState.Direction == 0 {
		return 0
	}

	queueLength := 0
	for _, floorOrders := range elevState.Orders {
		for _, hasOrder := range floorOrders {
			if hasOrder {
				queueLength++
			}
		}
	}

	// Primær faktor: avstand (høy vekt)
	// Sekundær faktor: queue (lav vekt)
	cost := distance*100 + queueLength*2

	// Retningsstraf: MYKERE enn før
	// Kun hvis heisen beveger seg BORT fra ordren og har lang kø
	if queueLength > 2 {
		if elevState.Direction == 1 && order.Floor < elevState.Floor {
			cost += 20
		} else if elevState.Direction == -1 && order.Floor > elevState.Floor {
			cost += 20
		}
	}

	// Bonus hvis heisen beveger seg MOT ordren
	if elevState.Direction == 1 && order.Floor > elevState.Floor {
		cost -= 10
	} else if elevState.Direction == -1 && order.Floor < elevState.Floor {
		cost -= 10
	}

	return cost
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
