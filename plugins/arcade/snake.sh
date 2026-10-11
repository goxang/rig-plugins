#!/usr/bin/env bash
# Snake: arrows/WASD/hjkl steer, +/space faster and - slower (one level per press), p pauses, r restarts, q quits.
# Walls wrap: leave one side, come back on the opposite one.
set -u

read -r rows cols < <(stty size 2>/dev/null || echo "24 80")
W=$(( (cols - 4) / 2 )) H=$(( rows - 7 ))
(( W > 40 )) && W=40
(( H > 20 )) && H=20
(( W < 12 )) && W=12
(( H < 8 )) && H=8
GW=$(( W * 2 ))

cache="${XDG_CACHE_HOME:-$HOME/.cache}/rig-arcade"
best=0
[[ -r $cache/snake.best ]] && read -r best < "$cache/snake.best"
[[ $best =~ ^[0-9]+$ ]] || best=0

cleanup() { printf '\e[?25h\e[0m\e[2J\e[H'; stty sane 2>/dev/null || true; }
trap cleanup EXIT
stty -echo -icanon 2>/dev/null || true
printf '\e[?25l\e[2J'

# tick length per level 1..9, in microseconds
declare -a TICK=(0 230000 195000 165000 140000 118000 98000 82000 68000 56000)

now() { echo "${EPOCHREALTIME/./}"; }

reset() {
  qx=() qy=() occ=()
  head=0 tail=0
  local i x0=$(( W / 2 - 2 )) y0=$(( H / 2 ))
  for ((i = 0; i < 4; i++)); do qx[head]=$((x0 + i)) qy[head]=$y0 occ[y0 * W + x0 + i]=1; head=$((head + 1)); done
  dx=1 dy=0 turns=() score=0 eaten=0
  level=${START_LEVEL:-3} over=0 paused=0 ready=1
  place
}

place() {
  while :; do
    fx=$(( RANDOM % W )) fy=$(( RANDOM % H ))
    [[ -z ${occ[fy * W + fx]:-} ]] && return
  done
}

turn() { # dx dy: queued, so two quick turns between ticks both count
  ready=0
  local ldx=$dx ldy=$dy
  if (( ${#turns[@]} )); then
    local last=${turns[${#turns[@]} - 1]}
    ldx=${last% *} ldy=${last#* }
  fi
  (( $1 == -ldx && $2 == -ldy )) && return
  (( $1 == ldx && $2 == ldy )) && return
  (( ${#turns[@]} < 3 )) && turns+=("$1 $2")
}

step() {
  if (( ${#turns[@]} )); then
    dx=${turns[0]% *} dy=${turns[0]#* }
    turns=("${turns[@]:1}")
  fi
  local hx=$(( (qx[head - 1] + dx + W) % W )) hy=$(( (qy[head - 1] + dy + H) % H ))
  local grow=0
  (( hx == fx && hy == fy )) && grow=1
  if (( !grow )); then # the tail leaves its cell before the head arrives
    unset "occ[qy[tail] * W + qx[tail]]"
    unset "qx[tail]" "qy[tail]"
    tail=$((tail + 1))
  fi
  if [[ -n ${occ[hy * W + hx]:-} ]]; then
    over=1
    return
  fi
  qx[head]=$hx qy[head]=$hy occ[hy * W + hx]=1
  head=$((head + 1))
  if (( grow )); then
    eaten=$((eaten + 1))
    score=$(( score + 10 * level ))
    (( eaten % 5 == 0 && level < 9 )) && level=$((level + 1))
    place
  fi
  if (( score > best )); then
    best=$score
    mkdir -p "$cache" && echo "$best" > "$cache/snake.best"
  fi
}

banner() { # row text colour: a centred bar across the board row
  local text=$1 colour=$2
  local pad=$(( (GW - ${#text}) / 2 ))
  printf '%*s\e[%sm%s\e[0m%*s' "$pad" "" "$colour" "$text" $(( GW - ${#text} - pad )) ""
}

draw() {
  local len=$(( head - tail )) y x i k out line c
  local -a body=()
  for ((i = tail; i < head; i++)); do body[qy[i] * W + qx[i]]=$(( i - tail )); done
  local hi=$(( head - 1 ))
  local hpos=$(( qy[hi] * W + qx[hi] ))

  local bar="" lvl_bar=""
  local -a ramp=(0 46 82 118 154 190 226 220 214 208)
  for ((i = 0; i < GW; i++)); do bar+="─"; done
  for ((i = 1; i <= 9; i++)); do
    if (( i <= level )); then lvl_bar+=$'\e[38;5;'${ramp[i]}$'m■'; else lvl_bar+=$'\e[38;5;238m■'; fi
  done
  lvl_bar+=$'\e[0m'
  local edge=$'\e['$(( GW + 2 ))$'G\e[1;32m│\e[0m'

  out=$'\e[H'
  out+=$'\e[1;32m╭'"$bar"$'╮\e[0m\n'
  line=$(printf ' \e[1mSNAKE\e[0m  \e[33m%d\e[0m pts · best \e[36m%d\e[0m · length %d' "$score" "$best" "$len")
  out+=$'\e[1;32m│\e[0m'"$line"$'\e[K'"$edge"$'\n'
  line=" speed $lvl_bar \e[2mlevel $level\e[0m"
  out+=$'\e[1;32m│\e[0m'"$line"$'\e[K'"$edge"$'\n'
  out+=$'\e[1;32m├'"$bar"$'┤\e[0m\n'

  local mid=$(( H / 2 ))
  for ((y = 0; y < H; y++)); do
    out+=$'\e[1;32m│\e[0m'
    if (( paused && y == mid )); then
      out+=$(banner "  PAUSED · p resumes  " "1;30;43")
    elif (( ready && y == mid )); then
      out+=$(banner "  press an arrow key  " "1;30;46")
    elif (( over && y == mid - 1 )); then
      out+=$(banner "      GAME OVER       " "1;97;41")
    elif (( over && y == mid )); then
      out+=$(banner "$(printf '  score %-6d  r again' "$score")" "1;97;41")
    else
      for ((x = 0; x < W; x++)); do
        k=$(( y * W + x ))
        if (( k == hpos )); then
          out+=$'\e[48;5;51;38;5;16m••\e[0m'
        elif [[ -n ${body[k]:-} ]]; then
          c=$(( 16 + 6 * (1 + body[k] * 4 / (len > 1 ? len - 1 : 1)) ))
          out+=$'\e[48;5;'"$c"$'m  \e[0m'
        elif (( x == fx && y == fy )); then
          out+=$'\e[1;91m●●\e[0m'
        elif (( (x + y) % 2 )); then
          out+=$'\e[48;5;235m  \e[0m'
        else
          out+=$'\e[48;5;234m  \e[0m'
        fi
      done
    fi
    out+=$'\e[1;32m│\e[0m\n'
  done
  out+=$'\e[1;32m╰'"$bar"$'╯\e[0m\n'
  out+=$'\e[2m arrows/wasd/hjkl steer · +/space faster · - slower · p pause · r restart · q quit\e[0m\e[K\n'
  printf '%b' "$out"
}

speed_key() { # one level per press: key-repeat events (under 120ms apart) are ignored
  local t gap
  t=$(now); gap=$(( t - last_speed ))
  last_speed=$t
  (( gap < 120000 )) && return 1
  level=$(( level + $1 ))
  (( level > 9 )) && level=9
  (( level < 1 )) && level=1
  return 0
}

handle() { # key: returns 0 when the screen needs a redraw before the next tick
  case $1 in
    $'\e[A' | $'\eOA' | w | W | k | K) turn 0 -1 ;;
    $'\e[B' | $'\eOB' | s | S | j | J) turn 0 1 ;;
    $'\e[C' | $'\eOC' | d | D | l | L) turn 1 0 ;;
    $'\e[D' | $'\eOD' | a | A | h | H) turn -1 0 ;;
    + | = | ']' | ' ') speed_key 1 ;;
    - | _ | '[') speed_key -1 ;;
    p | P) (( !over && !ready )) && paused=$((!paused)); return 0 ;;
    r | R) reset; return 0 ;;
    q | Q) exit 0 ;;
  esac
  return 1
}

last_speed=0
reset
draw
while :; do
  tick=${TICK[level]}
  deadline=$(( $(now) + tick ))
  while :; do
    left=$(( deadline - $(now) ))
    (( left <= 0 )) && break
    printf -v wait '0.%06d' "$left"
    key=""
    IFS= read -rsn1 -t "$wait" key || continue
    if [[ $key == $'\e' ]]; then
      seq=""
      while IFS= read -rsn1 -t 0.003 c; do seq+=$c; [[ $c == [A-Za-z~] ]] && break; done
      key+=${seq/#\[1;?/[}
    fi
    if (( ${#turns[@]} < 3 )) || [[ $key == [pPrRqQ+=_\]\[\ -] ]]; then
      handle "$key" && draw
    fi
  done
  if (( !over && !paused && !ready )); then
    step
  fi
  draw
done
