package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

func init() {
	tools["snake"] = func([]string) error { return play(snake) }
	tools["tictactoe"] = func([]string) error { return play(tictactoe) }
}

// pane is a raw terminal that repaints only the lines that changed.
type pane struct {
	keys chan string
	last []string
}

func (p *pane) size() (w, h int) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 80, 24
	}
	return w, h
}

func (p *pane) paint(lines []string) {
	var b strings.Builder
	for i, l := range lines {
		if i < len(p.last) && p.last[i] == l {
			continue
		}
		fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[K", i+1, l)
	}
	for i := len(lines); i < len(p.last); i++ {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[K", i+1)
	}
	p.last = lines
	if b.Len() > 0 {
		os.Stdout.WriteString(b.String())
	}
}

// readKeys turns stdin into key names; an escape sequence is one key ("up", "down", "right", "left").
func readKeys(keys chan<- string) {
	buf := make([]byte, 16)
	arrows := map[byte]string{'A': "up", 'B': "down", 'C': "right", 'D': "left"}
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			close(keys)
			return
		}
		for i := 0; i < n; i++ {
			switch {
			case buf[i] == 0x1b && i+2 < n && (buf[i+1] == '[' || buf[i+1] == 'O'):
				if k, ok := arrows[buf[i+2]]; ok {
					keys <- k
				}
				i += 2
			case buf[i] == 3:
				keys <- "q"
			case buf[i] == '\r' || buf[i] == '\n':
				keys <- "enter"
			default:
				keys <- strings.ToLower(string(buf[i]))
			}
		}
	}
}

// game draws itself and handles one key; tick is the interval it wants (0: it waits for keys, using no CPU).
type game interface {
	view(w, h int) []string
	key(k string) (quit bool)
	tick() time.Duration
	step()
}

func play(newGame func() game) error {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("a game needs a terminal: %w", err)
	}
	defer term.Restore(fd, old)
	os.Stdout.WriteString("\x1b[?25l\x1b[2J")
	defer os.Stdout.WriteString("\x1b[?25h\x1b[0m\x1b[2J\x1b[H")

	p := &pane{keys: make(chan string, 64)}
	go readKeys(p.keys)
	g := newGame()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	var next time.Time // a held key must not push the next step back
	for {
		w, h := p.size()
		p.paint(g.view(w, h))
		timer.Stop()
		if d := g.tick(); d > 0 {
			if next.IsZero() {
				next = time.Now().Add(d)
			}
			timer.Reset(max(0, time.Until(next)))
		} else {
			next = time.Time{}
		}
		select {
		case k, ok := <-p.keys:
			if !ok || g.key(k) {
				return nil
			}
		case <-timer.C:
			next = time.Time{}
			g.step()
		}
	}
}

const (
	reverse = "\x1b[1;30;47m" // explicit colours: reverse video turns grey on a themed background
	hint    = "\x1b[97m"
	dim     = "\x1b[2m"
	reset   = "\x1b[0m"
)

type snakeGame struct {
	w, h             int
	body             [][2]int
	dx, dy, ndx, ndy int
	food             [2]int
	score            int
	over, paused     bool
	fast             bool
	started          bool
}

func snake() game {
	s := &snakeGame{w: 30, h: 16}
	s.restart()
	return s
}

func (s *snakeGame) restart() {
	s.body = [][2]int{{s.w/2 - 3, s.h / 2}, {s.w/2 - 2, s.h / 2}, {s.w/2 - 1, s.h / 2}, {s.w / 2, s.h / 2}}
	s.dx, s.dy, s.ndx, s.ndy = 1, 0, 1, 0
	s.score, s.over, s.paused, s.started = 0, false, false, false
	s.place()
}

func (s *snakeGame) on(x, y int) bool {
	for _, c := range s.body {
		if c[0] == x && c[1] == y {
			return true
		}
	}
	return false
}

func (s *snakeGame) place() {
	for {
		s.food = [2]int{rand.IntN(s.w), rand.IntN(s.h)}
		if !s.on(s.food[0], s.food[1]) {
			return
		}
	}
}

// tick is 0 until the first key, while paused and after game over: an idle game costs nothing.
func (s *snakeGame) tick() time.Duration {
	if !s.started || s.paused || s.over {
		return 0
	}
	if s.fast {
		return 55 * time.Millisecond
	}
	return 110 * time.Millisecond
}

func (s *snakeGame) step() {
	s.dx, s.dy = s.ndx, s.ndy
	head := s.body[len(s.body)-1]
	x, y := head[0]+s.dx, head[1]+s.dy
	if x < 0 || y < 0 || x >= s.w || y >= s.h || s.on(x, y) {
		s.over = true
		return
	}
	s.body = append(s.body, [2]int{x, y})
	if x == s.food[0] && y == s.food[1] {
		s.score++
		s.place()
		return
	}
	s.body = s.body[1:]
}

func (s *snakeGame) key(k string) bool {
	turn := func(dx, dy int) {
		if dx != -s.dx || dy != -s.dy {
			s.ndx, s.ndy = dx, dy
		}
		s.started = true
	}
	switch k {
	case "up", "w", "k":
		turn(0, -1)
	case "down", "s", "j":
		turn(0, 1)
	case "right", "d", "l":
		turn(1, 0)
	case "left", "a", "h":
		turn(-1, 0)
	case "p":
		s.paused = !s.paused
	case " ":
		s.fast = !s.fast
	case "r":
		s.restart()
	case "q":
		return true
	}
	return false
}

func (s *snakeGame) view(w, h int) []string {
	s.w, s.h = min(40, max(8, (w-2)/2)), min(20, max(6, h-3))
	status := "snake  score " + fmt.Sprint(s.score) + "   arrows/wasd/hjkl · space speed · p pause · r restart · q quit"
	switch {
	case s.over:
		status = fmt.Sprintf("game over, score %d   r restart · q quit", s.score)
	case s.paused:
		status = "paused   p resume"
	case !s.started:
		status = "snake   press an arrow key to start · q quit"
	}
	cells := make([][]string, s.h)
	for y := range cells {
		cells[y] = make([]string, s.w)
		for x := range cells[y] {
			cells[y][x] = "  "
		}
	}
	if s.food[0] < s.w && s.food[1] < s.h {
		cells[s.food[1]][s.food[0]] = "\x1b[31m()\x1b[32m"
	}
	for _, c := range s.body {
		if c[0] < s.w && c[1] < s.h {
			cells[c[1]][c[0]] = "██"
		}
	}
	bar := strings.Repeat("─", s.w*2)
	lines := []string{fmt.Sprintf("%s%-*s%s", reverse, s.w*2+2, status, reset), "┌" + bar + "┐"}
	for _, row := range cells {
		lines = append(lines, "│\x1b[32m"+strings.Join(row, "")+reset+"│")
	}
	return append(lines, "└"+bar+"┘")
}

var ticLines = [8][3]int{{0, 1, 2}, {3, 4, 5}, {6, 7, 8}, {0, 3, 6}, {1, 4, 7}, {2, 5, 8}, {0, 4, 8}, {2, 4, 6}}

type ticGame struct {
	board         [9]byte
	cursor        int
	turn          byte
	twoPlayer     bool
	over          bool
	winner        byte
	win           [3]int
	sx, so, draws int
}

func tictactoe() game {
	t := &ticGame{}
	t.reset()
	return t
}

func (t *ticGame) reset() {
	t.board = [9]byte{}
	for i := range t.board {
		t.board[i] = ' '
	}
	t.cursor, t.turn, t.over, t.winner, t.win = 4, 'X', false, 0, [3]int{-1, -1, -1}
}

func (t *ticGame) tick() time.Duration { return 0 }
func (t *ticGame) step()               {}

func (t *ticGame) check() {
	for _, l := range ticLines {
		if c := t.board[l[0]]; c != ' ' && c == t.board[l[1]] && c == t.board[l[2]] {
			t.over, t.winner, t.win = true, c, l
			return
		}
	}
	for _, c := range t.board {
		if c == ' ' {
			return
		}
	}
	t.over, t.winner = true, 'd'
}

func (t *ticGame) finish() {
	switch t.winner {
	case 'X':
		t.sx++
	case 'O':
		t.so++
	case 'd':
		t.draws++
	}
}

// cpu wins if it can, else blocks, else takes the centre, a corner, any cell.
func (t *ticGame) cpu() {
	find := func(who byte) int {
		for _, l := range ticLines {
			n, free := 0, -1
			for _, i := range l {
				switch t.board[i] {
				case who:
					n++
				case ' ':
					free = i
				}
			}
			if n == 2 && free >= 0 {
				return free
			}
		}
		return -1
	}
	pick := find('O')
	if pick < 0 {
		pick = find('X')
	}
	if pick < 0 && t.board[4] == ' ' {
		pick = 4
	}
	for _, set := range [][]int{{0, 2, 6, 8}, {1, 3, 5, 7}} {
		if pick >= 0 {
			break
		}
		var free []int
		for _, i := range set {
			if t.board[i] == ' ' {
				free = append(free, i)
			}
		}
		if len(free) > 0 {
			pick = free[rand.IntN(len(free))]
		}
	}
	if pick >= 0 {
		t.board[pick] = 'O'
	}
}

func (t *ticGame) place(i int) {
	if t.over || t.board[i] != ' ' {
		return
	}
	t.board[i] = t.turn
	t.check()
	if !t.over && !t.twoPlayer {
		t.turn = 'O'
		t.cpu()
		t.check()
	}
	if t.over {
		t.finish()
		return
	}
	t.turn = map[byte]byte{'X': 'O', 'O': 'X'}[t.turn]
	if !t.twoPlayer {
		t.turn = 'X'
	}
}

func (t *ticGame) key(k string) bool {
	switch k {
	case "up", "w", "k":
		if t.cursor >= 3 {
			t.cursor -= 3
		}
	case "down", "s", "j":
		if t.cursor <= 5 {
			t.cursor += 3
		}
	case "left", "a", "h":
		if t.cursor%3 > 0 {
			t.cursor--
		}
	case "right", "d", "l":
		if t.cursor%3 < 2 {
			t.cursor++
		}
	case " ", "enter":
		t.place(t.cursor)
	case "m":
		t.twoPlayer = !t.twoPlayer
		t.reset()
	case "r":
		t.reset()
	case "q":
		return true
	default:
		if len(k) == 1 && k[0] >= '1' && k[0] <= '9' {
			t.cursor = int(k[0] - '1')
			t.place(t.cursor)
		}
	}
	return false
}

func (t *ticGame) cell(i int) string {
	v := string(t.board[i])
	switch {
	case i == t.win[0] || i == t.win[1] || i == t.win[2]:
		v = "\x1b[1;42;37m" + v + reset
	case v == "X":
		v = "\x1b[36;1mX" + reset
	case v == "O":
		v = "\x1b[33;1mO" + reset
	case !t.over && i == t.cursor:
		v = dim + "·" + reset
	}
	if i == t.cursor {
		return reverse + " " + v + " " + reset
	}
	return " " + v + " "
}

func (t *ticGame) view(int, int) []string {
	mode := "vs CPU"
	if t.twoPlayer {
		mode = "2-Player"
	}
	status := fmt.Sprintf("turn: %c   arrows/wasd/1-9 move · space/enter place · m mode · r reset · q quit", t.turn)
	if t.over {
		status = "game over: " + string(t.winner) + " wins!   r restart · m mode · q quit"
		if t.winner == 'd' {
			status = "game over: draw!   r restart · m mode · q quit"
		}
	}
	row := func(a int) string {
		return fmt.Sprintf("       │ %s │ %s │ %s │", t.cell(a), t.cell(a+1), t.cell(a+2))
	}
	return []string{
		fmt.Sprintf("%s%-46s%s", reverse, fmt.Sprintf(" Tic-Tac-Toe  [%s]  X: %d  O: %d  Ties: %d", mode, t.sx, t.so, t.draws), reset),
		hint + fmt.Sprintf("%-46s", " "+status) + reset,
		"",
		"       ┌─────┬─────┬─────┐", row(0),
		"       ├─────┼─────┼─────┤", row(3),
		"       ├─────┼─────┼─────┤", row(6),
		"       └─────┴─────┴─────┘",
		"",
		hint + "   arrows/wasd/hjkl or 1-9 to move · space/enter to place · r restart" + reset,
	}
}
