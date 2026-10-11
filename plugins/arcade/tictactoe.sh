#!/usr/bin/env bash
# Tic-Tac-Toe for a terminal pane: arrows/WASD/hjkl move, 1-9 place directly, space/enter place, m mode, t side, r new game, q quit.
set -u

E=$'\e'
RS="${E}[0m" DIM="${E}[2m" BD="${E}[1m" GR="${E}[38;5;244m"
CX="${E}[1;38;5;51m" CO="${E}[1;38;5;220m"

cache="${XDG_CACHE_HOME:-$HOME/.cache}/rig-arcade"
best_streak=0
[[ -r $cache/tictactoe.best ]] && read -r best_streak < "$cache/tictactoe.best"
[[ $best_streak =~ ^[0-9]+$ ]] || best_streak=0
save_best() { { mkdir -p "$cache" && printf '%s\n' "$best_streak" > "$cache/tictactoe.best"; } 2>/dev/null; }

saved_stty=$(stty -g 2>/dev/null || true)
cleanup() { save_best; printf '%s[?25h%s[0m%s[2J%s[H' "$E" "$E" "$E" "$E"; stty "${saved_stty:-sane}" 2>/dev/null || stty sane 2>/dev/null || true; }
trap cleanup EXIT
trap 'exit 0' INT TERM HUP
trap 'sizechg=1' WINCH
stty -echo -icanon min 1 time 0 2>/dev/null || true
printf '%s[?25l%s[2J' "$E" "$E"

rows=24 cols=80 sizechg=1 CW=34 CH=18 cw=7 chh=3
WINS=("0 1 2" "3 4 5" "6 7 8" "0 3 6" "1 4 7" "2 5 8" "0 4 8" "2 4 6")
declare -a board=() win_line=() L=()
cursor=4 turn=X mode=cpu me=X over=0 winner="" thinking=0
score_x=0 score_o=0 score_draw=0 streak=0 pick=-1 key="" b=""

other() { [[ $1 == X ]] && echo O || echo X; }

reset_board() {
  board=(" " " " " " " " " " " " " " " " " ")
  cursor=4 turn=X over=0 winner="" thinking=0 win_line=()
}

check_winner() {
  local combo a b c cell full=1
  for combo in "${WINS[@]}"; do
    set -- $combo
    a=$1 b=$2 c=$3
    if [[ ${board[a]} != " " && ${board[a]} == "${board[b]}" && ${board[b]} == "${board[c]}" ]]; then
      winner=${board[a]} win_line=("$a" "$b" "$c") over=1
      return
    fi
  done
  for cell in "${board[@]}"; do [[ $cell == " " ]] && full=0; done
  (( full )) && { winner=draw over=1; }
}

finish() { # tally a finished game
  case $winner in
    draw) score_draw=$((score_draw + 1)); [[ $mode == cpu ]] && streak=0 ;;
    X) score_x=$((score_x + 1)) ;;
    O) score_o=$((score_o + 1)) ;;
  esac
  if [[ $mode == cpu && $winner != draw ]]; then
    if [[ $winner == "$me" ]]; then
      streak=$((streak + 1))
      (( streak > best_streak )) && { best_streak=$streak; save_best; }
    else
      streak=0
    fi
  fi
}

find_two() { # symbol -> pick: the empty cell of a line holding two of symbol
  local combo i cnt emp
  for combo in "${WINS[@]}"; do
    cnt=0 emp=-1
    for i in $combo; do
      if [[ ${board[i]} == "$1" ]]; then cnt=$((cnt + 1)); elif [[ ${board[i]} == " " ]]; then emp=$i; fi
    done
    (( cnt == 2 && emp >= 0 )) && { pick=$emp; return 0; }
  done
  return 1
}

free_of() { # cells... -> free (those still empty)
  free=()
  local i
  for i in "$@"; do [[ ${board[i]} == " " ]] && free+=("$i"); done
}

cpu_move() {
  local cpu opp
  cpu=$(other "$me") opp=$me
  if ! find_two "$cpu" && ! find_two "$opp"; then
    if [[ ${board[4]} == " " ]]; then
      pick=4
    else
      # against two opposite corners a corner reply loses to a fork, so take an edge
      if [[ ( ${board[0]} == "$opp" && ${board[8]} == "$opp" ) || ( ${board[2]} == "$opp" && ${board[6]} == "$opp" ) ]]; then
        free_of 1 3 5 7
      else
        free_of 0 2 6 8
      fi
      (( ${#free[@]} )) || free_of 0 1 2 3 5 6 7 8
      pick=${free[RANDOM % ${#free[@]}]}
    fi
  fi
  board[pick]=$cpu
  check_winner
  if (( over )); then finish; else turn=$me; fi
}

cpu_turn() {
  thinking=1
  paint
  sleep 0.35
  while IFS= read -rsn1 -t 0.01 _; do :; done
  thinking=0
  cpu_move
}

place() {
  local idx=$1
  (( over )) && return
  [[ ${board[idx]} == " " ]] || return
  board[idx]=$turn
  check_winner
  if (( over )); then finish; return; fi
  if [[ $mode == cpu ]]; then
    turn=$(other "$me")
    cpu_turn
  else
    turn=$(other "$turn")
  fi
}

new_game() {
  reset_board
  [[ $mode == cpu && $me == O ]] && { turn=X; cpu_turn; }
}

is_win_cell() { local w; for w in "${win_line[@]}"; do (( w == $1 )) && return 0; done; return 1; }

bar() { # width style text -> b
  local l=$(( ($1 - ${#3}) / 2 ))
  printf -v b '%s%*s%s%*s%s' "$2" "$l" '' "$3" "$(( $1 - ${#3} - l ))" '' "$RS"
}

hr() { printf -v b '%*s' "$1" ''; b=${b// /─}; }

layout() {
  if (( rows >= 18 )); then CH=18 cw=7 chh=3; else CH=14 cw=5 chh=1; fi
}

cell_line() { # idx line -> cl
  local idx=$1 r=$2 bg="" v=${board[$1]} st="" ch=" " pad
  if is_win_cell "$idx"; then bg="${E}[48;5;28m"
  elif (( idx == cursor && !over )); then bg="${E}[48;5;24m"; fi
  if (( r == chh / 2 )); then
    if [[ $v == X ]]; then st=$CX ch=X
    elif [[ $v == O ]]; then st=$CO ch=O
    else st="${DIM}" ch=$((idx + 1)); fi
    is_win_cell "$idx" && st="${E}[1;97m"
  fi
  pad=$(( (cw - 1) / 2 ))
  printf -v cl '%s%*s%s%s%s%s%*s%s' "$bg" "$pad" '' "$st" "$ch" "$RS" "$bg" "$(( cw - 1 - pad ))" '' "$RS"
}

build() {
  local r c rr cl t sep top mid bot ln i lead status st
  hr "$cw"; sep=$b
  lead=$(printf '%*s' "$(( (CW - (3 * cw + 4)) / 2 ))" '')
  top="${GR}╭${sep}┬${sep}┬${sep}╮${RS}"
  mid="${GR}├${sep}┼${sep}┼${sep}┤${RS}"
  bot="${GR}╰${sep}┴${sep}┴${sep}╯${RS}"

  L=()
  hr $(( CW - 16 ))
  L[0]="${E}[1;35m╭─ ${RS}${BD}TIC-TAC-TOE${RS}${E}[1;35m ${b}╮${RS}"
  local mtxt="vs CPU"
  [[ $mode == 2p ]] && mtxt="2 players"
  printf -v t '%s│%s %sX%s %-2d %sO%s %-2d %sdraw%s %-2d  %s%-9s%s' "${E}[1;35m" "$RS" "$CX" "$RS" "$score_x" "$CO" "$RS" "$score_o" "$DIM" "$RS" "$score_draw" "${E}[33m" "$mtxt" "$RS"
  printf -v i '%*s' "$(( CW - 31 ))" ''
  L[1]="$t$i${E}[1;35m│${RS}"
  printf -v t '%s│%s %sstreak%s %-3d  %sbest%s %-3d' "${E}[1;35m" "$RS" "$DIM" "$RS" "$streak" "$DIM" "$RS" "$best_streak"
  printf -v i '%*s' "$(( CW - 23 ))" ''
  L[2]="$t$i${E}[1;35m│${RS}"
  hr $(( CW - 2 ))
  L[3]="${E}[1;35m╰${b}╯${RS}"

  if (( over )); then
    if [[ $winner == draw ]]; then bar "$CW" "${E}[1;30;43m" "Draw!  enter for next game"
    elif [[ $mode == 2p || $winner == "$me" ]]; then
      [[ $mode == 2p ]] && t="$winner wins!" || t="You win!"
      bar "$CW" "${E}[1;97;42m" "$t  enter for next game"
    else bar "$CW" "${E}[1;97;41m" "CPU wins  enter for next game"; fi
  elif (( thinking )); then bar "$CW" "${E}[1;30;47m" "CPU is thinking..."
  elif [[ $mode == cpu ]]; then bar "$CW" "${E}[1;30;46m" "Your turn ($me)"
  else bar "$CW" "${E}[1;30;46m" "$turn's turn"; fi
  L[4]=$b

  i=5
  L[i]="$lead$top"; i=$((i + 1))
  for ((r = 0; r < 3; r++)); do
    for ((rr = 0; rr < chh; rr++)); do
      ln="$lead${GR}│${RS}"
      for ((c = 0; c < 3; c++)); do
        cell_line $((r * 3 + c)) "$rr"
        ln+="$cl${GR}│${RS}"
      done
      L[i]=$ln; i=$((i + 1))
    done
    if (( r < 2 )); then L[i]="$lead$mid"; else L[i]="$lead$bot"; fi
    i=$((i + 1))
  done
  L[i]="${DIM} arrows/hjkl move · 1-9 or space place${RS}"
  L[i + 1]="${DIM} m mode · t side · r new · q quit${RS}"
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
    r | R) new_game; return ;;
    m | M) [[ $mode == cpu ]] && mode=2p || mode=cpu; streak=0; new_game; return ;;
    t | T) [[ $mode == cpu ]] && { me=$(other "$me"); streak=0; new_game; }; return ;;
  esac
  if (( over )); then
    [[ $key == ' ' || $key == '' ]] && new_game
    return
  fi
  case $key in
    "${E}[A" | w | W | k | K) (( cursor >= 3 )) && cursor=$((cursor - 3)) ;;
    "${E}[B" | s | S | j | J) (( cursor <= 5 )) && cursor=$((cursor + 3)) ;;
    "${E}[D" | a | A | h | H) (( cursor % 3 > 0 )) && cursor=$((cursor - 1)) ;;
    "${E}[C" | d | D | l | L) (( cursor % 3 < 2 )) && cursor=$((cursor + 1)) ;;
    ' ' | '') place "$cursor" ;;
    [1-9]) cursor=$((key - 1)); place "$cursor" ;;
  esac
}

reset_board
paint
while :; do
  if readkey 30; then handle; fi
  read -t 0 || paint
done
