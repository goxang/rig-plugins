#!/usr/bin/env bash
# 2048 for a terminal pane: arrows/WASD/hjkl slide, u undoes, r restarts, q quits.
set -u

E=$'\e'
RS="${E}[0m" DIM="${E}[2m" BD="${E}[1m" GR="${E}[38;5;244m" PB="${E}[48;5;238m"

cache="${XDG_CACHE_HOME:-$HOME/.cache}/rig-arcade"
best=0
[[ -r $cache/2048.best ]] && read -r best < "$cache/2048.best"
[[ $best =~ ^[0-9]+$ ]] || best=0
save_best() { { mkdir -p "$cache" && printf '%s\n' "$best" > "$cache/2048.best"; } 2>/dev/null; }

saved_stty=$(stty -g 2>/dev/null || true)
cleanup() { save_best; printf '%s[?25h%s[0m%s[2J%s[H' "$E" "$E" "$E" "$E"; stty "${saved_stty:-sane}" 2>/dev/null || stty sane 2>/dev/null || true; }
trap cleanup EXIT
trap 'exit 0' INT TERM HUP
trap 'sizechg=1' WINCH
stty -echo -icanon min 1 time 0 2>/dev/null || true
printf '%s[?25l%s[2J' "$E" "$E"

rows=24 cols=80 sizechg=1 TH=3 TW=7 CW=33 CH=22
declare -a board=() undo_board=() L=() slide_out=()
score=0 over=0 won=0 keep_going=0 can_undo=0 undo_score=0 slide_pts=0 key="" b=""

spawn() {
  local -a empty=()
  local i
  for ((i = 0; i < 16; i++)); do (( board[i] == 0 )) && empty+=("$i"); done
  (( ${#empty[@]} == 0 )) && return
  board[${empty[RANDOM % ${#empty[@]}]}]=$(( RANDOM % 10 == 0 ? 4 : 2 ))
}

reset() {
  board=(0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0)
  score=0 over=0 won=0 keep_going=0 can_undo=0
  spawn
  spawn
}

slide_line() {
  local -a a=("$@") nz=() merged=()
  local i n val
  slide_pts=0
  for ((i = 0; i < 4; i++)); do (( a[i] != 0 )) && nz+=("${a[i]}"); done
  n=${#nz[@]}
  for ((i = 0; i < n; i++)); do
    if (( i + 1 < n && nz[i] == nz[i + 1] )); then
      val=$(( nz[i] * 2 ))
      merged+=("$val")
      slide_pts=$(( slide_pts + val ))
      i=$((i + 1))
    else
      merged+=("${nz[i]}")
    fi
  done
  while (( ${#merged[@]} < 4 )); do merged+=(0); done
  slide_out=("${merged[@]}")
}

line_idx() { # dir i j -> idx; dir 0=left 1=right 2=up 3=down
  case $1 in
    0) idx=$(( $2 * 4 + $3 )) ;;
    1) idx=$(( $2 * 4 + 3 - $3 )) ;;
    2) idx=$(( $3 * 4 + $2 )) ;;
    *) idx=$(( (3 - $3) * 4 + $2 )) ;;
  esac
}

move_board() {
  local dir=$1 moved=0 pts=0 i j idx
  local -a line=() prev=("${board[@]}")
  for ((i = 0; i < 4; i++)); do
    line=()
    for ((j = 0; j < 4; j++)); do line_idx "$dir" "$i" "$j"; line+=("${board[idx]}"); done
    slide_line "${line[@]}"
    pts=$(( pts + slide_pts ))
    for ((j = 0; j < 4; j++)); do
      line_idx "$dir" "$i" "$j"
      (( board[idx] != slide_out[j] )) && { moved=1; board[idx]=${slide_out[j]}; }
    done
  done
  (( moved )) || return
  undo_board=("${prev[@]}") undo_score=$score can_undo=1
  score=$(( score + pts ))
  if (( score > best )); then best=$score; save_best; fi
  spawn
  check_status
}

check_status() {
  local i r c val
  if (( !won && !keep_going )); then
    for ((i = 0; i < 16; i++)); do (( board[i] >= 2048 )) && { won=1; break; }; done
  fi
  for ((i = 0; i < 16; i++)); do (( board[i] == 0 )) && return; done
  for ((r = 0; r < 4; r++)); do
    for ((c = 0; c < 4; c++)); do
      val=${board[r * 4 + c]}
      (( c < 3 && val == board[r * 4 + c + 1] )) && return
      (( r < 3 && val == board[(r + 1) * 4 + c] )) && return
    done
  done
  over=1
}

undo() {
  (( can_undo )) || return
  board=("${undo_board[@]}") score=$undo_score can_undo=0 over=0 won=0
  check_status
  over=0
}

tile_style() { # value -> ts (background + foreground)
  case $1 in
    0)    ts="${E}[48;5;240m" ;;
    2)    ts="${E}[48;5;254;38;5;238m" ;;
    4)    ts="${E}[48;5;223;38;5;238m" ;;
    8)    ts="${E}[48;5;215;1;38;5;231m" ;;
    16)   ts="${E}[48;5;209;1;38;5;231m" ;;
    32)   ts="${E}[48;5;203;1;38;5;231m" ;;
    64)   ts="${E}[48;5;196;1;38;5;231m" ;;
    128)  ts="${E}[48;5;221;1;38;5;238m" ;;
    256)  ts="${E}[48;5;220;1;38;5;238m" ;;
    512)  ts="${E}[48;5;214;1;38;5;238m" ;;
    1024) ts="${E}[48;5;208;1;38;5;231m" ;;
    2048) ts="${E}[48;5;226;1;38;5;16m" ;;
    *)    ts="${E}[48;5;93;1;38;5;231m" ;;
  esac
}

bar() { # width style text -> b
  local l=$(( ($1 - ${#3}) / 2 ))
  printf -v b '%s%*s%s%*s%s' "$2" "$l" '' "$3" "$(( $1 - ${#3} - l ))" '' "$RS"
}

hr() { printf -v b '%*s' "$1" ''; b=${b// /─}; }

layout() { # tall panes get 3-row tiles, short ones single-row
  if (( rows >= 22 )); then TH=3 TW=7 CW=33 CH=22; else TH=1 TW=6 CW=29 CH=14; fi
}

build() {
  local r c rr val v pad t top ln i
  local -a panel=()
  local gap="${PB}$(printf '%*s' "$CW" '')${RS}"
  panel+=("$gap")
  for ((r = 0; r < 4; r++)); do
    for ((rr = 0; rr < TH; rr++)); do
      ln="${PB} ${RS}"
      for ((c = 0; c < 4; c++)); do
        val=${board[r * 4 + c]}
        tile_style "$val"
        v=""
        if (( rr == TH / 2 && val )); then v=$val; fi
        pad=$(( (TW - ${#v}) / 2 ))
        printf -v t '%s%*s%s%*s%s%s %s' "$ts" "$pad" '' "$v" "$(( TW - ${#v} - pad ))" '' "$RS" "$PB" "$RS"
        ln+=$t
      done
      panel+=("$ln")
    done
    panel+=("$gap")
  done

  top=0
  for ((i = 0; i < 16; i++)); do (( board[i] > top )) && top=${board[i]}; done
  local ov=$(( (${#panel[@]} - 3) / 2 ))
  if (( over )); then
    bar "$CW" "${E}[1;97;41m" ""; panel[ov]=$b
    bar "$CW" "${E}[1;97;41m" "GAME OVER"; panel[ov + 1]=$b
    bar "$CW" "${E}[97;41m" "u undo · r restart"; panel[ov + 2]=$b
  elif (( won && !keep_going )); then
    bar "$CW" "${E}[1;30;43m" ""; panel[ov]=$b
    bar "$CW" "${E}[1;30;43m" "YOU REACHED 2048!"; panel[ov + 1]=$b
    bar "$CW" "${E}[30;43m" "c continue · r restart"; panel[ov + 2]=$b
  fi

  L=()
  printf -v t ' top %d ' "$top"
  hr $(( CW - 10 - ${#t} ))
  L[0]="${E}[1;33m╭─ ${RS}${BD}2048${RS}${E}[1;33m ${b}${t}─╮${RS}"
  printf -v t '%s│%s %sScore%s %s%-6d%s  %sBest%s %s%-6d%s' "${E}[1;33m" "$RS" "$DIM" "$RS" "${E}[33m" "$score" "$RS" "$DIM" "$RS" "${E}[36m" "$best" "$RS"
  printf -v pad '%*s' "$(( CW - 28 ))" ''
  L[1]="$t$pad${E}[1;33m│${RS}"
  hr $(( CW - 2 ))
  L[2]="${E}[1;33m╰${b}╯${RS}"
  for i in "${!panel[@]}"; do L[i + 3]=${panel[i]}; done
  i=$(( ${#panel[@]} + 3 ))
  L[i]="${DIM} ←↑↓→ / wasd / hjkl  slide${RS}"
  L[i + 1]="${DIM} u undo  r restart  q quit${RS}"
}

paint() {
  local out="" i top left
  if (( sizechg )); then
    sizechg=0
    read -r rows cols < <(stty size 2>/dev/null) || true
    rows=${rows:-24} cols=${cols:-80}
    out="${E}[2J"
  fi
  layout
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
    r | R) reset; return ;;
    u | U) undo; return ;;
  esac
  if (( won && !keep_going )); then
    [[ $key == c || $key == C ]] && keep_going=1
    return
  fi
  (( over )) && return
  case $key in
    "${E}[D" | a | A | h | H) move_board 0 ;;
    "${E}[C" | d | D | l | L) move_board 1 ;;
    "${E}[A" | w | W | k | K) move_board 2 ;;
    "${E}[B" | s | S | j | J) move_board 3 ;;
  esac
}

reset
paint
while :; do
  if readkey 30; then handle; fi
  # held keys queue faster than we draw; paint once they are consumed
  read -t 0 || paint
done
