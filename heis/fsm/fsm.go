package fsm

import (
	"fmt"
	"time"

	"Driver-go/elevio"
)

const DoorOpenDur = 3 * time.Second

type Order struct {
	Floor  int
	Button elevio.ButtonType
}

type StateUpdate struct {
	Floor     int
	Direction int
	Orders    [][]bool
}

type State int

const (
	ST_Idle State = iota
	ST_Moving
	ST_DoorOpen
)

type Dir int

const (
	DIR_Up   Dir = 1
	DIR_Down Dir = -1
	DIR_Stop Dir = 0
)

type Elevator struct {
	state      State
	floor      int
	dir        Dir
	lastDir    Dir
	obstructed bool
	doorTimer  *time.Timer

	orders          [][]bool
	stateCh         chan<- StateUpdate
	floorServedTxCh chan<- int
}

func Run(numFloors int,
	btnCh <-chan elevio.ButtonEvent,
	floorCh <-chan int,
	obstrCh <-chan bool,
	orderCh <-chan Order,
	stateCh chan<- StateUpdate,
	floorServedTxCh chan<- int,
) {
	e := Elevator{
		state:           ST_Idle,
		floor:           0,
		dir:             DIR_Stop,
		lastDir:         DIR_Up,
		orders:          make([][]bool, numFloors),
		stateCh:         stateCh,
		floorServedTxCh: floorServedTxCh,
	}
	for f := 0; f < numFloors; f++ {
		e.orders[f] = make([]bool, 3)
	}

	fmt.Printf("[FSM] start floors=%d\n", numFloors)

	elevio.SetMotorDirection(elevio.MD_Stop)
	elevio.SetDoorOpenLamp(false)
	elevio.SetFloorIndicator(0)

	// Initialize all button lamps to OFF
	for f := 0; f < numFloors; f++ {
		for bt := elevio.ButtonType(0); bt < 3; bt++ {
			elevio.SetButtonLamp(bt, f, false)
		}
	}

	// Init obstruction state (nice-to-have)
	e.obstructed = elevio.GetObstruction()
	if e.obstructed {
		fmt.Println("[OBSTR] active at startup -> motor inhibited")
	}

	// Init: hvis mellom etasjer, kjør ned til vi treffer en etasje (kun hvis ikke obstruert)
	if elevio.GetFloor() == -1 && !e.obstructed {
		fmt.Println("[INIT] between floors -> moving down to find floor")
		e.state = ST_Moving
		e.dir = DIR_Down
		e.lastDir = DIR_Down
		elevio.SetMotorDirection(elevio.MD_Down)
	}

	for {
		// Create a dynamic door timeout channel
		var doorTimeoutCh <-chan time.Time
		if e.doorTimer != nil {
			doorTimeoutCh = e.doorTimer.C
		}

		select {
		case b := <-btnCh:
			onButton(&e, b)

		case f := <-floorCh:
			onFloor(&e, f)

		case o := <-obstrCh:
			onObstruction(&e, o)

		case ord := <-orderCh:
			onExternalOrder(&e, ord)

		case <-doorTimeoutCh:
			onDoorTimeout(&e)
		}
	}
}

func onButton(e *Elevator, b elevio.ButtonEvent) {
	fmt.Printf("[BTN] %+v\n", b)

	// lagre alltid bestilling (“queue”)
	e.orders[b.Floor][b.Button] = true
	elevio.SetButtonLamp(b.Button, b.Floor, true)

	// Hvis vi står stille: prøv å starte (men startOrStayIdle sjekker obstruction)
	if e.state == ST_Idle {
		startOrStayIdle(e)
	}
}

func onFloor(e *Elevator, f int) {
	e.floor = f
	elevio.SetFloorIndicator(f)
	fmt.Printf("[FLOOR] %d state=%s dir=%s obstr=%v\n", f, stateToStr(e.state), dirToStr(e.dir), e.obstructed)

	// Send state update
	sendStateUpdate(e)

	// Hvis vi init-kjører for å finne etasje og det ikke finnes ordre: gå idle
	if e.state == ST_Moving && !hasAnyOrders(e) {
		fmt.Println("[INIT] found floor, no orders -> STOP + IDLE")
		elevio.SetMotorDirection(elevio.MD_Stop)
		e.state = ST_Idle
		e.dir = DIR_Stop
		return
	}

	if e.state != ST_Moving {
		return
	}

	// hvis obstruert: stopp alltid (sikkerhet)
	if e.obstructed {
		fmt.Println("[OBSTR] active -> STOP motor")
		elevio.SetMotorDirection(elevio.MD_Stop)
		e.state = ST_Idle
		e.dir = DIR_Stop
		return
	}

	if shouldStop(e, f) {
		fmt.Println("[STOP] stopping here")
		elevio.SetMotorDirection(elevio.MD_Stop)

		// Save current direction BEFORE setting to STOP
		directionWhenStopped := e.dir

		e.state = ST_DoorOpen
		e.dir = DIR_Stop

		elevio.SetDoorOpenLamp(true)
		fmt.Println("[DOOR] OPEN")

		clearOrdersAtFloor(e, f, int(directionWhenStopped))

		// Broadcast to all elevators that this floor has been served
		select {
		case e.floorServedTxCh <- f:
			fmt.Printf("[FSM] Broadcasted floor served: %d\n", f)
		default:
			fmt.Printf("[FSM] Floor served channel full, dropped signal for floor %d\n", f)
		}

		// Start door timer - non-blocking
		if e.doorTimer != nil {
			e.doorTimer.Stop()
		}
		e.doorTimer = time.NewTimer(DoorOpenDur)
	}
}

func onObstruction(e *Elevator, active bool) {
	e.obstructed = active
	fmt.Printf("[OBSTR] %v\n", active)

	if active {
		// Obstruksjon aktiv: sett dør-åpen-lys PÅ og stopp motor
		elevio.SetDoorOpenLamp(true)
		fmt.Println("[DOOR LIGHT] ON (obstruction active)")
		elevio.SetMotorDirection(elevio.MD_Stop)
		fmt.Println("[MOTOR] STOP (obstruction)")

		// sett til idle slik at vi kan starte igjen når obstruction slipper
		if e.state == ST_Moving {
			e.state = ST_Idle
			e.dir = DIR_Stop
		}
		return
	}

	// Obstruksjon ble slått av: start timer for å slukke dør-lyset etter 3 sekunder
	fmt.Println("[OBSTR] cleared -> door light will turn off in 3 seconds")
	if e.doorTimer != nil {
		e.doorTimer.Stop()
	}
	e.doorTimer = time.NewTimer(DoorOpenDur)

	// Hvis vi står idle og har ordre, fortsett
	if e.state == ST_Idle && hasAnyOrders(e) {
		fmt.Println("[OBSTR] cleared -> resume if orders exist")
		startOrStayIdle(e)
	}
}

func onExternalOrder(e *Elevator, ord Order) {
	fmt.Printf("[EXTERNAL ORDER] floor=%d button=%d\n", ord.Floor, ord.Button)

	if ord.Floor < 0 || ord.Floor >= len(e.orders) {
		fmt.Println("[ERROR] Invalid floor in external order")
		return
	}

	// Legg til ordre hvis det ikke allerede finnes
	if !e.orders[ord.Floor][ord.Button] {
		e.orders[ord.Floor][ord.Button] = true

		// SET BUTTON LAMP ONLY FOR CAB BUTTONS
		// Hall order lights (button 0 og 1) handled globally by main's HallLightMgr
		// CAB light (button 2) handled locally by FSM
		if ord.Button == 2 { // BT_Cab
			elevio.SetButtonLamp(ord.Button, ord.Floor, true)
		}
	}

	// Hvis vi står stille: prøv å starte
	if e.state == ST_Idle {
		startOrStayIdle(e)
	}
}

// Helper function to safely get door timer channel
func onDoorTimeout(e *Elevator) {
	// Hvis obstruksjon er aktiv, ignorer timeout - lyset skal forbli PÅ!
	if e.obstructed {
		fmt.Println("[TIMEOUT] ignored - obstruction still active, door light stays ON")
		// Restart timer så vi prøver igjen senere
		if e.doorTimer != nil {
			e.doorTimer.Stop()
		}
		e.doorTimer = time.NewTimer(1 * time.Second) // Check again soon
		return
	}

	if e.state == ST_DoorOpen {
		// Full dør-sekvens: lukkdøren og gå tilbake til IDLE
		fmt.Println("[DOOR] CLOSED")
		elevio.SetDoorOpenLamp(false)

		e.state = ST_Idle
		startOrStayIdle(e)
	} else {
		// Obstruksjon-timeout: bare slukk lyset
		fmt.Println("[DOOR LIGHT] OFF (obstruction timeout)")
		elevio.SetDoorOpenLamp(false)
	}
}

//superirriterende git

func startOrStayIdle(e *Elevator) {
	// Viktig: aldri start motor hvis obstruert
	if e.obstructed {
		fmt.Println("[STATE] blocked by obstruction -> stay IDLE")
		e.state = ST_Idle
		e.dir = DIR_Stop
		elevio.SetMotorDirection(elevio.MD_Stop)
		return
	}

	next := chooseDirection(e)

	if next == DIR_Stop {
		fmt.Println("[STATE] IDLE (no orders)")
		e.state = ST_Idle
		e.dir = DIR_Stop
		elevio.SetMotorDirection(elevio.MD_Stop)
		return
	}

	// Heisen skal kjøre - slå av dør-lyset hvis det står på
	elevio.SetDoorOpenLamp(false)

	e.state = ST_Moving
	e.dir = next
	e.lastDir = next

	if next == DIR_Up {
		fmt.Println("[MOTOR] UP")
		elevio.SetMotorDirection(elevio.MD_Up)
	} else {
		fmt.Println("[MOTOR] DOWN")
		elevio.SetMotorDirection(elevio.MD_Down)
	}

	// Send state update
	sendStateUpdate(e)
}

/*** Queue policy ***/

func shouldStop(e *Elevator, floor int) bool {
	if e.orders[floor][elevio.BT_Cab] {
		return true
	}

	switch e.dir {
	case DIR_Up:
		if e.orders[floor][elevio.BT_HallUp] {
			return true
		}
		if e.orders[floor][elevio.BT_HallDown] && !hasOrdersAbove(e, floor) {
			return true
		}
	case DIR_Down:
		if e.orders[floor][elevio.BT_HallDown] {
			return true
		}
		if e.orders[floor][elevio.BT_HallUp] && !hasOrdersBelow(e, floor) {
			return true
		}
	case DIR_Stop:
		return anyOrderAtFloor(e, floor)
	}
	return false
}

// Smartere: fortsett i samme retning hvis mulig
func chooseDirection(e *Elevator) Dir {
	switch e.lastDir {
	case DIR_Up:
		if hasOrdersAbove(e, e.floor) {
			return DIR_Up
		}
		if hasOrdersBelow(e, e.floor) {
			return DIR_Down
		}
	case DIR_Down:
		if hasOrdersBelow(e, e.floor) {
			return DIR_Down
		}
		if hasOrdersAbove(e, e.floor) {
			return DIR_Up
		}
	default:
		if hasOrdersAbove(e, e.floor) {
			return DIR_Up
		}
		if hasOrdersBelow(e, e.floor) {
			return DIR_Down
		}
	}

	if anyOrderAtFloor(e, e.floor) {
		return DIR_Stop
	}
	return DIR_Stop
}

/*** Helpers ***/

func clearOrdersAtFloor(e *Elevator, floor int, direction int) {
	fmt.Printf("[CLEAR] floor=%d dir=%d\n", floor, direction)

	numFloors := len(e.orders)

	// CAB buttons (BT_Cab=2) slettes alltid
	if e.orders[floor][2] {
		e.orders[floor][2] = false
		elevio.SetButtonLamp(2, floor, false)
		fmt.Printf("  - Cleared CAB\n")
	}

	// Hall buttons (BT_HallUp=0, BT_HallDown=1) håndteres etter etasje

	if floor == 0 { // Nedeterste etasje: kun "opp"-knapp finnes
		if e.orders[floor][0] { // BT_HallUp
			e.orders[floor][0] = false
			// HallLightMgr styrer lysene basert på state-updates, ikke FSM
			fmt.Printf("  - Cleared HallUp from orders (bottom floor)\n")
		}
	} else if floor == numFloors-1 { // Øverste etasje: kun "ned"-knapp finnes
		if e.orders[floor][1] { // BT_HallDown
			e.orders[floor][1] = false
			// HallLightMgr styrer lysene basert på state-updates, ikke FSM
			fmt.Printf("  - Cleared HallDown from orders (top floor)\n")
		}
	} else { // Midtetasjer: begge knapper kan være tilstede, slett basert på retning
		if direction == 1 { // Heisen beveger seg oppover
			if e.orders[floor][0] { // BT_HallUp
				e.orders[floor][0] = false
				// HallLightMgr styrer lysene basert på state-updates, ikke FSM
				fmt.Printf("  - Cleared HallUp from orders (moving up)\n")
			}
		} else if direction == -1 { // Heisen beveger seg nedover
			if e.orders[floor][1] { // BT_HallDown
				e.orders[floor][1] = false
				// HallLightMgr styrer lysene basert på state-updates, ikke FSM
				fmt.Printf("  - Cleared HallDown from orders (moving down)\n")
			}
		}
	}
}

func hasAnyOrders(e *Elevator) bool {
	for f := 0; f < len(e.orders); f++ {
		if anyOrderAtFloor(e, f) {
			return true
		}
	}
	return false
}

func anyOrderAtFloor(e *Elevator, floor int) bool {
	for bt := 0; bt < 3; bt++ {
		if e.orders[floor][bt] {
			return true
		}
	}
	return false
}

func hasOrdersAbove(e *Elevator, floor int) bool {
	for f := floor + 1; f < len(e.orders); f++ {
		if anyOrderAtFloor(e, f) {
			return true
		}
	}
	return false
}

func hasOrdersBelow(e *Elevator, floor int) bool {
	for f := 0; f < floor; f++ {
		if anyOrderAtFloor(e, f) {
			return true
		}
	}
	return false
}

// sendStateUpdate sends the current elevator state to the order assigner
// Uses non-blocking send to avoid deadlock
func sendStateUpdate(e *Elevator) {
	if e.stateCh == nil {
		return
	}

	// Create a copy of orders slice
	ordersCopy := make([][]bool, len(e.orders))
	for i := range e.orders {
		ordersCopy[i] = make([]bool, len(e.orders[i]))
		copy(ordersCopy[i], e.orders[i])
	}

	update := StateUpdate{
		Floor:     e.floor,
		Direction: int(e.dir),
		Orders:    ordersCopy,
	}

	// Non-blocking send
	select {
	case e.stateCh <- update:
		// State sent successfully
	default:
		// Channel full or not ready - don't block FSM
		fmt.Printf("[FSM] state update channel not ready (full), skipping\n")
	}
}

func stateToStr(s State) string {
	switch s {
	case ST_Idle:
		return "IDLE"
	case ST_Moving:
		return "MOVING"
	case ST_DoorOpen:
		return "DOOR_OPEN"
	default:
		return "UNKNOWN"
	}
}

func dirToStr(d Dir) string {
	switch d {
	case DIR_Up:
		return "UP"
	case DIR_Down:
		return "DOWN"
	case DIR_Stop:
		return "STOP"
	default:
		return "?"
	}
}
