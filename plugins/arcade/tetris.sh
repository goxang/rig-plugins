#!/usr/bin/env bash
# Tetris for a terminal pane: arrows/WASD/hjkl move, up/x rotate, z rotate back, space hard drop, c hold, p pause, r restart, q quit.
set -u

W=10 H=20 CW=40 CH=24
E=$'\e'
RS="${E}[0m" DIM="${E}[2m" BD="${E}[1m" GR="${E}[38;5;244m"

cache="${XDG_CACHE_HOME:-$HOME/.cache}/rig-arcade"
best_score=0
[[ -r $cache/tetris.best ]] && read -r best_score < "$cache/tetris.best"
[[ $best_score =~ ^[0-9]+$ ]] || best_score=0
save_best() { { mkdir -p "$cache" && printf '%s\n' "$best_score" > "$cache/tetris.best"; } 2>/dev/null; }

saved_stty=$(stty -g 2>/dev/null || true)
cleanup() { save_best; printf '%s[?25h%s[0m%s[2J%s[H' "$E" "$E" "$E" "$E"; stty "${saved_stty:-sane}" 2>/dev/null || stty sane 2>/dev/null || true; }
trap cleanup EXIT
trap 'exit 0' INT TERM HUP
trap 'sizechg=1' WINCH
stty -echo -icanon min 1 time 0 2>/dev/null || true
printf '%s[?25l%s[2J' "$E" "$E"

rows=24 cols=80 sizechg=1
declare -a board=() L=() bag=() bx=(0 0 0 0) by=(0 0 0 0)
score=0 lines_total=0 level=0 over=0 paused=0 ready=1
cur_shape=0 cur_rot=0 cur_x=3 cur_y=0 ghost_y=0 preview_shape=0
hold_shape=-1 hold_used=0 next_drop=0 iv=800 now=0 nxt=0 cc="" b="" key=""
ivs=(800 720 640 560 480 400 330 270 220 180 150 120 100 85 70)

get_blocks() {
  case "$1-$2" in
    0-0) bx=(0 1 2 3); by=(1 1 1 1) ;;
    0-1) bx=(2 2 2 2); by=(0 1 2 3) ;;
    0-2) bx=(0 1 2 3); by=(2 2 2 2) ;;
    0-3) bx=(1 1 1 1); by=(0 1 2 3) ;;

    1-0) bx=(0 0 1 2); by=(0 1 1 1) ;;
    1-1) bx=(1 2 1 1); by=(0 0 1 2) ;;
    1-2) bx=(0 1 2 2); by=(1 1 1 2) ;;
    1-3) bx=(1 1 0 1); by=(0 1 2 2) ;;

    2-0) bx=(2 0 1 2); by=(0 1 1 1) ;;
    2-1) bx=(1 1 1 2); by=(0 1 2 2) ;;
    2-2) bx=(0 1 2 0); by=(1 1 1 2) ;;
    2-3) bx=(0 1 1 1); by=(0 0 1 2) ;;

    3-*) bx=(1 2 1 2); by=(0 0 1 1) ;;

    4-0) bx=(1 2 0 1); by=(0 0 1 1) ;;
    4-1) bx=(1 1 2 2); by=(0 1 1 2) ;;
    4-2) bx=(1 2 0 1); by=(1 1 2 2) ;;
    4-3) bx=(0 0 1 1); by=(0 1 1 2) ;;

    5-0) bx=(1 0 1 2); by=(0 1 1 1) ;;
    5-1) bx=(1 1 2 1); by=(0 1 1 2) ;;
    5-2) bx=(0 1 2 1); by=(1 1 1 2) ;;
    5-3) bx=(1 0 1 1); by=(0 1 1 2) ;;

    6-0) bx=(0 1 1 2); by=(0 0 1 1) ;;
    6-1) bx=(2 1 2 1); by=(0 1 1 2) ;;
    6-2) bx=(0 1 1 2); by=(1 1 2 2) ;;
    6-3) bx=(1 0 1 0); by=(0 1 1 2) ;;
  esac
}

cell_color() { # cell value (shape+1) -> cc
  local n
  case $1 in 1) n=51 ;; 2) n=33 ;; 3) n=208 ;; 4) n=226 ;; 5) n=46 ;; 6) n=135 ;; *) n=196 ;; esac
  cc="${E}[1;38;5;${n}m"
}

next_shape() { # 7-bag: every piece shows up once before any repeats
  if (( ${#bag[@]} == 0 )); then
    bag=(0 1 2 3 4 5 6)
    local i j t
    for ((i = 6; i > 0; i--)); do j=$((RANDOM % (i + 1))); t=${bag[i]}; bag[i]=${bag[j]}; bag[j]=$t; done
  fi
  nxt=${bag[0]}
  bag=("${bag[@]:1}")
}

now_ms() {
  if [[ -n ${EPOCHREALTIME-} ]]; then
    local t=${EPOCHREALTIME/./}
    now=$(( t / 1000 ))
  else
    now=$(( $(date +%s%N) / 1000000 ))
  fi
}

start_clock() {
  now_ms
  iv=${ivs[level > 14 ? 14 : level]}
  next_drop=$(( now + iv ))
}

add_score() {
  score=$(( score + $1 ))
  (( score > best_score )) && best_score=$score
}

collides() { # x y rot
  get_blocks "$cur_shape" "$3"
  local i gx gy
  for ((i = 0; i < 4; i++)); do
    gx=$(( $1 + bx[i] )) gy=$(( $2 + by[i] ))
    (( gx < 0 || gx >= W || gy >= H )) && return 0
    (( gy >= 0 && board[gy * W + gx] != 0 )) && return 0
  done
  return 1
}

calc_ghost_y() {
  ghost_y=$cur_y
  while ! collides "$cur_x" "$((ghost_y + 1))" "$cur_rot"; do ghost_y=$((ghost_y + 1)); done
}

spawn_piece() {
  cur_shape=$preview_shape
  next_shape
  preview_shape=$nxt
  cur_rot=0 cur_x=3 cur_y=0
  calc_ghost_y
  if collides "$cur_x" "$cur_y" "$cur_rot"; then
    over=1
    save_best
  fi
}

clear_lines() {
  local y x full cleared=0
  local -a temp=()
  for ((y = H - 1; y >= 0; y--)); do
    full=1
    for ((x = 0; x < W; x++)); do
      (( board[y * W + x] == 0 )) && { full=0; break; }
    done
    if (( full )); then
      cleared=$((cleared + 1))
    else
      temp+=("${board[@]:y * W:W}")
    fi
  done
  (( cleared == 0 )) && return
  while (( ${#temp[@]} < W * H )); do temp+=(0); done
  local idx=0
  for ((y = H - 1; y >= 0; y--)); do
    for ((x = 0; x < W; x++)); do board[y * W + x]=${temp[idx]}; idx=$((idx + 1)); done
  done
  lines_total=$(( lines_total + cleared ))
  case $cleared in 1) add_score $((100 * (level + 1))) ;; 2) add_score $((300 * (level + 1))) ;; 3) add_score $((500 * (level + 1))) ;; *) add_score $((800 * (level + 1))) ;; esac
  level=$(( lines_total / 10 ))
}

lock_piece() {
  get_blocks "$cur_shape" "$cur_rot"
  local i gx gy
  for ((i = 0; i < 4; i++)); do
    gx=$(( cur_x + bx[i] )) gy=$(( cur_y + by[i] ))
    (( gy >= 0 && gy < H && gx >= 0 && gx < W )) && board[gy * W + gx]=$(( cur_shape + 1 ))
  done
  clear_lines
  hold_used=0
  spawn_piece
  start_clock
}

step_down() { # points per cell moved
  if collides "$cur_x" "$((cur_y + 1))" "$cur_rot"; then
    lock_piece
  else
    cur_y=$((cur_y + 1))
    add_score "$1"
  fi
}

hard_drop() {
  while ! collides "$cur_x" "$((cur_y + 1))" "$cur_rot"; do
    cur_y=$((cur_y + 1))
    add_score 2
  done
  lock_piece
}

move_x() {
  collides "$((cur_x + $1))" "$cur_y" "$cur_rot" && return
  cur_x=$((cur_x + $1))
  calc_ghost_y
}

try_rotate() { # quarter turns clockwise; small sideways kicks off walls and stacks
  local nrot=$(( (cur_rot + $1) % 4 )) k
  for k in 0 1 -1 2 -2; do
    if ! collides "$((cur_x + k))" "$cur_y" "$nrot"; then
      cur_x=$((cur_x + k)) cur_rot=$nrot
      calc_ghost_y
      return
    fi
  done
}

do_hold() {
  (( hold_used )) && return
  hold_used=1
  if (( hold_shape < 0 )); then
    hold_shape=$cur_shape
    spawn_piece
  else
    local t=$cur_shape
    cur_shape=$hold_shape hold_shape=$t
    cur_rot=0 cur_x=3 cur_y=0
    calc_ghost_y
  fi
}

reset() {
  board=()
  local i
  for ((i = 0; i < W * H; i++)); do board+=(0); done
  score=0 lines_total=0 level=0 over=0 paused=0 hold_shape=-1 hold_used=0 bag=()
  next_shape
  preview_shape=$nxt
  spawn_piece
}

bar() { # width style text -> b
  local l=$(( ($1 - ${#3}) / 2 ))
  printf -v b '%s%*s%s%*s%s' "$2" "$l" '' "$3" "$(( $1 - ${#3} - l ))" '' "$RS"
}

mini() { # shape (-1 for none) -> m1 m2, two 8-wide rows
  m1="" m2=""
  local -a g=(0 0 0 0 0 0 0 0)
  local i
  if (( $1 >= 0 )); then
    get_blocks "$1" 0
    for ((i = 0; i < 4; i++)); do (( by[i] < 2 && bx[i] < 4 )) && g[by[i] * 4 + bx[i]]=1; done
    cell_color $(( $1 + 1 ))
  fi
  for ((i = 0; i < 4; i++)); do
    if (( g[i] )); then m1+="${cc}██${RS}"; else m1+="  "; fi
    if (( g[4 + i] )); then m2+="${cc}██${RS}"; else m2+="  "; fi
  done
}

boxtop() { local s; printf -v s '%*s' "$((11 - ${#1}))" ''; btop="${GR}╭─ ${RS}${BD}$1${RS}${GR} ${s// /─}╮${RS}"; }

build() {
  local -a disp=("${board[@]}") ghost=()
  local i x y c line
  for ((i = 0; i < W * H; i++)); do ghost+=(0); done
  if (( !over && !ready )); then
    get_blocks "$cur_shape" "$cur_rot"
    for ((i = 0; i < 4; i++)); do
      x=$(( cur_x + bx[i] )) y=$(( ghost_y + by[i] ))
      (( y >= 0 && y < H && x >= 0 && x < W && disp[y * W + x] == 0 )) && ghost[y * W + x]=$(( cur_shape + 1 ))
    done
    for ((i = 0; i < 4; i++)); do
      x=$(( cur_x + bx[i] )) y=$(( cur_y + by[i] ))
      (( y >= 0 && y < H && x >= 0 && x < W )) && { disp[y * W + x]=$(( cur_shape + 1 )); ghost[y * W + x]=0; }
    done
  fi

  L=()
  L[0]="${GR}╭─ ${RS}${BD}${E}[38;5;51mTETRIS${RS}${GR} ───────────╮${RS}"
  for ((y = 0; y < H; y++)); do
    line="${GR}│${RS}"
    case "$ready$paused$over-$y" in
      1??-8)  bar 20 "${E}[1;30;46m" "T E T R I S"; line+=$b ;;
      1??-10) bar 20 "${E}[1;97;44m" "SPACE to start"; line+=$b ;;
      ?1?-9)  bar 20 "${E}[1;30;43m" "PAUSED"; line+=$b ;;
      ?1?-10) bar 20 "${E}[1;30;43m" "p to resume"; line+=$b ;;
      ??1-8)  bar 20 "${E}[1;97;41m" "GAME OVER"; line+=$b ;;
      ??1-9)  bar 20 "${E}[1;97;41m" "score $score"; line+=$b ;;
      ??1-10) bar 20 "${E}[1;97;41m" "r to play again"; line+=$b ;;
      *)
        for ((x = 0; x < W; x++)); do
          c=${disp[y * W + x]}
          if (( c )); then
            cell_color "$c"; line+="${cc}██${RS}"
          elif (( ghost[y * W + x] )); then
            cell_color "${ghost[y * W + x]}"; line+="${cc}${DIM}░░${RS}"
          else
            line+="${E}[38;5;238m· ${RS}"
          fi
        done ;;
    esac
    L[y + 1]="$line${GR}│${RS}"
  done
  L[H + 1]="${GR}╰────────────────────╯${RS}"

  local bbot="${GR}╰──────────────╯${RS}" brow v
  mini "$preview_shape"; local n1=$m1 n2=$m2
  mini "$hold_shape"; local h1=$m1 h2=$m2
  boxtop NEXT; L[0]+="  $btop"
  L[1]+="  ${GR}│${RS}   $n1   ${GR}│${RS}"
  L[2]+="  ${GR}│${RS}   $n2   ${GR}│${RS}"
  L[3]+="  $bbot"
  boxtop HOLD; L[4]+="  $btop"
  L[5]+="  ${GR}│${RS}   $h1   ${GR}│${RS}"
  L[6]+="  ${GR}│${RS}   $h2   ${GR}│${RS}"
  L[7]+="  $bbot"
  boxtop STATS; L[8]+="  $btop"
  srow() { printf -v brow '  %s│%s %s%-6s%s%s%7s%s%s│%s' "$GR" "$RS" "$DIM" "$1" "$RS" "$2" "$3" "$RS" "$GR" "$RS"; }
  srow Score "${E}[1;93m" "$score";            L[9]+=$brow
  srow Best  "${E}[36m" "$best_score";         L[10]+=$brow
  srow Level "${E}[1;95m" "$((level + 1))";    L[11]+=$brow
  srow Lines "${E}[32m" "$lines_total";        L[12]+=$brow
  L[13]+="  $bbot"
  L[H + 2]="${DIM} ←→ move  ↑/x rotate  z back  ↓ soft drop${RS}"
  L[H + 3]="${DIM} ␣ hard drop  c hold  p pause  r new  q quit${RS}"
}

paint() {
  local out="" i top left
  if (( sizechg )); then
    sizechg=0
    read -r rows cols < <(stty size 2>/dev/null) || true
    rows=${rows:-24} cols=${cols:-80}
    out="${E}[2J"
  fi
  if (( rows < CH || cols < CW )); then
    printf '%s%s[2J%s[1;1HPane too small: need %dx%d, have %dx%d%s' "$out" "$E" "$E" "$CW" "$CH" "$cols" "$rows" "$RS"
    sizechg=1
    return
  fi
  build
  top=$(( (rows - CH) / 2 + 1 )) left=$(( (cols - CW) / 2 + 1 ))
  for i in "${!L[@]}"; do
    out+="${E}[$((top + i));1H${E}[2K${E}[$((top + i));${left}H${L[i]}"
  done
  printf '%s' "$out"
}

readkey() { # timeout (s) -> key; arrows normalised to ESC [ A-D
  local k r
  IFS= read -rsn1 -t "$1" k || { (( $? > 128 )) && return 1; exit 0; }
  if [[ $k == "$E" ]]; then
    while IFS= read -rsn1 -t 0.02 r; do
      k+=$r
      [[ $r == [A-Za-z~] && ${#k} -gt 2 ]] && break
    done
  fi
  [[ $k == "${E}[1;"?[A-D] ]] && k="${E}[${k: -1}"
  [[ $k == "${E}O"[A-D] ]] && k="${E}[${k: -1}"
  key=$k
}

handle() {
  case $key in
    q | Q) exit 0 ;;
    r | R) reset; ready=0; start_clock; return ;;
  esac
  if (( ready )); then
    [[ $key == ' ' || $key == '' ]] && { ready=0; start_clock; }
    return
  fi
  if [[ $key == p || $key == P ]]; then
    (( over )) && return
    paused=$((!paused))
    (( paused )) || start_clock
    return
  fi
  (( over || paused )) && return
  case $key in
    "${E}[D" | a | A | h | H) move_x -1 ;;
    "${E}[C" | d | D | l | L) move_x 1 ;;
    "${E}[A" | w | W | k | K | x | X) try_rotate 1 ;;
    z | Z) try_rotate 3 ;;
    "${E}[B" | s | S | j | J) step_down 1; (( over )) || start_clock ;;
    ' ') hard_drop ;;
    c | C) do_hold ;;
  esac
}

reset
paint
while :; do
  now_ms
  if (( !ready && !paused && !over )); then
    ms=$(( next_drop - now ))
    (( ms < 1 )) && ms=1
    printf -v to '%d.%03d' $(( ms / 1000 )) $(( ms % 1000 ))
  else
    to=30
  fi
  if readkey "$to"; then handle; fi
  if (( !ready && !paused && !over )); then
    now_ms
    (( now >= next_drop )) && { step_down 0; (( over )) || start_clock; }
  fi
  # a held key queues repeats faster than we draw; paint only once they are consumed
  read -t 0 || paint
done
