package orderassigner

import (
	"fmt"
	"math"

	"Driver-go/elevio"
	"Driver-go/fsm"
)




//GAMMEL ORDERASSIGNER, BRUKES IKKE 



// ElevatorState represents the state of an elevator
type ElevatorState struct {
	ID        int
	Floor     int
	Direction int // -1 (down), 0 (stop), 1 (up)
	Alive     bool
	NumFloors int
	Orders    [][]bool // [floor][buttontype]
}

// HallOrder represents a hall call order
type HallOrder struct {
	Floor  int
	Button elevio.ButtonType // BT_HallUp or BT_HallDown
}

// OrderAssigner handles order distribution for a single elevator
type OrderAssigner struct {
	elevatorID  int
	numFloors   int
	myState     ElevatorState
	otherElevs  map[int]ElevatorState
	hallOrders  []HallOrder
	orderCh     chan<- fsm.Order
	myStateCh   <-chan fsm.StateUpdate
	hallOrderCh <-chan HallOrder
}

// NewOrderAssigner creates a new order assigner for this elevator
func NewOrderAssigner(elevatorID int, numFloors int, orderCh chan<- fsm.Order, myStateCh <-chan fsm.StateUpdate, hallOrderCh <-chan HallOrder) *OrderAssigner {
	return &OrderAssigner{
		elevatorID:  elevatorID,
		numFloors:   numFloors,
		otherElevs:  make(map[int]ElevatorState),
		hallOrders:  []HallOrder{},
		orderCh:     orderCh,
		myStateCh:   myStateCh,
		hallOrderCh: hallOrderCh,
		myState: ElevatorState{
			ID:        elevatorID,
			Floor:     0,
			Direction: 0,
			Alive:     true,
			NumFloors: numFloors,
			Orders:    make([][]bool, numFloors),
		},
	}
}

// Run starts the order assigner event loop
func (oa *OrderAssigner) Run() {
	fmt.Printf("[OrderAssigner-%d] started\n", oa.elevatorID)

	for {
		select {
		// Update my own state from FSM
		case update := <-oa.myStateCh:
			oa.onMyStateUpdate(update)

		// New hall order received (from network or local input in future)
		case hallOrder, ok := <-oa.hallOrderCh:
			if !ok {
				// Channel closed, continue without hall orders
				continue
			}
			oa.onNewHallOrder(hallOrder)
		}
	}
}

// onMyStateUpdate handles state updates from the FSM
func (oa *OrderAssigner) onMyStateUpdate(update fsm.StateUpdate) {
	oa.myState.Floor = update.Floor
	oa.myState.Direction = update.Direction
	oa.myState.Orders = update.Orders

	fmt.Printf("[OrderAssigner-%d] state update: floor=%d dir=%d\n", oa.elevatorID, update.Floor, update.Direction)

	// Re-evaluate hall orders in case any should be assigned to me
	oa.tryAssignHallOrders()
}

// onNewHallOrder handles a new hall order
func (oa *OrderAssigner) onNewHallOrder(order HallOrder) {
	fmt.Printf("[OrderAssigner-%d] new hall order: floor=%d button=%d\n", oa.elevatorID, order.Floor, order.Button)

	// Add to pending orders
	oa.hallOrders = append(oa.hallOrders, order)

	// Try to assign it
	oa.tryAssignHallOrders()
}

// tryAssignHallOrders attempts to assign pending hall orders
func (oa *OrderAssigner) tryAssignHallOrders() {
	if len(oa.hallOrders) == 0 {
		return
	}

	assigned := []HallOrder{}

	for _, order := range oa.hallOrders {
		// For now: assign to myself if I'm the best candidate
		// In Fase 2, we'll check other elevators too
		isBestCandidate := oa.isBestCandidateForOrder(order)

		if isBestCandidate {
			fmt.Printf("[OrderAssigner-%d] assigning hall order floor=%d to myself\n", oa.elevatorID, order.Floor)
			// Send order to FSM via orderCh
			oa.orderCh <- fsm.Order{
				Floor:  order.Floor,
				Button: order.Button,
			}
			assigned = append(assigned, order)
		}
	}

	// Remove assigned orders from pending
	for _, assignedOrder := range assigned {
		for i, order := range oa.hallOrders {
			if order.Floor == assignedOrder.Floor && order.Button == assignedOrder.Button {
				oa.hallOrders = append(oa.hallOrders[:i], oa.hallOrders[i+1:]...)
				break
			}
		}
	}
}

// isBestCandidateForOrder determines if this elevator should take the order
// For Fase 1: use simple distance-based heuristic
func (oa *OrderAssigner) isBestCandidateForOrder(order HallOrder) bool {
	// Simple heuristic: if I'm the only elevator, assign to me
	if len(oa.otherElevs) == 0 {
		return true
	}

	// Calculate my cost to reach this order
	myCost := oa.costToReachFloor(order.Floor, oa.myState)

	// In Fase 2: compare with other elevators' costs
	// For now, just take it if others are far away or non-existent
	for _, other := range oa.otherElevs {
		if !other.Alive {
			continue
		}
		otherCost := oa.costToReachFloor(order.Floor, other)
		if otherCost < myCost {
			// Other elevator is better suited
			return false
		}
	}

	return true
}

// costToReachFloor calculates a simple cost metric for reaching a floor
// Lower cost = better candidate
// This is a placeholder; in Phase 2+ use more sophisticated cost functions
func (oa *OrderAssigner) costToReachFloor(targetFloor int, state ElevatorState) int {
	// Simple distance metric
	distanceToFloor := int(math.Abs(float64(state.Floor - targetFloor)))

	// Add penalty if elevator is moving away
	directionPenalty := 0
	if state.Direction == 1 && targetFloor < state.Floor {
		directionPenalty = oa.numFloors // High penalty if going up but target below
	} else if state.Direction == -1 && targetFloor > state.Floor {
		directionPenalty = oa.numFloors // High penalty if going down but target above
	}

	// Total cost
	return distanceToFloor + directionPenalty
}

// UpdateOtherElevatorState updates the state of another elevator
// Called from network module in Fase 2
func (oa *OrderAssigner) UpdateOtherElevatorState(state ElevatorState) {
	fmt.Printf("[OrderAssigner-%d] update elevator %d: floor=%d dir=%d alive=%v\n",
		oa.elevatorID, state.ID, state.Floor, state.Direction, state.Alive)

	oa.otherElevs[state.ID] = state

	// Re-evaluate hall orders
	oa.tryAssignHallOrders()
}

// MarkElevatorDead marks an elevator as non-responsive
// Called from failure detector in Fase 2
func (oa *OrderAssigner) MarkElevatorDead(elevatorID int) {
	if state, exists := oa.otherElevs[elevatorID]; exists {
		fmt.Printf("[OrderAssigner-%d] marking elevator %d as DEAD\n", oa.elevatorID, elevatorID)
		state.Alive = false
		oa.otherElevs[elevatorID] = state

		// Re-evaluate hall orders - maybe we need to take some we didn't before
		oa.tryAssignHallOrders()
	}
}

// GetMyState returns the current state of this elevator
// Can be called by network module to broadcast
func (oa *OrderAssigner) GetMyState() ElevatorState {
	return oa.myState
}
