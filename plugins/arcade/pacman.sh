#!/usr/bin/env bash
# Pac-Man: arrows/WASD/hjkl steer, p pauses, r restarts, q quits.
# Eat every dot, grab a power pellet (●) to turn the ghosts blue and eat them. Three lives.
set -u

MAZE=(
  '#####################'
  '#o.......#.#.......o#'
  '#.##.###.#.#.###.##.#'
  '#...................#'
  '#.##.#.#######.#.##.#'
  '#....#.........#....#'
  '#.##.#.###-###.#.##.#'
  '#....#.#     #.#....#'
  '#.##.#.#######.#.##.#'
  '#....#....#....#....#'
  '#.##.###.#.#.###.##.#'
  '#...................#'
  '#o.##.....#.....##.o#'
  '#####################'
)
MW=21 MH=14
DX=(0 1 0 -1) DY=(-1 0 1 0)
GCOL=(196 213 51 208)
GHOME_X=(8 9 11 12)
GCORNER_X=(19 1 19 1) GCORNER_Y=(1 1 12 12)

cache="${XDG_CACHE_HOME:-$HOME/.cache}/rig-arcade"
best=0
[[ -r $cache/pacman.best ]] && read -r best < "$cache/pacman.best"
[[ $best =~ ^[0-9]+$ ]] || best=0

cleanup() { printf '\e[?25h\e[0m\e[2J\e[H'; stty sane 2>/dev/null || true; }
trap cleanup EXIT
stty -echo -icanon 2>/dev/null || true
printf '\e[?25l\e[2J'

now() { echo "${EPOCHREALTIME/./}"; }
wall() { [[ ${MAZE[$2]:$1:1} == '#' ]]; }
open() { [[ ${MAZE[$2]:$1:1} != '#' && ${MAZE[$2]:$1:1} != '-' ]]; }

fill() {
  pellets=0 dots=()
  local x y c
  for ((y = 0; y < MH; y++)); do
    for ((x = 0; x < MW; x++)); do
      c=${MAZE[y]:x:1}
      case $c in
        .) dots[y * MW + x]=1; pellets=$((pellets + 1)) ;;
        o) dots[y * MW + x]=2; pellets=$((pellets + 1)) ;;
      esac
    done
  done
}

place() {
  px=10 py=11 pd=1 want=1 mouth=0
  local i
  for i in 0 1 2 3; do
    gx[i]=${GHOME_X[i]} gy[i]=7 gd[i]=0 gs[i]=0 gt[i]=$(( i * 14 ))
  done
  gx[0]=10 gy[0]=5 gs[0]=1 gt[0]=0
  fright=0 combo=1 ready=1
}

reset() {
  score=0 lives=3 level=1 over=0 paused=0 tick=0
  gx=() gy=() gd=() gs=() gt=()
  fill
  place
}

next_level() {
  level=$((level + 1))
  fill
  place
}

award() {
  score=$((score + $1))
  if (( score > best )); then
    best=$score
    mkdir -p "$cache" && echo "$best" > "$cache/pacman.best"
  fi
}

move_pac() {
  local nx ny
  nx=$(( px + DX[want] )) ny=$(( py + DY[want] ))
  open "$nx" "$ny" && pd=$want
  nx=$(( px + DX[pd] )) ny=$(( py + DY[pd] ))
  open "$nx" "$ny" || return
  px=$nx py=$ny mouth=$((!mouth))
  case ${dots[py * MW + px]:-} in
    1) award 10; pellets=$((pellets - 1)) ;;
    2) award 50; pellets=$((pellets - 1))
       fright=$(( 44 - 3 * level )); (( fright < 16 )) && fright=16
       combo=1
       local i
       for i in 0 1 2 3; do (( gs[i] == 1 )) && gd[i]=$(( (gd[i] + 2) % 4 )); done ;;
  esac
  unset "dots[py * MW + px]"
}

target() { # ghost -> tx ty
  case $1 in
    0) tx=$px ty=$py ;;
    1) tx=$(( px + 4 * DX[pd] )) ty=$(( py + 4 * DY[pd] )) ;;
    2) if (( RANDOM % 2 )); then tx=$px ty=$py; else tx=$(( RANDOM % MW )) ty=$(( RANDOM % MH )); fi ;;
    3) local dx=$(( gx[3] - px )) dy=$(( gy[3] - py ))
       if (( dx * dx + dy * dy > 36 )); then tx=$px ty=$py; else tx=${GCORNER_X[3]} ty=${GCORNER_Y[3]}; fi ;;
  esac
}

move_ghost() {
  local i=$1 d nd best_d=-1 best_dist=999999 nx ny dist tx ty
  local -a ok=()
  if (( gs[i] == 0 )); then
    if (( gt[i] > 0 )); then gt[i]=$((gt[i] - 1)); return; fi
    if (( gx[i] < 10 )); then gx[i]=$((gx[i] + 1))
    elif (( gx[i] > 10 )); then gx[i]=$((gx[i] - 1))
    else gy[i]=$((gy[i] - 1)); (( gy[i] == 5 )) && { gs[i]=1; gd[i]=$(( RANDOM % 2 * 2 + 1 )); }
    fi
    return
  fi
  for d in 0 1 2 3; do
    (( d == (gd[i] + 2) % 4 )) && continue
    open $(( gx[i] + DX[d] )) $(( gy[i] + DY[d] )) && ok+=("$d")
  done
  (( ${#ok[@]} )) || ok=("$(( (gd[i] + 2) % 4 ))")
  if (( fright > 0 )); then
    best_d=${ok[RANDOM % ${#ok[@]}]}
  else
    target "$i"
    for d in "${ok[@]}"; do
      nx=$(( gx[i] + DX[d] - tx )) ny=$(( gy[i] + DY[d] - ty ))
      dist=$(( nx * nx + ny * ny ))
      (( dist < best_dist )) && { best_dist=$dist best_d=$d; }
    done
  fi
  gd[i]=$best_d
  gx[i]=$(( gx[i] + DX[best_d] )) gy[i]=$(( gy[i] + DY[best_d] ))
}

collide() { # ghost on pacman: eaten when blue, fatal otherwise
  local i
  for i in 0 1 2 3; do
    (( gs[i] == 1 && gx[i] == px && gy[i] == py )) || continue
    if (( fright > 0 )); then
      award $(( 200 * combo )); combo=$((combo * 2))
      gx[i]=10 gy[i]=7 gs[i]=0 gt[i]=12
    else
      lives=$((lives - 1))
      if (( lives == 0 )); then over=1; else place; fi
      return
    fi
  done
}

step() {
  tick=$((tick + 1))
  local i
  move_pac
  collide
  (( over || ready )) && return
  if (( tick % 6 != 0 && !(fright > 0 && tick % 2 == 0) )); then
    for i in 0 1 2 3; do move_ghost "$i"; done
    collide
  fi
  (( fright > 0 )) && fright=$((fright - 1))
  (( pellets == 0 )) && next_level
}

banner() { # text colour: centred across the maze width
  local text=$1 colour=$2 w=$(( MW * 2 ))
  local pad=$(( (w - ${#text}) / 2 ))
  printf '%*s\e[%sm%s\e[0m%*s' "$pad" "" "$colour" "$text" $(( w - ${#text} - pad )) ""
}

draw() {
  local x y i k out line c hearts="" bar=""
  local w=$(( MW * 2 ))
  local -a gat=()
  for ((i = 0; i < w; i++)); do bar+="─"; done
  for ((i = 0; i < 3; i++)); do
    if (( i < lives )); then hearts+=$'\e[91m♥ '; else hearts+=$'\e[38;5;238m♥ '; fi
  done
  hearts+=$'\e[0m'
  for i in 0 1 2 3; do gat[gy[i] * MW + gx[i]]=$i; done
  local edge=$'\e['$(( w + 2 ))$'G\e[1;33m│\e[0m'
  local side=$'\e[1;33m│\e[0m'

  out=$'\e[H'
  out+=$'\e[1;33m╭'"$bar"$'╮\e[0m\n'
  line=$(printf ' \e[1;93mPAC-MAN\e[0m  \e[33m%d\e[0m pts · best \e[36m%d\e[0m · level %d' "$score" "$best" "$level")
  out+="$side$line"$'\e[K'"$edge"$'\n'
  out+="$side lives $hearts"$'\e[K'"$edge"$'\n'
  out+=$'\e[1;33m├'"$bar"$'┤\e[0m\n'

  local mid=$(( MH / 2 ))
  for ((y = 0; y < MH; y++)); do
    out+=$side
    if (( paused && y == mid )); then
      out+=$(banner "  PAUSED · p resumes  " "1;30;43")
    elif (( ready && y == mid + 2 )); then
      out+=$(banner "  press an arrow key  " "1;30;46")
    elif (( over && y == mid - 1 )); then
      out+=$(banner "      GAME OVER       " "1;97;41")
    elif (( over && y == mid )); then
      out+=$(banner "$(printf '  score %-6d  r again' "$score")" "1;97;41")
    else
      for ((x = 0; x < MW; x++)); do
        k=$(( y * MW + x ))
        c=${MAZE[y]:x:1}
        if (( x == px && y == py )); then
          if (( mouth )); then
            case $pd in
              0) out+=$'\e[1;93m▴●\e[0m' ;; 1) out+=$'\e[1;93m●▸\e[0m' ;;
              2) out+=$'\e[1;93m▾●\e[0m' ;; 3) out+=$'\e[1;93m◂●\e[0m' ;;
            esac
          else
            out+=$'\e[1;93m●●\e[0m'
          fi
        elif [[ -n ${gat[k]:-} ]]; then
          i=${gat[k]}
          if (( fright > 0 && gs[i] == 1 )); then
            if (( fright < 10 && fright % 2 )); then out+=$'\e[1;30;47m▟▙\e[0m'; else out+=$'\e[1;97;44m▟▙\e[0m'; fi
          else
            out+=$'\e[1;38;5;'"${GCOL[i]}"$'m▟▙\e[0m'
          fi
        elif [[ $c == '#' ]]; then
          out+=$'\e[48;5;19;38;5;33m▒▒\e[0m'
        elif [[ $c == '-' ]]; then
          out+=$'\e[38;5;213m━━\e[0m'
        else
          case ${dots[k]:-} in
            1) out+=$'\e[38;5;223m ·\e[0m' ;;
            2) out+=$'\e[1;97m ●\e[0m' ;;
            *) out+='  ' ;;
          esac
        fi
      done
    fi
    out+=$side$'\n'
  done
  out+=$'\e[1;33m╰'"$bar"$'╯\e[0m\n'
  out+=$'\e[2m arrows/wasd/hjkl steer · p pause · r restart · q quit\e[0m\e[K\n'
  printf '%b' "$out"
}

handle() { # key: returns 0 when the screen needs a redraw before the next tick
  case $1 in
    $'\e[A' | $'\eOA' | w | W | k | K) want=0; ready=0 ;;
    $'\e[B' | $'\eOB' | s | S | j | J) want=2; ready=0 ;;
    $'\e[C' | $'\eOC' | d | D | l | L) want=1; ready=0 ;;
    $'\e[D' | $'\eOD' | a | A | h | H) want=3; ready=0 ;;
    p | P) (( !over && !ready )) && paused=$((!paused)); return 0 ;;
    r | R) reset; return 0 ;;
    q | Q) exit 0 ;;
  esac
  return 1
}

reset
draw
while :; do
  tickus=$(( 170000 - 10000 * (level - 1) ))
  (( tickus < 90000 )) && tickus=90000
  deadline=$(( $(now) + tickus ))
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
    handle "$key" && draw
  done
  (( !over && !paused && !ready )) && step
  draw
done
