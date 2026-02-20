package fsm

import (
	"fmt"
	"time"

	"Driver-go/elevio"
)

const DoorOpenDur = 3 * time.Second

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

	orders [][]bool
}

func Run(numFloors int,
	btnCh <-chan elevio.ButtonEvent,
	floorCh <-chan int,
	obstrCh <-chan bool,
) {
	e := Elevator{
		state:   ST_Idle,
		floor:   0,
		dir:     DIR_Stop,
		lastDir: DIR_Up,
		orders:  make([][]bool, numFloors),
	}
	for f := 0; f < numFloors; f++ {
		e.orders[f] = make([]bool, 3)
	}

	fmt.Printf("[FSM] start floors=%d\n", numFloors)

	elevio.SetMotorDirection(elevio.MD_Stop)
	elevio.SetDoorOpenLamp(false)
	elevio.SetFloorIndicator(0)

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
		select {
		case b := <-btnCh:
			onButton(&e, b)

		case f := <-floorCh:
			onFloor(&e, f)

		case o := <-obstrCh:
			onObstruction(&e, o)
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

		e.state = ST_DoorOpen
		e.dir = DIR_Stop

		elevio.SetDoorOpenLamp(true)
		fmt.Println("[DOOR] OPEN")

		clearOrdersAtFloor(e, f)

		// OBS: Denne sleepen blokkerer event-loop.
		// Det betyr at obstruction-events ikke behandles mens døra “står åpen”.
		// For en enkel første versjon er det OK, men senere bør du bruke timer-kanal.
		time.Sleep(DoorOpenDur)

		elevio.SetDoorOpenLamp(false)
		fmt.Println("[DOOR] CLOSED")

		e.state = ST_Idle
		startOrStayIdle(e)
	}
}

func onObstruction(e *Elevator, active bool) {
	e.obstructed = active
	fmt.Printf("[OBSTR] %v\n", active)

	if active {
		// Kravet ditt: aldri kjøre når obstruksjon er på
		elevio.SetMotorDirection(elevio.MD_Stop)
		fmt.Println("[MOTOR] STOP (obstruction)")
		// sett til idle slik at vi kan starte igjen når obstruction slipper
		if e.state == ST_Moving {
			e.state = ST_Idle
			e.dir = DIR_Stop
		}
		return
	}

	// obstruction ble slått av: hvis vi står idle og har ordre, fortsett
	if e.state == ST_Idle && hasAnyOrders(e) {
		fmt.Println("[OBSTR] cleared -> resume if orders exist")
		startOrStayIdle(e)
	}
}

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

func clearOrdersAtFloor(e *Elevator, floor int) {
	fmt.Printf("[CLEAR] floor=%d\n", floor)
	for bt := elevio.ButtonType(0); bt < 3; bt++ {
		if e.orders[floor][bt] {
			e.orders[floor][bt] = false
			elevio.SetButtonLamp(bt, floor, false)
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
